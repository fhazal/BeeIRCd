package server

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

func saslPlain(account, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(account + "\x00" + account + "\x00" + password))
}

// SASL on a leaf (services off) goes to the hub's services and back.
func TestLinkSASL(t *testing.T) {
	hub, leaf := linkedPair(t)
	dan := login(t, leaf.s, "dan")
	hub.seen(t, "dan")
	dan.send("NS REGISTER " + pw)
	dan.expect("is now registered to you.")
	dan.send("QUIT")
	dan.expect("ERROR")
	waitLink(t, "dan gone", func() bool { return hub.user("dan") == nil })

	c := raw(t, leaf.s)
	c.nick = "*"
	c.send("CAP REQ :sasl")
	c.send("NICK dan2")
	c.send("USER dan 0 * :Dan")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + saslPlain("dan", "wrong-password"))
	c.expect(" 904 ")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + saslPlain("dan", pw))
	c.expect(" 900 ")
	c.expect(" 903 ")
	c.send("CAP END")
	c.expect(" 001 dan2 ")
	waitLink(t, "account on the hub", func() bool { u := hub.user("dan2"); return u != nil && u.Account == "dan" })
}

// With no services server linked, SASL fails at once with a reason.
func TestLinkSASLNoServices(t *testing.T) {
	n := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	n.start(t)
	c := raw(t, n.s)
	c.nick = "*"
	c.send("CAP REQ :sasl")
	c.send("AUTHENTICATE PLAIN")
	if l := c.expect(" 904 "); !strings.Contains(l, "services are unavailable") {
		t.Errorf("got %q", l)
	}
}

// Services on the hub join and part someone on the leaf (AJOIN, Anope's
// SVSJOIN/SVSPART); the leaf does it and everyone sees it.
func TestLinkSvsJoinPart(t *testing.T) {
	hub, leaf := linkedPair(t)
	eve := login(t, leaf.s, "eve")
	hub.seen(t, "eve")
	uid := hub.user("eve").UID
	hub.s.onLoop(func() { host{hub.s}.ForceJoin(uid, "#hive") })
	eve.expect(":eve!")
	eve.expect(" 366 eve #hive ")
	waitLink(t, "hub sees the join", func() bool {
		var in bool
		hub.s.onLoop(func() { ch := hub.s.net.Channel("#hive"); in = ch != nil && ch.Members[uid] != nil })
		return in
	})
	leaf.fromLink(t, ":"+hub.sid+" SVSPART "+uid+" #hive :services say so")
	if l := eve.expect("PART #hive"); !strings.Contains(l, "services say so") {
		t.Errorf("part %q", l)
	}
	leaf.fromLink(t, ":"+hub.sid+" SVSJOIN eve #hive2,#hive3")
	eve.expect(" 366 eve #hive2 ")
	eve.expect(" 366 eve #hive3 ")
}

// An oper on the leaf has their flags on the hub (OperServ there checks
// them), also when they opered before the link came up; deoper clears.
func TestLinkOperFlags(t *testing.T) {
	hub, leaf := linkedPair(t)
	fay := login(t, leaf.s, "fay")
	hub.seen(t, "fay")
	uid := hub.user("fay").UID
	leaf.s.onLoop(func() { leaf.s.operUp(leaf.s.clients[uid], &store.Oper{Class: "netadmin"}) })
	fay.expect(" 381 fay ")
	flags := func() string {
		var f string
		hub.s.onLoop(func() { f = host{hub.s}.OperFlags(uid) })
		return f
	}
	waitLink(t, "flags on the hub", func() bool { return flags() == "acghks" })
	fay.send("OS STATS")
	fay.expect(":OperServ!")
	leaf.s.onLoop(func() { leaf.s.deoper(leaf.s.clients[uid], "") })
	waitLink(t, "deoper on the hub", func() bool { return flags() == "" })
	// UnrealIRCd sends only the class.
	if f := hub.s.flagsForClass("admin-with-override"); f != "ghks" {
		t.Errorf("admin-with-override = %q", f)
	}
}

// "WHOIS nick nick" asks the user's own server, which knows the idle time.
func TestLinkRemoteWhois(t *testing.T) {
	hub, leaf := linkedPair(t)
	gus := login(t, leaf.s, "gus")
	ann := login(t, hub.s, "ann")
	hub.seen(t, "gus")
	leaf.seen(t, "ann")
	ann.send("WHOIS gus")
	ann.expect(" 312 ann gus leaf.test.local ")
	if l := ann.expect(" 318 "); strings.Contains(l, "317") {
		t.Fatal(l)
	}
	ann.send("WHOIS gus gus")
	ann.expect(" 311 ann gus ")
	ann.expect(" 317 ann gus ")
	ann.expect(" 318 ann gus ")
	ann.send("WHOIS leaf.test.local nobody")
	ann.expect(" 401 ann nobody ")
	gus.send("WHOIS ann ann") // ann is on the hub: same thing the other way
	gus.expect(" 317 gus ann ")
	gus.send("WHOIS services.test.local NickServ")
	gus.expect(" 313 gus NickServ :is a Network Service")
}

// SQLINE (nick bans) and SNLINE (realname bans) from the hub's OperServ
// reach the leaf; a Q-line from UnrealIRCd is kept too.
func TestLinkNickAndRealnameBans(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	hub.s.onLoop(func() { hub.s.operUp(hub.s.clients[hub.s.net.UserByNick("op").UID], &store.Oper{Class: "netadmin"}) })
	op.expect(" 381 op ")
	op.send("OS SQLINE ADD bad*nick no thanks")
	op.expect(":OperServ!")
	c := login(t, leaf.s, "ivy")
	waitLink(t, "qline on the leaf", func() bool {
		var ok bool
		leaf.s.onLoop(func() { _, ok = leaf.s.qlineFor("badXnick") })
		return ok
	})
	c.send("NICK badXnick")
	if l := c.expect(" 432 "); !strings.Contains(l, "no thanks") {
		t.Errorf("got %q", l)
	}
	op.send("OS SQLINE DEL bad*nick")
	op.expect(":OperServ!")
	waitLink(t, "qline gone on the leaf", func() bool {
		var ok bool
		leaf.s.onLoop(func() { _, ok = leaf.s.qlineFor("badXnick") })
		return !ok
	})
	leaf.fromLink(t, ":"+hub.sid+" TKL + Q * unreal* someone 0 1794444176 :from unreal")
	c.send("NICK unrealfan")
	c.expect(" 432 ")

	hub.seen(t, "ivy")
	spam := raw(t, leaf.s)
	spam.nick = "jon"
	spam.send("NICK jon")
	spam.send("USER jon 0 * :I am a spam bot")
	spam.expect(" 001 jon ")
	hub.seen(t, "jon")
	op.send("OS SNLINE ADD +1h *spam?bot* realname ban")
	spam.expect("ERROR")
	waitLink(t, "jon removed", func() bool { return leaf.user("jon") == nil })
	again := raw(t, leaf.s) // the leaf itself refuses the realname now
	again.nick = "jon2"
	again.send("NICK jon2")
	again.send("USER jon 0 * :I am a spam bot")
	if l := again.expect(" 465 "); !strings.Contains(l, "realname ban") {
		t.Errorf("got %q", l)
	}
}

// UnrealIRCd passes Anope's saslmechlist on with the services SID as the
// target ("MD client 00B saslmechlist ..."), seen on the prod link test.
func TestLinkSASLServerBySID(t *testing.T) {
	hub, leaf := linkedPair(t)
	leaf.s.onLoop(func() { leaf.s.links.saslServer = "" })
	leaf.fromLink(t, ":"+hub.sid+" MD client 9HB saslmechlist :EXTERNAL,PLAIN")
	var got string
	leaf.s.onLoop(func() { got = leaf.s.saslTarget() })
	if got != "services.test.local" {
		t.Errorf("sasl server %q", got)
	}
}
