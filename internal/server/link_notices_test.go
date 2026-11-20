package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Opers see link events in InspIRCd's words: how a link was checked, the
// burst, when it lags, and the three lines of a split.
func TestLinkNotices(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, want := range []string{
		"Verified server connection leaf.test.local (",
		"Bursting to leaf.test.local (Authentication: TLS certificate fingerprint, TLS 1.3 ",
		"Finished bursting to leaf.test.local.",
		"Received end of netburst from leaf.test.local (burst time: ",
		"is up: BeeIRCd", // the software, in the log
	} {
		if !hub.logHas(want) {
			t.Errorf("hub: no %q in:\n%s", want, hub.log.String())
		}
	}
	if !leaf.logHas("Connection to hub.test.local[127.0.0.1] started.") {
		t.Errorf("leaf: no started line in:\n%s", leaf.log.String())
	}
	if hub.logHas("\x02") {
		t.Errorf("bold codes in the log:\n%s", hub.log.String())
	}
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	op.send("MODE op +s")
	op.send("PING :umode")
	op.expect(" :umode")
	login(t, leaf.s, "bob")
	login(t, leaf.s, "ben")
	hub.seen(t, "bob")
	hub.seen(t, "ben")

	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.since = time.Now().Add(-(3*time.Minute + 4*time.Second))
			l.pingSent = time.Now().Add(-linkPingWarn - time.Second)
			hub.s.linkTick(time.Now())
			hub.s.linkCommand(l, mustParse(t, ":"+leaf.sid+" PONG leaf.test.local :hub.test.local"))
			hub.s.dropLink(l, "test split")
		}
	})
	for _, want := range []string{
		"*** LINK: Server \x02leaf.test.local\x02 has not responded to PING for 31 seconds, high latency.",
		"*** LINK: Server \x02leaf.test.local\x02 is responding to PING again (lag ",
		"*** LINK: Server \x02leaf.test.local\x02 split: test split",
		"*** LINK: Netsplit complete, lost \x022\x02 users on \x021\x02 server.",
		"*** LINK: Connection to '\x02leaf.test.local\x02' was established for 3 minutes and 4 seconds",
	} {
		op.expect(want)
	}
	for _, want := range []string{
		"Server leaf.test.local split: test split",
		"Netsplit complete, lost 2 users on 1 server.",
		"Connection to 'leaf.test.local' was established for 3 minutes and 4 seconds",
	} {
		if !hub.logHas(want) {
			t.Errorf("no %q in:\n%s", want, hub.log.String())
		}
	}
}

// Every failed try to connect is a notice, not only the first one.
func TestLinkDialFailedEveryTry(t *testing.T) {
	_, leaf := linkedPair(t)
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
	leaf.s.onLoop(func() {
		leaf.s.linkDialFailed("hub.test.local", refused)
		leaf.s.linkDialFailed("hub.test.local", refused)
		leaf.s.linkDialFailed("hub.test.local", context.DeadlineExceeded)
	})
	if n := strings.Count(leaf.log.String(), "CONNECT: Error connecting hub.test.local: connect: connection refused."); n != 2 {
		t.Errorf("%d refused notices, want 2:\n%s", n, leaf.log.String())
	}
	if !leaf.logHas("CONNECT: Error connecting hub.test.local (timeout of 15 seconds)") {
		t.Errorf("no timeout notice:\n%s", leaf.log.String())
	}
}

// A failed connection on the link port is one notice per address every 10
// seconds; the log has each one.
func TestLinkPortFailedThrottle(t *testing.T) {
	hub, _ := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	op.send("MODE op +s")
	op.send("PING :umode")
	op.expect(" :umode")
	a := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 40000}
	a2 := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 40001}
	b := &net.TCPAddr{IP: net.ParseIP("192.0.2.2"), Port: 40000}
	hub.s.onLoop(func() {
		hub.s.linkPortFailed(a, "TLS handshake failed: first")
		hub.s.linkPortFailed(a2, "TLS handshake failed: second")
		hub.s.linkPortFailed(b, "TLS handshake failed: other")
	})
	op.expect("*** LINK: Server connection from 192.0.2.1 failed: TLS handshake failed: first")
	op.expect("*** LINK: Server connection from 192.0.2.2 failed: TLS handshake failed: other")
	op.quiet("192.0.2.1")

	// 10 seconds on, the same address is told again
	hub.s.onLoop(func() {
		hub.s.noticeAt["linkport:192.0.2.1"] = time.Now().Add(-linkPortNoticeEvery)
		hub.s.linkPortFailed(a, "TLS handshake failed: later")
	})
	op.expect("*** LINK: Server connection from 192.0.2.1 failed: TLS handshake failed: later")
}

func TestLongDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                 "0 seconds",
		time.Second:                       "1 second",
		45 * time.Second:                  "45 seconds",
		15*time.Minute + 3*time.Second:    "15 minutes and 3 seconds",
		time.Hour + 30*time.Second:        "1 hour and 30 seconds",
		4*24*time.Hour + 2*time.Hour:      "4 days and 2 hours",
		9 * 24 * time.Hour:                "1 week and 2 days",
		400 * 24 * time.Hour:              "1 year and 5 weeks",
		400*24*time.Hour + 61*time.Second: "1 year, 5 weeks, 1 minute and 1 second",
	} {
		if got := longDuration(d); got != want {
			t.Errorf("longDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func mustParse(t *testing.T, line string) irc.Message {
	t.Helper()
	m, err := irc.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
