package server

import (
	"strings"
	"testing"
)

// A KNOCK only reached the channel's ops on the knocker's own server.
// It now crosses the link (":<uid> KNOCK #chan :reason", as UnrealIRCd 6
// sends it) and every server shows it to its own ops.
func TestKnockOverLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	lop := login(t, leaf.s, "leafop")
	joinAll("#k", lop)
	lop.send("MODE #k +i")
	lop.expect("MODE #k +i")
	hub.seen(t, "leafop")
	waitLink(t, "the hub knows #k +i", func() bool {
		var ok bool
		hub.s.onLoop(func() { ch := hub.s.net.Channel("#k"); ok = ch != nil && ch.HasMode('i') })
		return ok
	})
	k := login(t, hub.s, "knocker")
	leaf.seen(t, "knocker")
	k.send("KNOCK #k :let me in")
	k.expect("Knocked on #k")
	if l := lop.expect("[Knock]"); !strings.HasPrefix(l, ":leaf.test.local NOTICE @#k :[Knock] by knocker!~knocker@") || !strings.HasSuffix(l, " (let me in)") {
		t.Errorf("leaf op got %q", l)
	}

	// Without a reason it says so, like at home.
	k.send("KNOCK #k")
	if l := lop.expect("[Knock]"); !strings.HasSuffix(l, " (no reason specified)") {
		t.Errorf("leaf op got %q", l)
	}

	// From someone who is no user behind that link: dropped.
	leaf.fromLink(t, ":"+leaf.user("leafop").UID+" KNOCK #k :spoofed")
	lop.quiet("spoofed")
}

// UnrealIRCd links: a knock comes in as ":<uid> KNOCK #chan :reason";
// the ops here see it, it goes on to the other links (not back), and a
// knock here goes out to every link the same way.
func TestKnockUnrealLinks(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	joinAll("#k", hop)
	hop.send("MODE #k +i")
	hop.expect("MODE #k +i")
	bob := hub.user("bob").UID

	hub.fromLinkTo(t, "a.test.local", ":"+bob+" KNOCK #k :from unreal")
	if l := hop.expect("[Knock]"); !strings.HasPrefix(l, ":hub.test.local NOTICE @#k :[Knock] by bob!") || !strings.HasSuffix(l, " (from unreal)") {
		t.Errorf("hub op got %q", l)
	}
	waitLink(t, "sent on to b", func() bool {
		return hub.logHas("link b.test.local -> :" + bob + " KNOCK #k :from unreal")
	})
	if hub.logHas("link a.test.local -> :" + bob + " KNOCK") {
		t.Errorf("KNOCK sent back to where it came from")
	}

	// Not on a channel that isn't closed to them, or with +K.
	hop.send("MODE #k -i")
	hop.expect("MODE #k -i")
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" KNOCK #k :open channel")
	hop.quiet("open channel")

	k := login(t, hub.s, "knocker")
	hop.send("MODE #k +i")
	hop.expect("MODE #k +i")
	k.send("KNOCK #k :to unreal")
	k.expect("Knocked on #k")
	uid := hub.user("knocker").UID
	waitLink(t, "sent to both unreal links", func() bool {
		return hub.logHas("link a.test.local -> :"+uid+" KNOCK #k :to unreal") &&
			hub.logHas("link b.test.local -> :"+uid+" KNOCK #k :to unreal")
	})
	if hub.logHas("link b.test.local -> :" + bob + " KNOCK #k :open channel") {
		t.Errorf("a refused knock went on")
	}
}

// SAMODE on someone on another server said "No such nick". It now goes
// to their server, which sets it, and the change comes back as theirs.
func TestSamodeOverLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	leaf.seen(t, "op")

	op.send("SAMODE bob +B")
	if l := bob.expect(" MODE bob "); !strings.HasSuffix(l, "+B") {
		t.Errorf("bob got %q", l)
	}
	waitLink(t, "the hub sees bob +B", func() bool {
		u := hub.user("bob")
		return u != nil && u.Modes['B']
	})

	// Not for someone who isn't an oper with flag a.
	pl := login(t, hub.s, "plain")
	pl.send("SAMODE bob -B")
	pl.expectNumeric("481")
	bob.quiet("-B")

	// A relayed SAMODE from a user who isn't an oper is refused there.
	leaf.fromLink(t, ":"+leaf.user("plain").UID+" SAMODE "+leaf.user("bob").UID+" :-B")
	bob.quiet("-B")
}

// Behind UnrealIRCd, SAMODE on a user goes as services' SVS2MODE to
// their server (Unreal has no user SAMODE) and is set here as well,
// since Unreal sends nothing back. +o never goes.
func TestSamodeUnrealLinks(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	bob := hub.user("bob").UID
	var svc string
	hub.s.onLoop(func() { svc = hub.s.svcSource() })

	hop.send("SAMODE bob +oB")
	waitLink(t, "SVS2MODE to a", func() bool {
		return hub.logHas("link a.test.local -> :" + svc + " SVS2MODE " + bob + " +B")
	})
	waitLink(t, "set here", func() bool {
		u := hub.user("bob")
		return u != nil && u.Modes['B'] && !u.Modes['o']
	})
	// It goes on with the oper behind it (unrealircd.org/issued-by).
	if !hub.logHas("link b.test.local -> @unrealircd.org/issued-by=OPER:") || !hub.logHas(" :"+svc+" SVS2MODE "+bob+" +B") {
		t.Errorf("b didn't hear of it")
	}
	if hub.logHas("+oB") {
		t.Errorf("+o went out")
	}

	// From a BeeIRCd oper further away, the hub turns it into SVS2MODE.
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			if l.peer.Name == "b.test.local" {
				l.peer.Bee = true
			}
		}
	})
	ben := hub.user("ben").UID
	hub.fromLinkTo(t, "b.test.local", ":"+ben+" SAMODE "+bob+" :-B")
	waitLink(t, "translated for a", func() bool {
		return hub.logHas("link a.test.local -> :" + svc + " SVS2MODE " + bob + " -B")
	})
	if hub.logHas("link a.test.local -> :" + ben + " SAMODE") {
		t.Errorf("raw SAMODE sent to UnrealIRCd")
	}

	// An UnrealIRCd user's SAMODE is not ours to run.
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" SAMODE "+hub.user("hubop").UID+" :+B")
	hop.quiet(" MODE hubop")
}

// Opers could not send to $servermask from a client ("No such nick").
// With oper flag g (UnrealIRCd's chat:notice:global) it now reaches
// everyone on the matching servers, here and over the links.
func TestServerMaskMessage(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	home := login(t, hub.s, "home")
	far := login(t, leaf.s, "far")
	leaf.seen(t, "op")

	op.send("NOTICE $*.test.local :hello everyone")
	if l := far.expect("hello everyone"); !strings.HasPrefix(l, ":op!") || !strings.Contains(l, " NOTICE $*.test.local :") {
		t.Errorf("far got %q", l)
	}
	home.expect("NOTICE $*.test.local :hello everyone")
	op.quiet("hello everyone")

	// Only the servers the mask names.
	op.send("PRIVMSG $leaf.test.local :leaf only")
	far.expect("PRIVMSG $leaf.test.local :leaf only")
	home.quiet("leaf only")

	// Not for users.
	home.send("PRIVMSG $*.test.local :spam")
	home.expectNumeric("481")
	far.quiet("spam")
}

// To UnrealIRCd links a $mask message goes as ":<uid> PRIVMSG $mask :text".
func TestServerMaskMessageUnrealLinks(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	uid := hub.user("hubop").UID
	hop.send("PRIVMSG $*.test.local :to all")
	waitLink(t, "sent to both links", func() bool {
		return hub.logHas("link a.test.local -> :"+uid+" PRIVMSG $*.test.local :to all") &&
			hub.logHas("link b.test.local -> :"+uid+" PRIVMSG $*.test.local :to all")
	})
}

// INFO, LUSERS, CONNECT and REHASH with a server only ran here (or said
// "No such server"). They now go to that server, which answers.
func TestRoutedQueriesOverLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	pl := login(t, hub.s, "plain")
	leaf.seen(t, "op")
	leaf.seen(t, "plain")

	pl.send("INFO leaf.test.local")
	pl.expect(":leaf.test.local 371 plain :")
	pl.expect(":leaf.test.local 374 plain ")
	pl.send("INFO no.such.server")
	pl.expectNumeric("402")
	pl.send("INFO hub.test.local")
	pl.expect(":hub.test.local 374 plain ")

	pl.send("LUSERS leaf.test.local")
	pl.expect(":leaf.test.local 251 plain ")
	pl.send("LUSERS")
	pl.expect(":hub.test.local 251 plain ")

	op.send("CONNECT nowhere 6667 leaf.test.local")
	if l := op.expect("nowhere"); !strings.HasPrefix(l, ":leaf.test.local NOTICE op :") || !strings.Contains(l, "no link block for nowhere") {
		t.Errorf("op got %q", l)
	}
	pl.send("CONNECT nowhere 6667 leaf.test.local")
	pl.expectNumeric("481")
	op.send("CONNECT nowhere 6667 no.such.server")
	op.expectNumeric("402")

	op.send("REHASH leaf.test.local")
	op.expect(":leaf.test.local 382 op ")
	pl.send("REHASH leaf.test.local")
	pl.expectNumeric("481")
	op.send("REHASH no.such.server")
	op.expectNumeric("402")
}

// UnrealIRCd's forms (hunt_server): the server argument is replaced by
// a SID or a UID and sits where the command keeps it (REHASH <server>
// [-flags], CONNECT <server> <port> <remote>).
func TestRoutedQueriesUnrealLinks(t *testing.T) {
	hub, hop, ann, _ := globopsTree(t)
	bob := hub.user("bob").UID
	me := hub.user("hubop").UID

	hub.fromLinkTo(t, "a.test.local", ":"+bob+" INFO :0HB")
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" LUSERS :"+me)
	waitLink(t, "answered", func() bool {
		return hub.logHas("374 "+bob+" ") && hub.logHas("251 "+bob+" ")
	})

	// ann's flags are known here (in this rig her server tells them).
	hub.fromLinkTo(t, "a.test.local", ":"+ann+" REHASH 0HB -motd")
	hub.fromLinkTo(t, "a.test.local", ":"+ann+" CONNECT nowhere 6667 :0HB")
	waitLink(t, "both done", func() bool {
		return hub.logHas("382 "+ann+" ") && hub.logHas("NOTICE "+ann+" :*** There is no link block for nowhere.")
	})
	// Not for users.
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" REHASH 0HB")
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" CONNECT nowhere 6667 :0HB")
	waitLink(t, "both refused", func() bool {
		return strings.Count(hub.log.String(), "481 "+bob+" ") >= 2
	})
	if hub.logHas("382 " + bob) {
		t.Errorf("rehashed for a user")
	}

	// Ours go out the same way.
	hop.send("INFO a.test.local")
	hop.send("CONNECT nowhere 6667 b.test.local")
	hop.send("REHASH a.test.local")
	waitLink(t, "sent", func() bool {
		return hub.logHas("link a.test.local -> :"+me+" INFO :a.test.local") &&
			hub.logHas("link b.test.local -> :"+me+" CONNECT nowhere 6667 :b.test.local") &&
			hub.logHas("link a.test.local -> :"+me+" REHASH :a.test.local")
	})
}
