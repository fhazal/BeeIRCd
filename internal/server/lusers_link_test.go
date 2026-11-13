package server

import (
	"strconv"
	"testing"
	"time"
)

// TestLusersOnANetwork: LUSERS counts the servers of the whole network
// and this server's own links, not "1 servers" and "0 servers" as on a
// server alone; the global max counts the whole network too.
func TestLusersOnANetwork(t *testing.T) {
	old := linkRetryFirst
	linkRetryFirst = 100 * time.Millisecond
	t.Cleanup(func() { linkRetryFirst = old })
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	hub.start(t)
	leaf.start(t)
	waitLink(t, "the link", func() bool { return len(hub.servers()) >= 1 && len(leaf.servers()) >= 1 })
	login(t, hub.s, "hubby")
	l := login(t, leaf.s, "leafy")
	var all int
	leaf.s.onLoop(func() { all = len(leaf.s.netServers()) })
	if all < 2 {
		t.Fatalf("netServers: %d", all)
	}
	waitLink(t, "hubby on the leaf", func() bool {
		n := 0
		leaf.s.onLoop(func() { n, _, _ = leaf.s.net.PeopleCounts() })
		return n == 2
	})
	l.send("LUSERS")
	l.expect("invisible on " + strconv.Itoa(all) + " servers")
	l.expect("I have 1 clients and 1 servers")
	l.expect("Current global users 2, max 2")
}
