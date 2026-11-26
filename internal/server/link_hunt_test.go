package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// A server that runs services is the one SASL server of its network: a
// linked server's "MD client <name> saslmechlist" must not make that
// server the services server here, or it gets the services' rights
// (SVSLOGIN, SVSKILL, SVSNICK...) over our own users.
func TestLinkSaslMechlistCantClaimServices(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, hub.s, "alice")
	a := hub.user("alice").UID
	hub.fromLink(t, ":"+leaf.sid+" MD client "+leaf.name+" saslmechlist :PLAIN")
	hub.fromLink(t, ":"+leaf.sid+" SVSLOGIN * "+a+" Boss")
	hub.fromLink(t, ":"+leaf.sid+" SVSNICK "+a+" spoofnick 1700000000")
	time.Sleep(100 * time.Millisecond)
	u := hub.user("alice")
	if u == nil {
		t.Fatal("alice was renamed by the leaf")
	}
	if u.Account != "" {
		t.Fatalf("the leaf logged alice in as %q", u.Account)
	}
	var target string
	hub.s.onLoop(func() { target = hub.s.links.saslServer })
	if target != "" {
		t.Fatalf("the leaf became the SASL server: %q", target)
	}
}

// Our services bots can't be killed from here (481); a KILL for one from
// a linked server (an UnrealIRCd oper whose server doesn't U-line us)
// must not take it off the network. The killing side dropped the bot
// already, so it gets it back, in its channels.
func TestLinkKillOfOurServiceBot(t *testing.T) {
	hub, leaf := linkedPair(t)
	leaf.seen(t, "ChanServ")
	cs := hub.user("ChanServ").UID
	alice := login(t, hub.s, "alice")
	alice.send("JOIN #bots")
	alice.expect(" 366 alice #bots ")
	hub.s.onLoop(func() {
		hub.s.emit(&state.Join{UID: cs, Channel: "#bots", TS: hub.s.net.Channel("#bots").TS})
	})
	waitLink(t, "ChanServ in #bots on the leaf", func() bool { return inChan(leaf, "ChanServ", "#bots") })
	// The leaf drops ChanServ as a killing server does, then the KILL
	// reaches the hub.
	leaf.s.onLoop(func() { leaf.s.emitQuiet(&state.UserQuit{UID: cs, Reason: "Killed (bye)"}) })
	hub.fromLink(t, ":"+leaf.sid+" KILL "+cs+" :oper (bye services)")
	time.Sleep(100 * time.Millisecond)
	if hub.user("ChanServ") == nil {
		t.Fatal("a KILL from the leaf took ChanServ off the hub")
	}
	waitLink(t, "ChanServ back on the leaf, in #bots", func() bool { return inChan(leaf, "ChanServ", "#bots") })
}

// inChan reports whether nick is in channel name on n.
func inChan(n *linkNode, nick, name string) bool {
	in := false
	n.s.onLoop(func() {
		u, ch := n.s.net.UserByNick(nick), n.s.net.Channel(name)
		in = u != nil && ch != nil && ch.Member(u) != nil
	})
	return in
}
