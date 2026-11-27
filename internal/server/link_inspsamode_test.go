package server

import "testing"

// InspIRCd sends an oper's SAMODE on one of our users as a MODE from that
// oper (":opUID MODE ourUID +w"), which the bridge makes SVS2MODE. It was
// refused (only services may send it); from an IRC operator behind an
// InspIRCd link it now changes our user, still never to +o.
func TestInspOperSamodeOnOurUser(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	lop := login(t, leaf.s, "lop")
	login(t, leaf.s, "bob")
	netadminOn(t, leaf, lop, "lop")
	hub.seen(t, "bob")
	waitLink(t, "hub sees lop as an oper", func() bool {
		var o bool
		hub.s.onLoop(func() { u := hub.s.net.UserByNick("lop"); o = u != nil && u.Modes['o'] })
		return o
	})
	bridged(t, hub)
	modes := func() (w, o bool) {
		hub.s.onLoop(func() { u := hub.s.net.UserByNick("alice"); w, o = u.Modes['w'], u.Modes['o'] })
		return
	}
	if w, _ := modes(); w {
		t.Fatal("alice starts +w")
	}
	aliceUID, lopUID, bobUID := hub.user("alice").UID, hub.user("lop").UID, hub.user("bob").UID

	hub.fromLink(t, ":"+bobUID+" SVS2MODE "+aliceUID+" +w")
	if w, _ := modes(); w {
		t.Fatal("a plain user's SVS2MODE changed alice")
	}
	hub.fromLink(t, ":"+lopUID+" SVS2MODE "+aliceUID+" +wo")
	alice.expect(" MODE alice ")
	if w, o := modes(); !w || o {
		t.Errorf("after the oper's SAMODE: +w %v, +o %v", w, o)
	}
}
