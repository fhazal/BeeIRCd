package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// fromLink runs one raw server line on n as if its first link sent it.
func (n *linkNode) fromLink(t *testing.T, line string) {
	t.Helper()
	m, err := irc.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	n.s.onLoop(func() {
		for l := range n.s.links.conns {
			n.s.linkCommand(l, m)
			return
		}
		t.Error("no link")
	})
}

// SVSKILL (Anope on UnrealIRCd) removes a user with the reason as the
// quit message, for our own users and for remote ones. Before, it was
// skipped and the user stayed as a ghost (prod link test 11-12).
func TestLinkSvsKill(t *testing.T) {
	hub, leaf := linkedPair(t)
	bob := login(t, leaf.s, "bob")
	alice := login(t, hub.s, "alice")
	hub.seen(t, "bob")
	leaf.seen(t, "alice")
	b := leaf.user("bob")
	leaf.fromLink(t, ":"+hub.sid+" SVSKILL "+b.UID+" :Session limit exceeded")
	if e := bob.expect("ERROR"); !strings.Contains(e, "Session limit exceeded") {
		t.Errorf("bob got %q", e)
	}
	waitLink(t, "bob gone on the leaf", func() bool { return leaf.user("bob") == nil })
	leaf.fromLink(t, ":"+hub.sid+" SVSKILL alice :bye")
	waitLink(t, "alice gone on the leaf", func() bool { return leaf.user("alice") == nil })
	_ = alice
}

// A server introducing a nick it already holds means we missed how the
// old one left: drop the ghost, never kill or rename the real user.
func TestLinkStaleNickDropped(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, hub.s, "carol")
	leaf.seen(t, "carol")
	old := leaf.user("carol")
	leaf.fromLink(t, ":"+hub.sid+" UID carol 0 1794444176 carol host.test "+hub.sid+"ZZZZZZ 0 +i * cloak.test fwAAAQ== :again")
	waitLink(t, "new carol", func() bool {
		u := leaf.user("carol")
		return u != nil && u.UID == hub.sid+"ZZZZZZ"
	})
	if leaf.user(old.UID) != nil || leaf.logHas("Nick collision on carol") {
		t.Errorf("ghost kept or collision run:\n%s", leaf.log)
	}
}
