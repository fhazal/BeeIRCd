package server

import (
	"testing"
	"time"
)

// Checks from the 1.7.2 code sweep: who counts as the services server,
// clash renames, Q-lines and SASL over a split.

// bobKept fails when bob on the leaf was killed or logged in.
func bobKept(t *testing.T, leaf *linkNode, what string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	u := leaf.user("bob")
	if u == nil {
		t.Fatalf("%s: bob was killed", what)
	}
	if u.Account != "" {
		t.Fatalf("%s: bob was logged in as %q", what, u.Account)
	}
}

// A server behind the hub says it takes SASL while the real services
// server is linked: the leaf must keep the services server, and the
// newcomer gets no services rights over the leaf's users.
func TestLinkSaslMechlistNoTakeover(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, leaf.s, "bob")
	b := leaf.user("bob").UID
	leaf.fromLink(t, ":"+hub.sid+" SID rogue.test.local 2 0RG :rogue")
	leaf.fromLink(t, ":0RG MD client rogue.test.local saslmechlist :PLAIN")
	leaf.fromLink(t, ":0RG SVSLOGIN * "+b+" Boss")
	leaf.fromLink(t, ":0RG SVSKILL "+b+" :spoof")
	bobKept(t, leaf, "a second SASL server")
	var target string
	leaf.s.onLoop(func() { target = leaf.s.links.saslServer })
	if target != "services.test.local" {
		t.Fatalf("SASL server is now %q", target)
	}
}

// The services server splits and another server comes in under its name:
// the leaf must not take it for the services server.
func TestLinkServicesNameReusedAfterSplit(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, leaf.s, "bob")
	b := leaf.user("bob").UID
	leaf.fromLink(t, ":"+hub.sid+" SQUIT services.test.local :gone")
	waitLink(t, "services gone on the leaf", func() bool {
		var gone bool
		leaf.s.onLoop(func() { gone = leaf.s.serverByName("services.test.local") == nil })
		return gone
	})
	leaf.fromLink(t, ":"+hub.sid+" SID services.test.local 2 0FK :fake services")
	leaf.fromLink(t, ":0FK SVSLOGIN * "+b+" Boss")
	leaf.fromLink(t, ":0FK SVSKILL "+b+" :spoof")
	bobKept(t, leaf, "a server reusing the services name")
}

// The services server splits and comes back the same way, without a new
// saslmechlist (InspIRCd sends it only on the first link): the leaf must
// take it as the SASL server again, so logins work at once.
func TestLinkSaslServerBackAfterRelink(t *testing.T) {
	hub, leaf := linkedPair(t)
	target := func() (t string) { leaf.s.onLoop(func() { t = leaf.s.saslTarget() }); return t }
	if got := target(); got != "services.test.local" {
		t.Fatalf("SASL server before the split is %q", got)
	}
	leaf.fromLink(t, ":"+hub.sid+" SQUIT services.test.local :gone")
	waitLink(t, "services gone on the leaf", func() bool { return target() == "" })
	leaf.fromLink(t, ":"+hub.sid+" SID services.test.local 2 9HB :services back")
	waitLink(t, "services back as the SASL server", func() bool { return target() == "services.test.local" })
}

// The link to a server whose clients are halfway through SASL with our
// services drops: their sessions must go with it, not stay forever.
func TestLinkSaslSessionsGoWithTheLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, uid := range []string{"0LFAAAAAX", "0LFAAAAAY"} {
		hub.fromLink(t, ":"+leaf.sid+" SASL services.test.local "+uid+" H host.test 192.0.2.1 P")
	}
	hub.fromLink(t, ":"+leaf.sid+" SASL services.test.local 0LFAAAAAY S PLAIN")
	n := func() (n int) { hub.s.onLoop(func() { n = len(hub.s.links.sasl) }); return n }
	if got := n(); got != 2 {
		t.Fatalf("%d sessions before the split, want 2", got)
	}
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			hub.s.dropLink(l, "test split")
		}
	})
	if got := n(); got != 0 {
		t.Fatalf("%d SASL sessions left after the link dropped", got)
	}
}
