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

// hide_killed_by covers SVSKILL too: a kill-shaped reason shows the hidden
// name, to the victim and to the other side; other reasons stay.
func TestLinkSvsKillHideKilledBy(t *testing.T) {
	hub, leaf := linkedPair(t)
	leaf.s.onLoop(func() { leaf.s.cfg.Messages.HideKilledBy = "OperServ" })
	bob := login(t, leaf.s, "bob")
	carl := login(t, leaf.s, "carl")
	watch := login(t, leaf.s, "watch")
	for _, c := range []*tc{bob, carl, watch} {
		c.send("JOIN #k")
		c.send("PING :j")
		c.expect(" :j")
	}
	hub.seen(t, "carl")
	leaf.fromLink(t, ":"+hub.sid+" SVSKILL "+leaf.user("bob").UID+" :Killed (NickServ (GHOST command used by bob2))")
	if e := bob.expect("ERROR"); !strings.Contains(e, "Killed (OperServ (GHOST command used by bob2))") {
		t.Errorf("bob got %q", e)
	}
	if q := watch.expect(" QUIT "); !strings.Contains(q, "Killed (OperServ (GHOST") {
		t.Errorf("watch saw %q", q)
	}
	leaf.fromLink(t, ":"+hub.sid+" SVSKILL "+leaf.user("carl").UID+" :Session limit exceeded")
	if q := watch.expect(" QUIT "); !strings.Contains(q, ":Session limit exceeded") {
		t.Errorf("watch saw %q", q)
	}
	for in, want := range map[string]string{
		"Killed (a b (x))":     "Killed (a b (x))",
		"Killed (NickServ)":    "Killed (NickServ)",
		"Killed ( (x))":        "Killed ( (x))",
		"Killed (Svc (a (b)))": "Killed (OperServ (a (b)))",
	} {
		var got string
		leaf.s.onLoop(func() { got = leaf.s.hideKillerIn(in) })
		if got != want {
			t.Errorf("hideKillerIn(%q) = %q, want %q", in, got, want)
		}
	}
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
