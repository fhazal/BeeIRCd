package server

import (
	"strings"
	"testing"
	"time"
)

// msgidOf is the msgid tag of a line.
func msgidOf(t *testing.T, line string) string {
	t.Helper()
	i := strings.Index(line, "msgid=")
	if i < 0 {
		t.Fatalf("no msgid: %s", line)
	}
	rest := line[i+6:]
	if j := strings.IndexAny(rest, "; "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestRedact(t *testing.T) {
	s := testServer(t, draftsOn)
	bee := capLogin(t, s, "Bee", "message-tags", "echo-message", redactCap)
	ember := capLogin(t, s, "ember", "message-tags", redactCap)
	nova := login(t, s, "nova")
	joinAll("#a", bee, ember, nova) // Bee made #a: op

	ember.send("PRIVMSG #a :oops, wrong channel")
	id := msgidOf(t, bee.expect("PRIVMSG #a :oops"))
	nova.expect("PRIVMSG #a :oops")
	ember.send("REDACT #a " + id + " :my mistake")
	bee.expect(":ember!~ember@127.0.0.1 REDACT #a " + id + " :my mistake")
	ember.expect("REDACT #a " + id)
	nova.quiet("REDACT")

	// Someone else's message: only ops.
	bee.send("PRIVMSG #a :op talking")
	id = msgidOf(t, ember.expect("PRIVMSG #a :op talking"))
	ember.send("REDACT #a " + id)
	ember.expect("FAIL REDACT REDACT_FORBIDDEN #a " + id + " ")
	ember.send("PRIVMSG #a :spam spam")
	id = msgidOf(t, bee.expect("PRIVMSG #a :spam spam"))
	bee.send("REDACT #a " + id)
	ember.expect("Bee!~bee@127.0.0.1 REDACT #a " + id)

	// After the window only ops can.
	ember.send("PRIVMSG #a :old")
	id = msgidOf(t, bee.expect("PRIVMSG #a :old"))
	s.do(func() { r := s.recent.by[id]; r.at = r.at.Add(-time.Hour); s.recent.by[id] = r })
	ember.send("REDACT #a " + id)
	ember.expect("FAIL REDACT REDACT_WINDOW_EXPIRED #a " + id + " 900 ")
	ember.send("REDACT #a nope")
	ember.expect("FAIL REDACT UNKNOWN_MSGID #a nope ")

	// A private message.
	ember.send("PRIVMSG Bee :secret")
	id = msgidOf(t, bee.expect("PRIVMSG Bee :secret"))
	ember.send("REDACT Bee " + id)
	bee.expect("REDACT Bee " + id)
}

// A redacted line leaves +H history.
func TestRedactHistory(t *testing.T) {
	s := testServer(t, draftsOn)
	bee := capLogin(t, s, "Bee", "message-tags", "echo-message", redactCap)
	bee.send("JOIN #h")
	bee.expect(" 366 Bee #h ")
	bee.send("MODE #h +H 50:1d")
	bee.expect("MODE #h +H")
	bee.send("PRIVMSG #h :keep")
	bee.expect("PRIVMSG #h :keep")
	bee.send("PRIVMSG #h :forget me")
	id := msgidOf(t, bee.expect("PRIVMSG #h :forget me"))
	bee.send("REDACT #h " + id)
	bee.expect("REDACT #h " + id)
	late := login(t, s, "late")
	late.send("JOIN #h")
	late.expect("PRIVMSG #h :keep")
	late.send("PING :x")
	late.expect("PONG")
	late.quiet("forget me")
}

// Redactions reach the other BeeIRCd servers.
func TestLinkRedact(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	bee := capLogin(t, hub.s, "Bee", "message-tags", redactCap)
	bob := capLogin(t, leaf.s, "bob", "message-tags", redactCap)
	hub.seen(t, "bob")
	bee.send("JOIN #x")
	bee.expect(" 366 Bee #x ")
	bob.send("JOIN #x")
	bob.expect(" 366 bob #x ")
	waitLink(t, "bob in #x on the hub", func() bool {
		n := 0
		hub.s.onLoop(func() {
			if ch := hub.s.net.Channel("#x"); ch != nil {
				n = len(ch.Members)
			}
		})
		return n == 2
	})
	bob.send("PRIVMSG #x :from the leaf")
	id := msgidOf(t, bee.expect("PRIVMSG #x :from the leaf"))
	bee.send("REDACT #x " + id + " :off topic")
	bob.expect(":Bee!~bee@127.0.0.1 REDACT #x " + id + " :off topic")
}
