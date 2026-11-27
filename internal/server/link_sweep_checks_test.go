package server

import (
	"bufio"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// Checks from the 1.7.2 code sweep that found nothing wrong; kept so it
// stays that way.

// The clash-rename exception (SVSNICK uid uid) lets any server rename a
// user to their own UID (InspIRCd's SAVE does the same), but nothing
// else: not to another user's UID, not to a nick.
func TestLinkClashRenameOnlyToOwnUID(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, hub.s, "alice")
	login(t, hub.s, "carol")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	a, c, b := hub.user("alice").UID, hub.user("carol").UID, hub.user("bob").UID
	for _, line := range []string{
		":" + leaf.sid + " SVSNICK " + a + " " + c + " 0", // carol's UID
		":" + leaf.sid + " SVSNICK " + a + " " + b + " 0", // bob's UID
		":" + leaf.sid + " SVSNICK alice " + a + " 0",     // by nick, to a UID
		":" + leaf.sid + " SVSNICK " + a + " spoofnick 0",
		":" + leaf.sid + " SVSNICK alice alice 0",
		":" + b + " SVSNICK " + c + " " + a + " 0",
	} {
		hub.fromLink(t, line)
	}
	time.Sleep(100 * time.Millisecond)
	if hub.user("alice") == nil || hub.user("carol") == nil {
		t.Fatalf("a rename got through: alice there %v, carol there %v", hub.user("alice") != nil, hub.user("carol") != nil)
	}
	hub.fromLink(t, ":"+leaf.sid+" SVSNICK "+a+" "+a+" 0")
	waitLink(t, "alice renamed to her UID", func() bool { return hub.user(a) != nil })
	if hub.user("carol") == nil {
		t.Fatal("carol changed too")
	}
}

// PROXY headers: only from proxy_from addresses, a port without proxy
// never reads one, and v1/v2 over IPv6 give the right address.
func TestProxyTrustChecks(t *testing.T) {
	addr := func(s string) net.Addr { a, _ := net.ResolveTCPAddr("tcp", s); return a }
	port := func(from ...string) listener {
		return newListener(config.Listen{Address: "127.0.0.1:0", Proxy: true, ProxyFrom: from}, nil, nil)
	}
	if port().proxyAllowed(addr("127.0.0.1:5000")) {
		t.Error("an empty proxy_from trusts 127.0.0.1")
	}
	v4, v6 := port("127.0.0.1"), port("2001:db8::/64")
	for _, c := range []struct {
		l    listener
		addr string
		want bool
	}{
		{v4, "127.0.0.1:5000", true},
		{v4, "[::ffff:127.0.0.1]:5000", true}, // the same address, v4-mapped
		{v4, "127.0.0.2:5000", false},
		{v4, "[::1]:5000", false},
		{v6, "[2001:db8::9]:5000", true},
		{v6, "[2001:db9::9]:5000", false},
		{v6, "127.0.0.1:5000", false},
	} {
		if got := c.l.proxyAllowed(addr(c.addr)); got != c.want {
			t.Errorf("proxy_from %v, %s: allowed %v, want %v", c.l.conf.ProxyFrom, c.addr, got, c.want)
		}
	}

	real := addr("127.0.0.1:1")
	read := func(h []byte) (string, error) {
		a, err := readProxyHeader(bufio.NewReader(strings.NewReader(string(h)+"NICK x\r\n")), real)
		if err != nil {
			return "", err
		}
		return a.String(), nil
	}
	if got, err := read([]byte("PROXY TCP6 2001:db8::1 ::1 5555 6667\r\n")); err != nil || got != "[2001:db8::1]:5555" {
		t.Errorf("v1 TCP6: %q %v", got, err)
	}
	for _, bad := range []string{
		"PROXY TCP6 192.0.2.1 ::1 5555 6667\r\n",   // TCP6 with an IPv4 address
		"PROXY TCP4 2001:db8::1 ::1 5555 6667\r\n", // and the other way
		"PROXY TCP4 192.0.2.1 127.0.0.1 99999 6667\r\n",
		"PROXY TCP4 192.0.2.1 127.0.0.1 5555 6667\n", // no \r
		"PROXY TCP4 192.0.2.1 127.0.0.1 5555 6667 " + strings.Repeat("x", 120) + "\r\n",
	} {
		if got, err := read([]byte(bad)); err == nil {
			t.Errorf("%q accepted as %s", bad, got)
		}
	}
	v2 := append([]byte(nil), proxyV2Sig...)
	v2 = append(v2, 0x21, 0x21, 0, 36)
	v2 = append(v2, net.ParseIP("2001:db8::7")...)
	v2 = append(v2, net.ParseIP("::1")...)
	v2 = binary.BigEndian.AppendUint16(v2, 4444)
	v2 = binary.BigEndian.AppendUint16(v2, 6667)
	if got, err := read(v2); err != nil || got != "[2001:db8::7]:4444" {
		t.Errorf("v2 TCP6: %q %v", got, err)
	}
	short := append(append([]byte(nil), proxyV2Sig...), 0x21, 0x21, 0, 12)
	short = append(short, make([]byte, 12)...)
	if got, err := read(short); err == nil {
		t.Errorf("v2 TCP6 with 12 address bytes accepted as %s", got)
	}

	// A plain port takes the PROXY line as junk: the address stays.
	s := proxyServer(t, "127.0.0.0/8")
	c := raw(t, s)
	c.nick = "plainp"
	c.send("PROXY TCP4 203.0.113.9 127.0.0.1 5555 6667")
	c.send("NICK plainp")
	c.send("USER plainp 0 * :plainp")
	c.expect(" 001 plainp ")
	c.send("WHOIS plainp")
	if l := c.expect(" 311 plainp plainp "); strings.Contains(l, "203.0.113.9") {
		t.Errorf("a plain port took the PROXY address: %s", l)
	}
}

// Nick bans (Q-lines) from the hub's services over a split and relink: on
// the leaf they go with the link, come back once each, and one removed
// during the split stays removed; one added during it arrives.
func TestLinkQlinesOverSplit(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	ann := login(t, leaf.s, "ann")
	hub.seen(t, "ann")
	is := func(want ...string) func() bool {
		return func() bool {
			ok := true
			leaf.s.onLoop(func() {
				ok = len(leaf.s.links.qlines) == len(want)
				for _, w := range want {
					q, found := leaf.s.links.qlines[w]
					ok = ok && found && q.mask == w
				}
			})
			return ok
		}
	}
	op.send("OS SQLINE ADD keep*nick kept")
	op.expect(":OperServ!")
	op.send("OS SQLINE ADD gone*nick removed in the split")
	op.expect(":OperServ!")
	waitLink(t, "two Q-lines on the leaf", is("keep*nick", "gone*nick"))

	op.send("SQUIT leaf.test.local testing")
	waitLink(t, "split", func() bool { return hub.user("ann") == nil })
	waitLink(t, "the leaf dropped them", is())
	op.send("OS SQLINE DEL gone*nick")
	op.expect(":OperServ!")
	op.send("OS SQLINE ADD new*nick set in the split")
	op.expect(":OperServ!")

	leaf.s.onLoop(func() {
		for _, r := range leaf.s.links.retry {
			r.next = time.Time{}
		}
	})
	waitLink(t, "relink", func() bool { return hub.user("ann") != nil })
	waitLink(t, "the Q-lines as they are now", is("keep*nick", "new*nick"))
	time.Sleep(100 * time.Millisecond)
	if !is("keep*nick", "new*nick")() {
		t.Fatal("Q-lines changed after the relink settled")
	}
	ann.send("NICK keepXnick")
	ann.expect(" 432 ")
	ann.send("NICK goneXnick")
	ann.expect(":ann!")
}

// The services link drops while a leaf client is halfway through SASL:
// its next line fails at once (904) and it can try again.
func TestLinkSaslClientOverSplit(t *testing.T) {
	_, leaf := linkedPair(t)
	c := raw(t, leaf.s)
	c.nick = "*"
	c.send("CAP REQ :sasl")
	c.send("NICK dan2")
	c.send("USER dan 0 * :Dan")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	leaf.s.onLoop(func() {
		for l := range leaf.s.links.conns {
			leaf.s.dropLink(l, "test split")
		}
	})
	c.send("AUTHENTICATE " + saslPlain("dan", pw))
	c.expect(" 904 ")
	c.send("AUTHENTICATE PLAIN")
	if l := c.expect(" 90"); !strings.Contains(l, " 904 ") {
		t.Errorf("retry got %q", l)
	}
	c.send("CAP END")
	c.expect(" 001 dan2 ")
}
