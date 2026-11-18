package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// fromLinkTo runs one raw server line on n as if the link to name sent it.
func (n *linkNode) fromLinkTo(t *testing.T, name, line string) {
	t.Helper()
	m, err := irc.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	n.s.onLoop(func() {
		for l := range n.s.links.conns {
			if l.peer.Name == name {
				n.s.linkCommand(l, m)
				return
			}
		}
		t.Error("no link to " + name)
	})
}

// UnrealIRCd names the sender of a channel message by nick. Sent on as it
// came, InspIRCd dropped it (it takes only a UID): Unreal users were never
// heard by Insp users (QA link test, 1.2.0). The hub now passes the UID on.
func TestChannelMessageNickSourceGoesOnAsUID(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	a := newLinkNode(t, "a.test.local", "0AA", "leaf")
	b := newLinkNode(t, "b.test.local", "0BB", "leaf")
	hub.expects(a, false, "")
	hub.expects(b, false, "")
	a.expects(hub, true, "")
	b.expects(hub, true, "")
	hub.start(t)
	a.start(t)
	b.start(t)
	waitLink(t, "tree", func() bool { return len(a.servers()) == 3 && len(b.servers()) == 3 })
	ann := login(t, a.s, "ann")
	ben := login(t, b.s, "ben")
	ann.send("JOIN #t")
	ann.expect(" 366 ")
	ben.send("JOIN #t")
	ben.expect(" 366 ")
	waitLink(t, "both on #t at the hub", func() bool {
		var n int
		hub.s.onLoop(func() {
			if ch := hub.s.net.Channel("#t"); ch != nil {
				n = len(ch.Members)
			}
		})
		return n == 2
	})
	uid := hub.user("ann").UID
	hub.s.onLoop(func() { hub.s.links.debug = true })

	hub.fromLinkTo(t, "a.test.local", ":ann PRIVMSG #t :by nick")
	ben.expect(":ann!~ann@127.0.0.1 PRIVMSG #t :by nick")
	waitLink(t, "the hub sends it on by UID", func() bool {
		return hub.logHas(":" + uid + " PRIVMSG #t :by nick")
	})
	if hub.logHas(":ann PRIVMSG #t") {
		t.Error("the hub sent the message on with a nick source")
	}
}

// CHGHOST, CHGIDENT and CHGNAME on someone on another server gave 401 (QA
// link test, 1.2.0); the change now goes to their server.
func TestChgOnRemoteUser(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")

	op.send("CHGHOST bob new.example")
	waitLink(t, "host on both", func() bool {
		return hub.user("bob").Host == "new.example" && leaf.user("bob").Host == "new.example"
	})
	op.send("CHGIDENT bob newid")
	waitLink(t, "ident on both", func() bool {
		return hub.user("bob").Ident == "newid" && leaf.user("bob").Ident == "newid"
	})
	op.send("CHGNAME bob :A New Name")
	waitLink(t, "realname on both", func() bool {
		return hub.user("bob").Realname == "A New Name" && leaf.user("bob").Realname == "A New Name"
	})
	op.send("CHGHOST nobody x.example")
	if l := op.expect(" 401 "); !strings.Contains(l, "nobody") {
		t.Errorf("got %q", l)
	}
}
