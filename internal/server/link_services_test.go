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
	waitLink(t, "flags on the hub", func() bool { return flags() == "abcghks" })
	fay.send("OS STATS")
	fay.expect(":OperServ!")
	leaf.s.onLoop(func() { leaf.s.deoper(leaf.s.clients[uid], "") })
	waitLink(t, "deoper on the hub", func() bool { return flags() == "" })
	// UnrealIRCd sends only the class.
	if f := hub.s.flagsForClass("admin-with-override"); f != "ghks" {
		t.Errorf("admin-with-override = %q", f)
	}
	// InspIRCd sends its oper type: its default names match ours.
	for typ, want := range map[string]string{"NetAdmin": "abcghks", "GlobalOp": "hk", "Helper": "h", "Custom": ""} {
		if f := hub.s.flagsForClass(typ); f != want {
			t.Errorf("type %s = %q, want %q", typ, f, want)
		}
	}
}

// An UnrealIRCd oper's flags (worked out from their class) reach the
// BeeIRCd servers behind us live, not only in a burst: those take flags
// only from bee-operflags.
func TestLinkClassFlagsPassedOn(t *testing.T) {
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
	login(t, a.s, "bob")
	b.seen(t, "bob")
	uid := b.user("bob").UID
	hub.s.onLoop(func() { // a plays UnrealIRCd
		for l := range hub.s.links.conns {
			if l.peer.Name == "a.test.local" {
				l.peer.Bee = false
			}
		}
	})
	hub.fromLinkTo(t, "a.test.local", ":"+uid+" UMODE2 +o")
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+" operclass :globop")
	waitLink(t, "bob's flags on b", func() bool {
		u := b.user("bob")
		return u != nil && u.Modes['o'] && u.OperClass == "globop" && u.OperFlags == "hk"
	})
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+" operclass")
	waitLink(t, "bob's flags gone on b", func() bool { u := b.user("bob"); return u != nil && u.OperFlags == "" })
}

// OLINE and UMODE check the target's rank across a link: an admin on the
// leaf has flags the hub knows (bee-operflags), and an oper whose flags
// aren't known (only +o arrived, as from an InspIRCd type or UnrealIRCd
// class with no match here) counts as having every flag.
func TestLinkRemoteOperRank(t *testing.T) {
	hub, leaf := linkedPair(t)
	fay := login(t, leaf.s, "fay")
	gus := login(t, leaf.s, "gus")
	small := login(t, hub.s, "Small")
	mid := login(t, hub.s, "Mid")
	top := login(t, hub.s, "Top")
	hub.seen(t, "gus")
	operOn := func(c *tc, nick string, o *store.Oper) {
		hub.s.onLoop(func() { hub.s.operUp(hub.s.clients[hub.s.net.UserByNick(nick).UID], o) })
		c.expect(" 381 " + nick + " ")
	}
	operOn(small, "Small", &store.Oper{Flags: "s"})
	operOn(mid, "Mid", &store.Oper{Flags: "aghks"})
	operOn(top, "Top", &store.Oper{Class: "netadmin"})
	fuid, guid := hub.user("fay").UID, hub.user("gus").UID
	hubOS := func(nick, text string) string {
		return ":OperServ!services@services.test.local NOTICE " + nick + " :" + text
	}
	flags := func(uid string) string {
		var f string
		hub.s.onLoop(func() { f = host{hub.s}.OperFlags(uid) })
		return f
	}

	// A known admin: their flags crossed the link.
	leaf.s.onLoop(func() { leaf.s.operUp(leaf.s.clients[fuid], &store.Oper{Class: "netadmin"}) })
	fay.expect(" 381 fay ")
	waitLink(t, "fay's flags on the hub", func() bool { return flags(fuid) == "abcghks" })
	small.send("OS UMODE fay -o")
	small.expect(hubOS("Small", "fay has oper flag a, which you don't have."))
	mid.send("OS OLINE fay -k")
	mid.expect(hubOS("Mid", "fay has oper flag b, which you don't have."))

	// Rank not known: only +o.
	hub.fromLink(t, ":"+guid+" UMODE2 +o")
	waitLink(t, "gus is an oper on the hub", func() bool { return flags(guid) == "o" })
	unknown := "gus is an oper on another server whose oper flags aren't known here; only an oper with every flag (abcghks) may change them."
	small.send("OS UMODE gus -o")
	small.expect(hubOS("Small", unknown))
	mid.send("OS OLINE gus -k")
	mid.expect(hubOS("Mid", unknown))
	top.send("OS OLINE gus +k") // every flag: on to OLINE's own answer
	top.expect(hubOS("Top", "gus is on another server; OLINE works only for people on this server."))
	_ = gus
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

// BotServ kickers and fantasy commands also catch people on a leaf: their
// channel text reaches the hub's services over the link.
func TestLinkBotServLeafText(t *testing.T) {
	hub, leaf := linkedPair(t)
	own := login(t, hub.s, "own")
	own.send("NS REGISTER " + pw)
	own.expect("is now registered to you.")
	uid := hub.user("own").UID
	hub.s.onLoop(func() { hub.s.operUp(hub.s.clients[uid], &store.Oper{Class: "netadmin"}) })
	own.expect(" 381 own ")
	own.send("BS BOT ADD Buzz bot bot.host Busy Bee")
	own.expect("added to the bot list.")
	own.send("JOIN #hive")
	own.expect(" 366 own #hive ")
	own.send("CS REGISTER #hive the hive")
	own.expect("is registered to own")
	own.send("BS ASSIGN #hive Buzz")
	own.expect("has been assigned to #hive.")
	own.send("BS BADWORDS #hive ADD zzbad")
	own.expect("zzbad added to #hive bad words list.")
	own.send("BS KICK BADWORDS #hive ON")
	own.expect("BotServ!")

	lea := login(t, leaf.s, "lea")
	hub.seen(t, "lea")
	lea.send("JOIN #hive")
	lea.expect(" 366 lea #hive ")
	own.expect(":lea!")
	lea.send("PRIVMSG #hive :a zzbad word")
	lea.expect(":Buzz!bot@bot.host KICK #hive lea :")
	own.expect(":Buzz!bot@bot.host KICK #hive lea :")

	// Fantasy needs FANTASIA (channel access), like Anope.
	lea.send("NS REGISTER " + pw)
	lea.expect("is now registered to you.")
	own.send("CS ACCESS #hive ADD lea 5")
	own.expect("lea added to the access list of #hive")
	lea.send("JOIN #hive")
	lea.expect(" 366 lea #hive ")
	lea.send("PRIVMSG #hive :!help")
	lea.expect(":Buzz!bot@bot.host NOTICE lea :")
	lea.send("PRIVMSG #hive :!topic leaf topic")
	own.expect("TOPIC #hive :leaf topic")
}

// A listed helper on a leaf gets +h from the hub's HelpServ, and can go
// off and on duty with HelpServ OFF / ON.
func TestLinkHelperOnLeaf(t *testing.T) {
	hub, leaf := linkedPair(t)
	b := login(t, hub.s, "boss")
	b.send("NS REGISTER " + pw)
	b.expect("is now registered to you.")
	operEntryOn(t, hub, "boss", "s", []string{"*@*"})
	b.send("OPER boss " + operPW)
	b.expect(" 381 ")

	dan := login(t, leaf.s, "dan")
	dan.send("NS REGISTER " + pw)
	dan.expect("is now registered to you.")
	hub.seen(t, "dan")
	waitLink(t, "dan's account on the hub", func() bool { u := hub.user("dan"); return u != nil && u.Account == "dan" })
	b.send("HE HELPER ADD dan")
	b.expect("dan is a helper now")
	dan.expect("MODE dan +h")
	dan.expect("You are a helper")
	dan.send("WHOIS dan")
	dan.expectNumeric("310", "dan")
	dan.send("HE OFF")
	dan.expect("MODE dan -h")
	dan.send("HE ON")
	dan.expect("MODE dan +h")
	dan.expect("You are on duty")
	waitLink(t, "hub sees +h", func() bool { u := hub.user("dan"); return u != nil && u.Modes['h'] })
}

// An SNLINE /regex/ reaches the leaf with its case: lowercased, \W turned
// into \w there and the leaf refused the opposite set of people.
func TestLinkSNLineRegexKeepsCase(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	hub.s.onLoop(func() { hub.s.operUp(hub.s.clients[hub.s.net.UserByNick("op").UID], &store.Oper{Class: "netadmin"}) })
	op.expect(" 381 op ")
	op.send(`OS SNLINE ADD +1h /^\W+$/ symbols only`)
	op.expect(":OperServ!")
	var masks []string
	waitLink(t, "snline on the leaf", func() bool {
		masks = nil
		leaf.s.onLoop(func() {
			for _, b := range leaf.s.svc.LinkedBans() {
				if b.Kind == store.BanSNLine {
					masks = append(masks, b.Mask)
				}
			}
		})
		return len(masks) > 0
	})
	if want := `/^\W+$/`; len(masks) != 1 || masks[0] != want {
		t.Errorf("leaf keeps SNLINE %q, want %s", masks, want)
	}
}

// OS UMODE on someone on the leaf: the leaf sets the modes (SVS2MODE),
// not only the reply saying so.
func TestLinkOperServUmodeRemote(t *testing.T) {
	hub, leaf := linkedPair(t)
	eve := login(t, leaf.s, "eve")
	hub.seen(t, "eve")
	uid := hub.user("eve").UID
	hub.s.onLoop(func() { host{hub.s}.SetUserModes(uid, "", "+w") })
	eve.expect("MODE eve +w")
	waitLink(t, "hub sees +w", func() bool {
		var w bool
		hub.s.onLoop(func() { u := hub.s.net.UserByUID(uid); w = u != nil && u.Modes['w'] })
		return w
	})
}
