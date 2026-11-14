package server

import (
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
	waitLink(t, "sync", func() bool { return hub.logHas("sync done") && leaf.logHas("sync done") })
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
