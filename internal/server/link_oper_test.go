package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
)

// A leaf has no accounts (its services are off), so /oper
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
		err = n.s.store.SaveOper(&store.Oper{AccountID: a.ID, Account: a.Name, Flags: flags, Title: "Test oper", Swhois: []string{"looks very helpful"},
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
	// WHOIS shows the title and the extra lines from either server.
	for who, n := range map[string]*linkNode{"hugo": hub, "lena": leaf} {
		c := login(t, n.s, who)
		if n == hub {
			waitLink(t, "the hub knows opal's title", func() bool { u := hub.user("opal"); return u != nil && u.OperTitle != "" })
		}
		c.send("WHOIS opal")
		c.expect(" 313 " + who + " opal :is an IRC Operator (Test oper)")
		c.expect(" 320 " + who + " opal :looks very helpful")
		c.expect(" 318 " + who + " opal ")
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

// A change right after /oper, before the leaf's +o reached the hub, still
// follows the oper (it was dropped, and later changes with it).
func TestLeafOperChangeBeforeModeArrives(t *testing.T) {
	hub, leaf := linkedPair(t)
	opal := login(t, leaf.s, "opal")
	opal.send("NS REGISTER " + pw)
	opal.expect("is now registered to you.")
	operEntryOn(t, hub, "opal", "gk", []string{"*@*"})
	opal.send("OPER opal " + operPW)
	opal.expect(" 381 opal ")
	waitLink(t, "hub sees opal as oper", func() bool { u := hub.user("opal"); return u != nil && u.Modes['o'] })
	hub.s.onLoop(func() {
		u := hub.s.net.UserByNick("opal")
		u.Modes['o'] = false // as if the +o were still on its way
		defer func() { u.Modes['o'] = true }()
		a, _ := hub.s.store.AccountByNick("opal")
		o, _ := hub.s.store.OperByAccount(a.ID)
		o.Flags = "g"
		if err := hub.s.store.SaveOper(o); err != nil {
			t.Error(err)
		}
		hub.s.recheckOpers(a.ID)
	})
	opal.expect("Your oper flags are now g")
}

// An oper entry changed on the services server follows the oper to
// the leaf at once: new flags, then disabled = no longer an oper there.
func TestLeafOperLiveChanges(t *testing.T) {
	hub, leaf := linkedPair(t)
	opal := login(t, leaf.s, "opal")
	opal.send("NS REGISTER " + pw)
	opal.expect("is now registered to you.")
	operEntryOn(t, hub, "opal", "gk", []string{"*@*"})
	opal.send("OPER opal " + operPW)
	opal.expect(" 381 opal ")

	change := func(edit func(o *store.Oper)) {
		hub.s.onLoop(func() {
			a, _ := hub.s.store.AccountByNick("opal")
			o, _ := hub.s.store.OperByAccount(a.ID)
			edit(o)
			if err := hub.s.store.SaveOper(o); err != nil {
				t.Error(err)
			}
			hub.s.recheckOpers(a.ID)
		})
	}
	change(func(o *store.Oper) { o.Flags = "g" })
	opal.expect("Your oper flags are now g")
	var flags string
	leaf.s.onLoop(func() { flags = leaf.s.clients[leaf.s.net.UserByNick("opal").UID].operFlags })
	if flags != "g" {
		t.Errorf("flags on the leaf %q", flags)
	}

	change(func(o *store.Oper) { o.Disabled = "on holiday" })
	opal.expect("Your oper entry was disabled (on holiday), so you are no longer an operator.")
	waitLink(t, "hub sees opal de-opered", func() bool { u := hub.user("opal"); return u != nil && !u.Modes['o'] })

	// A forged change from a server that didn't oper opal is ignored.
	kim := login(t, leaf.s, "kim")
	netadminOn(t, leaf, kim, "kim")
	var kimUID string
	leaf.s.onLoop(func() { kimUID = leaf.s.net.UserByNick("kim").UID })
	hub.s.onLoop(func() { hub.s.operAnswer(kimUID, "DEOPER :forged") })
	kim.quiet("forged")
}

// A certificate oper on a leaf: the leaf sends the user's fingerprint
// with the /oper, and the services server checks it.
func TestLeafOperByCertificate(t *testing.T) {
	hub, leaf := linkedPair(t)
	reg := login(t, hub.s, "gardener")
	reg.send("NS REGISTER " + pw)
	reg.expect("is now registered to you.")
	operEntryOn(t, hub, "gardener", "g", []string{"*@*"})
	cert, fp := clientCert(t)
	other, _ := clientCert(t)
	hub.s.onLoop(func() {
		a, _ := hub.s.store.AccountByNick("gardener")
		o, _ := hub.s.store.OperByAccount(a.ID)
		o.CertFPs, o.PassHash = []string{fp}, ""
		hub.s.store.SaveOper(o)
	})

	w := certLogin(t, leaf.s, "wrongcert", &other)
	hub.seen(t, "wrongcert")
	w.send("OPER gardener x")
	w.expect(" 491 wrongcert ")
	c := certLogin(t, leaf.s, "gardenbot", &cert)
	hub.seen(t, "gardenbot")
	c.send("OPER gardener x")
	c.expect(" 381 gardenbot :You are now an IRC operator")
}

// What another server says about an oper's looks is kept short and clean.
func TestOperLook(t *testing.T) {
	if got := operLook("", nil, nil); got != "" {
		t.Errorf("nothing to say gives %q", got)
	}
	title, lines, _ := readOperLook(operLook("is the Founder", []string{"looks very helpful", "second"}, nil))
	if title != "is the Founder" || len(lines) != 2 || lines[1] != "second" {
		t.Errorf("round trip: %q %q", title, lines)
	}
	long := make([]string, 20)
	for i := range long {
		long[i] = strings.Repeat("x", 60)
	}
	if got := operLook("t", long, nil); len(got) > 380 {
		t.Errorf("too long for a link line: %d", len(got))
	}
	title, lines, _ = readOperLook(`{"t":"bad\r\ntitle","s":["a","b","c","d","e","f","\u0001"]}`)
	if title != "badtitle" || len(lines) != 5 {
		t.Errorf("unclean: %q %q", title, lines)
	}
	if title, lines, _ = readOperLook("not json"); title != "" || lines != nil {
		t.Errorf("junk: %q %q", title, lines)
	}
}

// The admin from beeircd.conf opers on a leaf with the leaf's own password,
// and still shows the title and WHOIS lines of their own entry on the
// services server, from either server.
func TestLeafAdminKeepsOwnLook(t *testing.T) {
	hub, leaf := linkedPair(t)
	h, _ := passwd.Hash(operPW)
	leaf.s.onLoop(func() { leaf.s.cfg.Services.Admin, leaf.s.cfg.Services.AdminPassword = "Queen", h })
	q := login(t, leaf.s, "Queen")
	q.send("NS REGISTER " + pw)
	q.expect("is now registered to you.")
	waitLink(t, "account on the leaf", func() bool { u := leaf.user("Queen"); return u != nil && u.Account == "Queen" })
	operEntryOn(t, hub, "Queen", "gk", []string{"*@*"})

	q.send("OPER Queen " + operPW)
	q.expect(" 381 Queen :You are now an IRC operator")
	waitLink(t, "the hub knows Queen's lines", func() bool { u := hub.user("Queen"); return u != nil && len(u.OperSwhois) > 0 })
	var flags string
	leaf.s.onLoop(func() { flags = leaf.s.clients[leaf.s.net.UserByNick("Queen").UID].operFlags })
	if flags != allFlags {
		t.Errorf("the leaf admin's flags became %q, want %q", flags, allFlags)
	}
	for who, n := range map[string]*linkNode{"hugo": hub, "lena": leaf} {
		c := login(t, n.s, who)
		c.send("WHOIS Queen")
		c.expect(" 313 " + who + " Queen :is an IRC Operator (Test oper)")
		c.expect(" 320 " + who + " Queen :looks very helpful")
		c.expect(" 318 " + who + " Queen ")
	}
}

// The oper_tag of the oper's own server shows from every server, and a
// change on rehash reaches the others.
func TestOperTagCrossesLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	hub.s.onLoop(func() { hub.s.cfg.Server.OperTag = "(IRCop)" })
	leaf.s.onLoop(func() { leaf.s.cfg.Server.OperTag = "(LeafTag)" })
	h, _ := passwd.Hash(operPW)
	hub.s.onLoop(func() { hub.s.cfg.Services.Admin, hub.s.cfg.Services.AdminPassword = "Kumbang", h })
	k := login(t, hub.s, "Kumbang")
	k.send("NS REGISTER " + pw)
	k.expect("is now registered to you.")
	k.send("OPER Kumbang " + operPW)
	k.expect(" 381 Kumbang :You are now an IRC operator")
	waitLink(t, "the leaf knows the hub's tag", func() bool {
		u := leaf.user("Kumbang")
		return u != nil && u.OperTag != nil && *u.OperTag == "(IRCop)"
	})
	for who, n := range map[string]*linkNode{"hugo": hub, "lena": leaf} {
		c := login(t, n.s, who)
		c.send("WHOIS Kumbang")
		c.expect(" 313 " + who + " Kumbang :is a Network Administrator (IRCop)")
	}

	// oper_tag changes on the hub: a rehash sends it again.
	hub.s.onLoop(func() { hub.s.cfg.Server.OperTag = ""; hub.s.resendOperLooks() })
	waitLink(t, "the leaf hears the tag is gone", func() bool {
		u := leaf.user("Kumbang")
		return u != nil && u.OperTag != nil && *u.OperTag == ""
	})
	c := login(t, leaf.s, "lina")
	c.send("WHOIS Kumbang")
	c.expect(" 313 lina Kumbang :is a Network Administrator\r")
}
