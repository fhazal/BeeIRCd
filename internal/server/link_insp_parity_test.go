package server

import (
	"strings"
	"sync"
	"testing"
)

// recBridge stands in for the InspIRCd bridge: what goes out is kept (and
// not sent), what comes in runs as it is.
type recBridge struct {
	mu  sync.Mutex
	out []string
}

func (r *recBridge) Out(line string) []string {
	r.mu.Lock()
	r.out = append(r.out, line)
	r.mu.Unlock()
	return nil
}

func (r *recBridge) In(line string) (run, reply []string) { return []string{line}, nil }

// sent says whether a line containing want went out.
func (r *recBridge) sent(want string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.out {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// bridged makes the hub's link to the leaf an InspIRCd one, with rec as
// its bridge.
func bridged(t *testing.T, hub *linkNode) *recBridge {
	t.Helper()
	rec := &recBridge{}
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.bridge = rec
		}
	})
	return rec
}

// Server queries towards a server behind an InspIRCd link: MOTD and WHOIS
// go out (the bridge turns them into InspIRCd's form); VERSION and TRACE,
// which InspIRCd 4 has no remote form of, are answered here with their
// ending numeric; WHOIS for someone not behind that link is answered
// here; InspIRCd's IDLE answer becomes the whole WHOIS, with the idle time.
func TestLinkInspQueries(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	rec := bridged(t, hub)
	aliceUID, bobUID := hub.user("alice").UID, hub.user("bob").UID

	alice.send("VERSION leaf.test.local")
	alice.expect("VERSION can't reach leaf.test.local")
	alice.expectNumeric("402", "leaf.test.local")
	alice.send("TRACE leaf.test.local")
	alice.expect("TRACE can't reach leaf.test.local")
	alice.expectNumeric("262", "leaf.test.local")

	alice.send("MOTD leaf.test.local")
	alice.send("WHOIS leaf.test.local bob")
	alice.send("WHOIS leaf.test.local alice")
	alice.expectNumeric("311", "alice")
	alice.expectNumeric("318", "alice")
	if !rec.sent(":"+aliceUID+" MOTD :leaf.test.local") || !rec.sent(":"+aliceUID+" WHOIS 0LF :bob") {
		t.Errorf("MOTD/WHOIS not sent on: %q", rec.out)
	}
	if rec.sent("WHOIS 0LF :alice") {
		t.Error("WHOIS for someone here went to the InspIRCd link")
	}

	hub.fromLink(t, ":"+bobUID+" IDLE "+aliceUID+" 1700000000 42")
	alice.expectNumeric("311", "bob")
	alice.expectNumeric("317", "bob", "42", "1700000000")
	alice.expectNumeric("318", "bob")

	// Their user asks us: the answer goes back on the link.
	hub.fromLink(t, ":"+bobUID+" TIME :hub.test.local")
	waitLink(t, "TIME answered", func() bool { return rec.sent(" 391 " + bobUID + " hub.test.local") })
}
