package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

func TestMultiline(t *testing.T) {
	s := testServer(t, draftsOn)
	bee := capLogin(t, s, "Bee", "batch", "message-tags", "echo-message", multilineCap)
	ember := capLogin(t, s, "ember", "batch", multilineCap)
	nova := login(t, s, "nova")
	joinAll("#a", bee, ember, nova)

	bee.send("BATCH +p1 " + multilineCap + " #a")
	bee.send("@batch=p1 PRIVMSG #a :first line")
	bee.send("@batch=p1 PRIVMSG #a :")
	bee.send("@batch=p1 PRIVMSG #a :third, ")
	bee.send("@batch=p1;" + concatTag + " PRIVMSG #a :joined")
	bee.send("BATCH -p1")

	open := ember.expect(" BATCH +")
	if !strings.Contains(open, "BATCH +m") || !strings.HasSuffix(open, " "+multilineCap+" #a") || !strings.HasPrefix(open, ":Bee!") {
		t.Fatalf("batch open: %s", open)
	}
	id := strings.Fields(open)[2][1:]
	ember.expect("@batch=" + id + " :Bee!~bee@127.0.0.1 PRIVMSG #a :first line")
	ember.expect("@batch=" + id + " :Bee!~bee@127.0.0.1 PRIVMSG #a :")
	ember.expect("PRIVMSG #a :third, ")
	if l := ember.expect("PRIVMSG #a :joined"); !strings.Contains(l, concatTag) {
		t.Errorf("concat tag lost: %s", l)
	}
	ember.expect("BATCH -" + id)

	// Without the cap: separate lines, the blank one left out.
	nova.expect(":Bee!~bee@127.0.0.1 PRIVMSG #a :first line")
	nova.expect("PRIVMSG #a :third, ")
	nova.expect("PRIVMSG #a :joined")

	// echo-message: the sender gets the batch back, with a msgid on it.
	if l := bee.expect(" BATCH +"); !strings.Contains(l, "msgid=") {
		t.Errorf("echo batch without msgid: %s", l)
	}
	bee.expect("BATCH -")

	// Limits and mistakes.
	bee.send("BATCH +p2 " + multilineCap + " #a")
	for i := 0; i < 25; i++ {
		bee.send("@batch=p2 PRIVMSG #a :x")
	}
	bee.send("BATCH -p2")
	bee.expect("FAIL BATCH MULTILINE_MAX_LINES 24 ")
	bee.send("BATCH +p3 " + multilineCap + " #a")
	bee.send("@batch=p3 PRIVMSG #b :elsewhere")
	bee.send("BATCH -p3")
	bee.expect("FAIL BATCH MULTILINE_INVALID_TARGET #a #b ")
	bee.send("BATCH +p4 " + multilineCap + " #a")
	bee.send("@batch=p4 PRIVMSG #a :" + strings.Repeat("y", 400))
	for i := 0; i < 10; i++ {
		bee.send("@batch=p4;" + concatTag + " PRIVMSG #a :" + strings.Repeat("y", 400))
	}
	bee.send("BATCH -p4")
	bee.expect("FAIL BATCH MULTILINE_MAX_BYTES 4096 ")
	nova.quiet("elsewhere")

	// To a person.
	bee.send("BATCH +p5 " + multilineCap + " ember")
	bee.send("@batch=p5 PRIVMSG ember :hi")
	bee.send("@batch=p5 PRIVMSG ember :there")
	bee.send("BATCH -p5")
	ember.expect(multilineCap + " ember")
	ember.expect("PRIVMSG ember :hi")
	ember.expect("PRIVMSG ember :there")
}

func TestMultilineCapValue(t *testing.T) {
	s := testServer(t, draftsOn)
	c := raw(t, s)
	c.send("CAP LS 302")
	if l := c.expect("CAP * LS"); !strings.Contains(l, multilineCap+"=max-bytes=4096,max-lines=24") {
		t.Errorf("CAP LS: %s", l)
	}
}

// Other servers get the lines one by one; +H history keeps each line.
func TestLinkMultiline(t *testing.T) {
	hub, leaf := linkedPair(t)
	hub.s.do(func() { hub.s.cfg.Server.IRCv3Drafts = true })
	bee := capLogin(t, hub.s, "Bee", "batch", multilineCap)
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	bee.send("JOIN #x")
	bee.expect(" 366 Bee #x ")
	bee.send("MODE #x +H 50:1d")
	bee.expect("MODE #x +H")
	bob.send("JOIN #x")
	bob.expect(" 366 bob #x ")
	waitLink(t, "bob in #x on the hub", func() bool { ch := hub.s.net.Channel("#x"); return ch != nil && len(ch.Members) == 2 })

	bee.send("BATCH +q " + multilineCap + " #x")
	bee.send("@batch=q PRIVMSG #x :one")
	bee.send("@batch=q PRIVMSG #x :two")
	bee.send("BATCH -q")
	bob.expect("PRIVMSG #x :one")
	bob.expect("PRIVMSG #x :two")

	late := login(t, hub.s, "late")
	late.send("JOIN #x")
	late.expect("PRIVMSG #x :one")
	late.expect("PRIVMSG #x :two")
}

// labeled-response: the label of BATCH + comes back on the echoed batch.
func TestMultilineLabel(t *testing.T) {
	s := testServer(t, draftsOn)
	bee := capLogin(t, s, "Bee", "batch", "echo-message", "labeled-response", "message-tags", multilineCap)
	bee.send("JOIN #a")
	bee.expect(" 366 Bee #a ")
	bee.send("@label=xyz BATCH +1 " + multilineCap + " #a")
	bee.send("@batch=1 PRIVMSG #a :hi")
	bee.send("BATCH -1")
	l := bee.expect(" BATCH +")
	if !strings.Contains(l, "label=xyz") || !strings.Contains(l, multilineCap+" #a") {
		t.Errorf("echo batch: %s", l)
	}
	bee.expect("PRIVMSG #a :hi")
	bee.expect(" BATCH -")
	bee.send("@label=abc BATCH +2 " + multilineCap + " #a")
	bee.send("BATCH -2")
	if l := bee.expect("FAIL BATCH MULTILINE_INVALID"); !strings.Contains(l, "label=abc") {
		t.Errorf("fail without label: %s", l)
	}
}

// The spam filter checks a multiline message as a whole: one bad line,
// or a bad word split over concat lines, stops the batch.
func TestMultilineSpamfilter(t *testing.T) {
	s, _, _ := fileServerWith(t, "", func(s *Server) { s.cfg.Server.IRCv3Drafts = true })
	b := boss(t, s)
	spamRule(t, b, "c block - No_spam *qaspamword*")
	v := capLogin(t, s, "Vic", "batch", multilineCap)
	a := login(t, s, "Ann")
	joinAll("#ch", v, a)
	v.send("BATCH +a " + multilineCap + " #ch")
	v.send("@batch=a PRIVMSG #ch :hello")
	v.send("@batch=a PRIVMSG #ch :buy qaspamword now")
	v.send("BATCH -a")
	v.expect("*** Your message to #ch was blocked: No spam")
	v.send("BATCH +b " + multilineCap + " #ch")
	v.send("@batch=b PRIVMSG #ch :buy qaspam")
	v.send("@batch=b;" + concatTag + " PRIVMSG #ch :word now")
	v.send("BATCH -b")
	v.expect("*** Your message to #ch was blocked: No spam")
	v.send("BATCH +c " + multilineCap + " #ch")
	v.send("@batch=c PRIVMSG #ch :all fine")
	v.send("@batch=c PRIVMSG #ch :really")
	v.send("BATCH -c")
	a.expect("PRIVMSG #ch :all fine")
	a.quiet("qaspam")
}

// A batch counts once for flood control: 20 lines arrive at once even
// with a slow flood rate.
func TestMultilineFloodOnce(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		draftsOn(c)
		c.Limits.FloodBurst, c.Limits.FloodRate = 5, 2
	})
	v := capLogin(t, s, "Vic", "batch", multilineCap)
	a := login(t, s, "Ann")
	joinAll("#ch", v, a)
	start := time.Now()
	v.send("BATCH +a " + multilineCap + " #ch")
	for i := 0; i < 20; i++ {
		v.send("@batch=a PRIVMSG #ch :line " + strconv.Itoa(i))
	}
	v.send("BATCH -a")
	a.expect("PRIVMSG #ch :line 19")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("batch took %v", d)
	}
}
