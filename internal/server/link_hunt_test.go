package server

import (
	"testing"
	"time"
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
