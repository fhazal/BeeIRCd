package server

import (
	"strings"
	"testing"
)

// GLOBOPS is for opers only, and shows with the g snomask letter.
func TestGlobopsLocal(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("MODE Boss +s +g")
	b.sync()
	b.send("GLOBOPS maintenance at noon")
	b.expect("*** GLOBOPS: From Boss: maintenance at noon")

	u := login(t, s, "alice")
	u.send("GLOBOPS nope")
	u.expect(" 481 alice ")
}

// A GLOBOPS crosses a bee link both ways, and shows as the letter G
// notice on the other side: ":<uid> GLOBOPS :<text>" on the wire.
func TestGlobopsOverLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	hop := login(t, hub.s, "hubop")
	netadminOn(t, hub, hop, "hubop")
	lop := login(t, leaf.s, "leafop")
	netadminOn(t, leaf, lop, "leafop")
	for c, nick := range map[*tc]string{hop: "hubop", lop: "leafop"} {
		c.send("MODE " + nick + " +s +gG")
		c.send("PING :umode")
		c.expect(" :umode")
	}
	hub.seen(t, "leafop")
	leaf.seen(t, "hubop")

	lop.send("GLOBOPS hello hub")
	lop.expect("*** GLOBOPS: From leafop: hello hub")
	hop.expect("*** GLOBOPS: From leafop: hello hub")
	hop.send("GLOBOPS hello leaf")
	hop.expect("*** GLOBOPS: From hubop: hello leaf")
	lop.expect("*** GLOBOPS: From hubop: hello leaf")

	// A user without +o on the other end is not believed.
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	bob := hub.user("bob")
	hub.fromLink(t, ":"+bob.UID+" GLOBOPS :sneaky")
	hub.fromLink(t, ":"+leaf.sid+" GLOBOPS :from a server")
	hop.expect("*** GLOBOPS: from a server")
	if strings.Contains(hub.log.String(), "sneaky") {
		t.Errorf("a non-oper GLOBOPS was shown")
	}
}

// globopsTree is a hub with two leaves, a and b, whose links the hub
// treats as UnrealIRCd ones; hop is an oper on the hub with +s +gG, and
// ann (on a) and ben (on b) are seen at the hub.
func globopsTree(t *testing.T) (hub *linkNode, hop *tc, ann, ben string) {
	t.Helper()
	hub = newLinkNode(t, "hub.test.local", "0HB", "hub")
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
	hop = login(t, hub.s, "hubop")
	netadminOn(t, hub, hop, "hubop")
	hop.send("MODE hubop +s +gG")
	hop.send("PING :umode")
	hop.expect(" :umode")
	netadminOn(t, a, login(t, a.s, "ann"), "ann")
	login(t, a.s, "bob")
	netadminOn(t, b, login(t, b.s, "ben"), "ben")
	hub.seen(t, "bob")
	waitLink(t, "opers seen at the hub", func() bool {
		x, y := hub.user("ann"), hub.user("ben")
		return x != nil && y != nil && x.Modes['o'] && y.Modes['o']
	})
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.peer.Bee = false
		}
		hub.s.links.debug = true
	})
	return hub, hop, hub.user("ann").UID, hub.user("ben").UID
}

// UnrealIRCd sends a GLOBOPS on as ":sid SENDUMODE o :from nick: text"
// (globops.c, send.c sendto_umode_global). It was dropped; it now shows
// as a G notice and goes on from the oper, while other SENDUMODE and
// SENDSNO lines stay out.
func TestGlobopsFromUnrealSendUmode(t *testing.T) {
	hub, hop, ann, _ := globopsTree(t)
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE o :from ann: hello opers")
	hop.expect("*** GLOBOPS: From ann: hello opers")
	waitLink(t, "sent on from ann", func() bool {
		return hub.logHas("link b.test.local -> :" + ann + " GLOBOPS :hello opers")
	})

	// UnrealIRCd 6 really sends the mode as "+o" (seen live by QA).
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE +o :from ann: r6 wire check")
	hop.expect("*** GLOBOPS: From ann: r6 wire check")
	waitLink(t, "sent on +o from ann", func() bool {
		return hub.logHas("link b.test.local -> :" + ann + " GLOBOPS :r6 wire check")
	})

	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE +s :from ann: wrong plus mode")
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE o :from bob: not an oper")
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE o :Link a.test.local is now synced")
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE s :from ann: wrong mode")
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDSNO s :from ann: a snomask notice")
	hub.fromLinkTo(t, "a.test.local", ":0AA SENDUMODE o :from a.test.local: from the server")
	hop.expect("*** GLOBOPS: From a.test.local: from the server")
	for _, bad := range []string{"not an oper", "is now synced", "wrong mode", "wrong plus mode", "a snomask notice"} {
		if hub.logHas("GLOBOPS: "+bad) || hub.logHas("GLOBOPS :"+bad) {
			t.Errorf("%q became a GLOBOPS", bad)
		}
	}
}

// A server's GLOBOPS "From nick: text" (how InspIRCd's comes in) went to
// UnrealIRCd as it was, shown there as "from insp.qa.local: From r5ci:
// msg". It now goes from the oper, ":uid GLOBOPS :msg", and as Unreal's own
// ":sid SENDUMODE o :from nick: msg" when the nick is nobody known.
func TestGlobopsServerLineToUnreal(t *testing.T) {
	hub, hop, _, ben := globopsTree(t)
	hub.fromLinkTo(t, "b.test.local", ":0BB GLOBOPS :From ben: hi there")
	hop.expect("*** GLOBOPS: From ben: hi there")
	waitLink(t, "sent on from ben", func() bool {
		return hub.logHas("link a.test.local -> :" + ben + " GLOBOPS :hi there")
	})
	hub.fromLinkTo(t, "b.test.local", ":0BB GLOBOPS :From ghost: boo")
	hop.expect("*** GLOBOPS: From ghost: boo")
	waitLink(t, "sent on as SENDUMODE", func() bool {
		return hub.logHas("link a.test.local -> :0BB SENDUMODE o :from ghost: boo")
	})
}

// wallopsWatch logs nick in on n with +w, ready to read WALLOPS.
func wallopsWatch(t *testing.T, n *linkNode, nick string) *tc {
	t.Helper()
	c := login(t, n.s, nick)
	c.send("MODE " + nick + " +w")
	c.send("PING :umode")
	c.expect(" :umode")
	return c
}

// A WALLOPS went only to +w people on the sender's own server. It now
// crosses a bee link (":<uid> WALLOPS :<text>") and shows to everyone
// with +w there, from the oper's full prefix; people without +w don't
// get it, and one from a non-oper on the other end is dropped.
func TestWallopsOverLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	hop := login(t, hub.s, "hubop")
	netadminOn(t, hub, hop, "hubop")
	hw := wallopsWatch(t, hub, "hubw")
	lw := wallopsWatch(t, leaf, "leafw")
	plain := login(t, leaf.s, "plain")
	leaf.seen(t, "hubop")
	waitLink(t, "hubop is an oper at the leaf", func() bool {
		u := leaf.user("hubop")
		return u != nil && u.Modes['o']
	})

	hop.send("WALLOPS :network maintenance soon")
	hw.expect(":hubop!~hubop@")
	if l := lw.expect(" WALLOPS :network maintenance soon"); !strings.HasPrefix(l, ":hubop!~hubop@") {
		t.Errorf("leaf +w got %q, want the oper's prefix", l)
	}
	plain.quiet("WALLOPS")

	// A non-oper on the other end is not believed; a server is.
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	bob := hub.user("bob")
	hub.fromLink(t, ":"+bob.UID+" WALLOPS :sneaky")
	hub.fromLink(t, ":"+leaf.sid+" WALLOPS :from a server")
	if l := hw.expect(" WALLOPS :from a server"); l != ":leaf.test.local WALLOPS :from a server" {
		t.Errorf("server WALLOPS shown as %q", l)
	}
	hw.quiet("sneaky")
}

// On a chain a - hub - b, an oper's WALLOPS on a reaches +w people on b,
// passed on by the hub (and not sent back to a).
func TestWallopsThreeServers(t *testing.T) {
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
	ann := login(t, a.s, "ann")
	netadminOn(t, a, ann, "ann")
	aw := wallopsWatch(t, a, "annw")
	bw := wallopsWatch(t, b, "benw")
	waitLink(t, "ann is an oper on b", func() bool {
		u := b.user("ann")
		return u != nil && u.Modes['o']
	})
	ann.send("WALLOPS :far end hello")
	aw.expect(":ann!~ann@")
	if l := bw.expect(" WALLOPS :far end hello"); !strings.HasPrefix(l, ":ann!~ann@") {
		t.Errorf("far end got %q", l)
	}
	aw.quiet("far end hello") // not echoed back by the hub
}

// UnrealIRCd links: a WALLOPS comes in as ":<uid> WALLOPS :<text>" and
// goes out the same way (Unreal's cmd_wallops sends client->id).
func TestWallopsUnrealLinks(t *testing.T) {
	hub, hop, ann, _ := globopsTree(t)
	hop.send("MODE hubop +w")
	hop.send("PING :umode")
	hop.expect(" :umode")

	hub.fromLinkTo(t, "a.test.local", ":"+ann+" WALLOPS :from unreal")
	hop.expect(":ann!~ann@")
	waitLink(t, "sent on to b", func() bool {
		return hub.logHas("link b.test.local -> :" + ann + " WALLOPS :from unreal")
	})
	if hub.logHas("link a.test.local -> :" + ann + " WALLOPS :from unreal") {
		t.Errorf("WALLOPS sent back to where it came from")
	}

	hopUID := hub.user("hubop").UID
	hop.send("WALLOPS :to unreal")
	hop.expect(" WALLOPS :to unreal")
	waitLink(t, "sent to both unreal links", func() bool {
		return hub.logHas("link a.test.local -> :"+hopUID+" WALLOPS :to unreal") &&
			hub.logHas("link b.test.local -> :"+hopUID+" WALLOPS :to unreal")
	})

	// bob (on a) is no oper: dropped, not passed on.
	bob := hub.user("bob").UID
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" WALLOPS :not an oper")
	hop.quiet("not an oper")
	if hub.logHas("WALLOPS :not an oper") {
		t.Errorf("a non-oper WALLOPS was passed on")
	}
}
