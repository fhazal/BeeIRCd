package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/passwd"
)

// Step 2f bug: a leaf has no accounts (its services are off), so /oper
// found no entry for anyone, the admin included. The admin from the
// leaf's beeircd.conf now opers with its admin_password once logged in
// (on the hub) to the account of that name; nobody else gets in that way.
func TestLeafAdminOper(t *testing.T) {
	hub, leaf := linkedPair(t)
	h, _ := passwd.Hash(operPW)
	leaf.s.onLoop(func() { leaf.s.cfg.Services.Admin, leaf.s.cfg.Services.AdminPassword = "Queen", h })

	// Not logged in: no entry, even with the right password.
	q := login(t, leaf.s, "Queen")
	hub.seen(t, "Queen")
	q.send("OPER Queen " + operPW)
	q.expect(" 491 ")

	q.send("NS REGISTER " + pw)
	q.expect("is now registered to you.")
	waitLink(t, "account on the leaf", func() bool { u := leaf.user("Queen"); return u != nil && u.Account == "Queen" })
	q.send("OPER Queen wrong-password")
	q.expect(" 464 ")
	q.send("OPER Queen " + operPW)
	q.expect(" 381 Queen :You are now an IRC operator")
	waitLink(t, "hub sees the oper", func() bool { u := hub.user("Queen"); return u != nil && u.Modes['o'] })

	// Another account, the admin's name and password: refused.
	m := login(t, leaf.s, "mallory")
	m.send("NS REGISTER " + pw)
	m.expect("is now registered to you.")
	waitLink(t, "mallory's account", func() bool { u := leaf.user("mallory"); return u != nil && u.Account == "mallory" })
	m.send("OPER Queen " + operPW)
	m.expect(" 491 ")
}
