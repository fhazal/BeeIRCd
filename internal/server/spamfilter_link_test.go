package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/spamfilter"
)

func addSpam(t *testing.T, n *linkNode, f spamfilter.Filter) spamfilter.Filter {
	t.Helper()
	var got spamfilter.Filter
	var err error
	n.s.onLoop(func() { got, err = n.s.svc.AddSpamFilter(f, "Tester", false) })
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func hasSpam(n *linkNode, match string) bool {
	found := false
	n.s.onLoop(func() {
		for _, f := range n.s.svc.SpamFilters() {
			if f.Match == match {
				found = true
			}
		}
	})
	return found
}

func TestSpamfilterOverLinks(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	leaf.blocks[0].Autoconnect = false
	hub.start(t)
	leaf.start(t)
	addSpam(t, hub, spamfilter.Filter{Type: spamfilter.Simple, Match: "*free nitro*", Targets: "cp", Action: spamfilter.Block, Reason: "No nitro_spam"})
	leaf.linkNow()
	waitLink(t, "sync", func() bool {
		return hub.logHas("Received end of netburst from") && leaf.logHas("Received end of netburst from")
	})
	waitLink(t, "burst rule", func() bool { return hasSpam(leaf, "*free nitro*") })

	// The leaf checks its own people with the hub's rule, reason intact.
	bob := login(t, leaf.s, "bob")
	bob.send("PRIVMSG #x :free nitro here")
	bob.send("JOIN #x")
	bob.expect(" 366 bob #x ")
	bob.send("PRIVMSG #x :free nitro here")
	bob.expect("*** Your message to #x was blocked: No nitro_spam")

	// Live add on the leaf reaches the hub; removal too.
	f := addSpam(t, leaf, spamfilter.Filter{Type: spamfilter.Regex, Match: `^bot[0-9]{4}!`, Targets: "u", Action: spamfilter.Kill, Reason: "Bot nicks"})
	waitLink(t, "live rule on hub", func() bool { return hasSpam(hub, `^bot[0-9]{4}!`) })
	c := raw(t, hub.s)
	c.send("NICK bot1234")
	c.send("USER x 0 * :x")
	c.expect("(Spam filter: Bot nicks)")
	leaf.s.onLoop(func() { leaf.s.svc.DelSpamFilter(f.ID, "Tester") })
	waitLink(t, "rule gone on hub", func() bool { return !hasSpam(hub, `^bot[0-9]{4}!`) })
	_ = time.Second
}

// A rule removed while the servers are split stays removed after the
// relink, on whichever side it was removed; a rule set during the split
// still reaches the other side.
func TestLinkSpamfilterRemovedDuringSplit(t *testing.T) {
	hub, leaf := linkedPair(t)
	mk := func(match string) spamfilter.Filter {
		return spamfilter.Filter{Type: spamfilter.Simple, Match: match, Targets: "cp", Action: spamfilter.Block, Reason: "r"}
	}
	const onHub, onLeaf, during = "*removed on the hub*", "*removed on the leaf*", "*set during the split*"
	addSpam(t, hub, mk(onHub))
	addSpam(t, hub, mk(onLeaf))
	waitLink(t, "rules on the leaf", func() bool { return hasSpam(leaf, onHub) && hasSpam(leaf, onLeaf) })
	find := func(n *linkNode, match string) (f spamfilter.Filter) {
		n.s.onLoop(func() {
			for _, have := range n.s.svc.SpamFilters() {
				if have.Match == match {
					f = have
				}
			}
		})
		return f
	}

	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	login(t, leaf.s, "ann")
	hub.seen(t, "ann")
	op.send("SQUIT leaf.test.local testing")
	waitLink(t, "split", func() bool { return hub.user("ann") == nil && leaf.user("op") == nil })

	hubRule, leafRule := find(hub, onHub), find(leaf, onLeaf)
	hub.s.onLoop(func() {
		if _, err := hub.s.svc.DelSpamFilter(hubRule.ID, "Tester"); err != nil {
			t.Errorf("hub removal: %v", err)
		}
	})
	leaf.s.onLoop(func() {
		if _, err := leaf.s.svc.DelSpamFilter(leafRule.ID, "Tester"); err != nil {
			t.Errorf("leaf removal: %v", err)
		}
	})
	addSpam(t, hub, mk(during))
	if !hasSpam(leaf, onHub) || !hasSpam(hub, onLeaf) {
		t.Fatal("a removal crossed the split")
	}

	waitBack(t, leaf, "relink", func() bool { return hub.user("ann") != nil && leaf.user("op") != nil })
	gone := func(n *linkNode) bool { return !hasSpam(n, onHub) && !hasSpam(n, onLeaf) }
	waitLink(t, "removed rules gone on both", func() bool { return gone(hub) && gone(leaf) })
	waitLink(t, "rule set during the split synced", func() bool { return hasSpam(leaf, during) })
	// Settled: nothing comes back, and each side asked once.
	hub.s.onLoop(func() {})
	leaf.s.onLoop(func() {})
	if !gone(hub) || !gone(leaf) {
		t.Error("a removed rule came back")
	}
	const asked = "asked it to remove it too"
	if n := strings.Count(hub.log.String(), asked); n != 1 {
		t.Errorf("hub asked %d times, want 1", n)
	}
	if n := strings.Count(leaf.log.String(), asked); n != 1 {
		t.Errorf("leaf asked %d times, want 1", n)
	}
}
