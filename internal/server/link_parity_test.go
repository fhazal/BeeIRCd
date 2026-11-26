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
