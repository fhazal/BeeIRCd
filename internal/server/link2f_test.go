package server

import (
	"errors"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
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

// operEntryOn gives account nick an oper entry on n (the services server).
func operEntryOn(t *testing.T, n *linkNode, nick, flags string, hosts []string) {
	t.Helper()
	h, _ := passwd.Hash(operPW)
	var err error
	n.s.onLoop(func() {
		a, _ := n.s.store.AccountByNick(nick)
		if a == nil {
			err = errNoAccount
			return
		}
		err = n.s.store.SaveOper(&store.Oper{AccountID: a.ID, Account: a.Name, Flags: flags, Title: "Test oper",
			Hosts: hosts, PassHash: h, AddedBy: "test", AddedAt: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
}

var errNoAccount = errors.New("no such account")

// Opers other than the admin: /oper on a leaf goes to the services server,
// which checks the entry, host and password and sends back the flags.
func TestLeafOperRelay(t *testing.T) {
	hub, leaf := linkedPair(t)
	opal := login(t, leaf.s, "opal")
	opal.send("NS REGISTER " + pw)
	opal.expect("is now registered to you.")
	pearl := login(t, leaf.s, "pearl")
	pearl.send("NS REGISTER " + pw)
	pearl.expect("is now registered to you.")
	operEntryOn(t, hub, "opal", "gk", []string{"*@*"})
	operEntryOn(t, hub, "pearl", "gk", []string{"*@nowhere.example"})

	opal.send("OPER opal wrong-password")
	opal.expect(" 464 opal :Password incorrect")
	opal.send("OPER opal " + operPW)
	opal.expect(" 381 opal :You are now an IRC operator")
	opal.expect("Oper flags: gk")
	waitLink(t, "hub sees opal as oper", func() bool { u := hub.user("opal"); return u != nil && u.Modes['o'] })
	var flags string
	leaf.s.onLoop(func() { flags = leaf.s.clients[leaf.s.net.UserByNick("opal").UID].operFlags })
	if flags != "gk" {
		t.Errorf("flags on the leaf %q", flags)
	}

	pearl.send("OPER pearl " + operPW) // entry, but not from this host
	pearl.expect(" 491 pearl :No oper entry for you from this host")
	pearl.send("OPER nosuch " + operPW)
	pearl.expect(" 491 pearl :No oper entry")
	if !hub.logHas("Failed /oper as pearl from pearl") {
		t.Error("the services server did not log the failed /oper")
	}
	if leaf.logHas(operPW) || hub.logHas(operPW) {
		t.Error("an oper password reached a log")
	}
}

// With no services server linked, /oper on a leaf says so at once.
func TestLeafOperNoServices(t *testing.T) {
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	leaf.start(t)
	c := login(t, leaf.s, "lone")
	c.send("OPER lone " + operPW)
	c.expect(" 491 lone :No oper entry for you from this host (the services server isn't linked right now)")
}

func TestDebugLineHidesOperPassword(t *testing.T) {
	if got := debugLine(":0LFAAAAAB BEEOPER 9HB opal :sekrit pass"); got != ":0LFAAAAAB BEEOPER 9HB opal :<password>" {
		t.Errorf("%q", got)
	}
	if got := debugLine(":0HB PRIVMSG #x :hi"); got != ":0HB PRIVMSG #x :hi" {
		t.Errorf("%q", got)
	}
}
