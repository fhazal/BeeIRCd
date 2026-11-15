package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Opers see how a link is secured, how big the other side's burst was,
// when it lags, and how many users a split took.
func TestLinkNotices(t *testing.T) {
	hub, leaf := linkedPair(t)
	if !hub.logHas("established over TLS 1.3 (") || !hub.logHas("certificate pinned: BeeIRCd") {
		t.Errorf("established notice without TLS: %s", hub.log.String())
	}
	login(t, leaf.s, "bob")
	login(t, leaf.s, "ben")
	hub.seen(t, "bob")
	hub.seen(t, "ben")

	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.pingSent = time.Now().Add(-linkPingWarn - time.Second)
			hub.s.linkTick(time.Now())
			hub.s.linkCommand(l, mustParse(t, ":"+leaf.sid+" PONG leaf.test.local :hub.test.local"))
			hub.s.dropLink(l, "test split")
		}
	})
	for _, want := range []string{
		"Link with leaf.test.local hasn't answered a PING for 31 seconds: it is lagging (dropped after 90)",
		"Link with leaf.test.local answers again (lag ",
		"test split, lost 2 users",
	} {
		if !hub.logHas(want) {
			t.Errorf("no %q in:\n%s", want, hub.log.String())
		}
	}
	if !hub.logHas("Link with leaf.test.local: sync done in ") || !hub.logHas(": 0 users on 1 server") {
		t.Errorf("sync notice without the burst size")
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
