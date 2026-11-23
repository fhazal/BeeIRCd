package server

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// capLogin registers nick after asking for caps.
func capLogin(t *testing.T, s *Server, nick string, caps ...string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("CAP LS 302")
	c.expect("CAP * LS")
	if len(caps) > 0 {
		c.send("CAP REQ :" + strings.Join(caps, " "))
		c.expect("CAP * ACK :" + strings.Join(caps, " "))
	}
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick + " Real")
	c.send("CAP END")
	c.expect(" MODE " + nick + " +i")
	return c
}

func joinAll(name string, cs ...*tc) {
	for _, c := range cs {
		c.send("JOIN " + name)
		c.expect(" 366 " + c.nick + " " + name + " ")
	}
}

func TestCapNegotiation(t *testing.T) {
	s := testServer(t, nil)
	c := raw(t, s)
	c.send("CAP LS 302")
	ls := c.expect("CAP * LS :")
	for _, want := range capabilities {
		if !strings.Contains(ls, want) {
			t.Errorf("CAP LS lacks %s: %s", want, ls)
		}
	}
	c.send("CAP REQ :server-time bogus")
	c.expect("CAP * NAK :server-time bogus")
	c.send("CAP REQ :server-time multi-prefix")
	c.expect("CAP * ACK :server-time multi-prefix")
	c.send("CAP REQ :-multi-prefix")
	c.expect("CAP * ACK :-multi-prefix")
	c.send("CAP LIST")
	c.expect("CAP * LIST :cap-notify server-time")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.quiet(" 001 ")
	c.send("CAP END")
	c.expect(" 001 Bee ")
}

func TestTypingAndReplyTags(t *testing.T) {
	s := testServer(t, nil)
	bee := capLogin(t, s, "Bee", "message-tags")
	ember := capLogin(t, s, "ember", "message-tags")
	nova := login(t, s, "nova")
	joinAll("#a", bee, ember, nova)

	bee.send("@+typing=active TAGMSG #a")
	l := ember.expect("TAGMSG #a")
	if !strings.HasPrefix(l, "@") || !strings.Contains(l, "+typing=active") || !strings.Contains(l, "msgid=") {
		t.Errorf("TAGMSG lost its tags: %s", l)
	}
	nova.quiet("TAGMSG")

	bee.send("@+draft/reply=abc;secret=no PRIVMSG #a :replying")
	l = ember.expect("PRIVMSG #a :replying")
	if !strings.Contains(l, "+draft/reply=abc") || strings.Contains(l, "secret=no") {
		t.Errorf("client tags wrong (only + tags pass): %s", l)
	}
	l = nova.expect("PRIVMSG #a :replying")
	if strings.HasPrefix(l, "@") {
		t.Errorf("a client without message-tags got tags: %s", l)
	}

	bee.send("@+typing=paused TAGMSG ember")
	ember.expect(":Bee!~bee@127.0.0.1 TAGMSG ember")
}

func TestServerTimeAndEcho(t *testing.T) {
	s := testServer(t, nil)
	bee := capLogin(t, s, "Bee", "server-time", "echo-message", "message-tags")
	ember := login(t, s, "ember")
	joinAll("#a", bee, ember)
	bee.send("PRIVMSG #a :hello")
	l := bee.expect("PRIVMSG #a :hello")
	if !regexp.MustCompile(`^@\S*time=20\d\d-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z`).MatchString(l) || !strings.Contains(l, "msgid=") {
		t.Errorf("echo without time or msgid: %s", l)
	}
	ember.expect(":Bee!~bee@127.0.0.1 PRIVMSG #a :hello")
	bee.send("@+typing=active TAGMSG #a")
	bee.expect("TAGMSG #a") // echo-message covers TAGMSG too
}

func TestLabeledResponse(t *testing.T) {
	s := testServer(t, nil)
	c := capLogin(t, s, "Bee", "labeled-response", "batch")
	c.send("@label=one PING :x")
	c.expect("@label=one :irc.test.local PONG irc.test.local :x")
	c.send("@label=two PONG :x")
	c.expect("@label=two :irc.test.local ACK")
	c.send("@label=three WHOIS Bee")
	start := c.expect("BATCH +")
	m := regexp.MustCompile(`^@label=three :irc\.test\.local BATCH \+(\S+) labeled-response$`).FindStringSubmatch(start)
	if m == nil {
		t.Fatalf("bad batch start: %s", start)
	}
	l := c.expect(" 311 Bee Bee ")
	if !strings.HasPrefix(l, "@batch="+m[1]+" ") {
		t.Errorf("batch line without its batch tag: %s", l)
	}
	c.expect(":irc.test.local BATCH -" + m[1])
}

func TestNamesCaps(t *testing.T) {
	s := testServer(t, nil)
	bee := login(t, s, "Bee")
	ember := capLogin(t, s, "ember", "multi-prefix", "userhost-in-names")
	joinAll("#a", bee, ember)
	bee.send("MODE #a +v Bee")
	ember.expect("MODE #a +v Bee")
	ember.send("NAMES #a")
	expectNames(t, ember.expect(" 353 ember = #a :"), "@+Bee!~bee@127.0.0.1 ember!~ember@127.0.0.1")
	bee.send("NAMES #a")
	expectNames(t, bee.expect(" 353 Bee = #a :"), "@Bee ember")
	ember.send("WHO #a")
	ember.expect(" 352 ember #a ~bee 127.0.0.1 irc.test.local Bee H@+ ")
}

func TestJoinAwayInviteNotify(t *testing.T) {
	s := testServer(t, nil)
	bee := login(t, s, "Bee")
	ember := capLogin(t, s, "ember", "extended-join", "away-notify", "invite-notify")
	joinAll("#a", bee, ember)
	bee.send("MODE #a +o ember")
	ember.expect("MODE #a +o ember")

	nova := login(t, s, "nova")
	nova.send("AWAY :brb")
	nova.expect(" 306 ")
	joinAll("#a", nova)
	ember.expect(":nova!~nova@127.0.0.1 JOIN #a * :nova Real")
	ember.expect(":nova!~nova@127.0.0.1 AWAY :brb")
	bee.expect(":nova!~nova@127.0.0.1 JOIN #a")

	nova.send("AWAY")
	ember.expect(":nova!~nova@127.0.0.1 AWAY")
	bee.quiet("AWAY")

	x := login(t, s, "x")
	bee.send("INVITE x #a")
	x.expect(":Bee!~bee@127.0.0.1 INVITE x #a")
	ember.expect(":Bee!~bee@127.0.0.1 INVITE x #a")
	nova.quiet("INVITE")
}

func TestCloaks(t *testing.T) {
	on := true
	s := testServer(t, func(c *config.Config) { c.Cloak.Enabled = &on })
	bee := login(t, s, "Bee")
	l := bee.expect(" 396 Bee beenet-")
	cloaked := regexp.MustCompile(`beenet-[0-9a-f]{8}\.[0-9a-f]{6}\.ip`).FindString(l)
	if cloaked == "" {
		t.Fatalf("no cloak in %s", l)
	}
	ember := capLogin(t, s, "ember", "chghost")
	joinAll("#a", bee, ember)
	ember.send("WHOIS Bee")
	ember.expect(" 311 ember Bee ~bee " + cloaked + " * :Bee Real")
	ember.quiet(" 378 ")
	bee.send("WHOIS Bee")
	bee.expect(" 378 Bee Bee :is connecting from *@127.0.0.1 127.0.0.1")

	bee.send("MODE Bee -x")
	bee.expect(":Bee MODE Bee -x")
	bee.expect(" 396 Bee 127.0.0.1 :is now your displayed host")
	ember.expect(":Bee!~bee@" + cloaked + " CHGHOST ~bee 127.0.0.1")
	bee.send("MODE Bee +x")
	ember.expect(":Bee!~bee@127.0.0.1 CHGHOST ~bee " + cloaked)

	// A ban on the real IP still catches the cloaked user.
	nova := login(t, s, "nova")
	joinAll("#b", nova)
	nova.send("MODE #b +b *!*@127.0.0.1")
	nova.expect("MODE #b +b *!*@127.0.0.1")
	bee.send("JOIN #b")
	bee.expect(" 474 Bee #b :Cannot join channel (+b)")
}

func TestFloodIsSlowedNotKilled(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.Limits.FloodBurst = 5
		c.Limits.FloodRate = 20
	})
	c := login(t, s, "fast")
	time.Sleep(400 * time.Millisecond) // let the login lines drain from the budget
	start := time.Now()
	var b strings.Builder
	for i := 0; i < 25; i++ {
		b.WriteString("PING :n\r\n")
	}
	c.conn.Write([]byte(b.String()))
	for i := 0; i < 25; i++ {
		c.expect("PONG irc.test.local :n")
	}
	// 25 lines, 5 free, then 20 per second: about one second.
	if took := time.Since(start); took < 800*time.Millisecond || took > 3*time.Second {
		t.Errorf("25 lines took %v, want about 1s", took)
	}
	c.sync()
}

func TestClipKeepsTags(t *testing.T) {
	long := "@time=2026-11-11T00:00:00.000Z;msgid=x :n!u@h PRIVMSG #a :" + strings.Repeat("y", 600)
	got := clip(long)
	tags, rest, _ := strings.Cut(got, " ")
	if tags != "@time=2026-11-11T00:00:00.000Z;msgid=x" || len(rest) != 510 {
		t.Errorf("clip: tags %q, rest %d bytes", tags, len(rest))
	}
}

func TestNoImplicitNames(t *testing.T) {
	s := testServer(t, nil)
	c := raw(t, s)
	c.send("CAP REQ :draft/no-implicit-names")
	c.expect("CAP * ACK :draft/no-implicit-names")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("CAP END")
	c.expect(" 001 Bee ")
	c.send("JOIN #a")
	c.expect(":Bee!~bee@127.0.0.1 JOIN #a")
	c.quiet(" 353 Bee ")
	c.send("NAMES #a") // asked for: the list comes
	c.expect(" 353 Bee = #a :@Bee")
	c.expect(" 366 Bee #a ")
	other := login(t, s, "ember") // without the cap JOIN still lists names
	other.send("JOIN #a")
	other.expect(" 353 ember = #a :")
}

func TestExtendedMonitor(t *testing.T) {
	s := testServer(t, nil)
	c := raw(t, s)
	c.send("CAP REQ :extended-monitor away-notify setname")
	c.expect("CAP * ACK :extended-monitor away-notify setname")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("CAP END")
	c.expect(" 001 Bee ")
	plain := raw(t, s) // away-notify without extended-monitor
	plain.send("CAP REQ :away-notify")
	plain.expect("CAP * ACK :away-notify")
	plain.send("NICK nova")
	plain.send("USER nova 0 * :Nova")
	plain.send("CAP END")
	plain.expect(" 001 nova ")
	ember := login(t, s, "ember")
	c.send("MONITOR + ember")
	c.expect(" 730 Bee :ember!")
	plain.send("MONITOR + ember")
	plain.expect(" 730 nova :ember!")
	ember.send("AWAY :lunch")
	ember.expect(" 306 ")
	c.expect(":ember!~ember@127.0.0.1 AWAY :lunch")
	plain.quiet("AWAY :lunch")
	// Sharing a channel: one copy only.
	c.send("JOIN #a")
	c.expect(" 366 Bee #a ")
	ember.send("JOIN #a")
	c.expect("ember!~ember@127.0.0.1 JOIN #a")
	c.expect(":ember!~ember@127.0.0.1 AWAY :lunch") // away-notify on join
	ember.send("AWAY")
	ember.expect(" 305 ")
	c.expect(":ember!~ember@127.0.0.1 AWAY")
	c.quiet(":ember!~ember@127.0.0.1 AWAY")
}

func TestUTF8Only(t *testing.T) {
	off := testServer(t, nil)
	a, b := login(t, off, "Bee"), login(t, off, "ember")
	a.send("PRIVMSG ember :caf\xe9")
	b.expect("PRIVMSG ember :caf\xe9") // off by default: passed on as it is
	on := testServer(t, func(c *config.Config) { c.Limits.UTF8Only = true })
	x, y := login(t, on, "Bee"), login(t, on, "ember")
	x.send("VERSION")
	x.expect(" UTF8ONLY ")
	x.send("PRIVMSG ember :caf\xe9")
	x.expect(":irc.test.local FAIL PRIVMSG INVALID_UTF8 :Message rejected, your IRC software MUST use UTF-8 encoding on this network")
	y.quiet("caf")
	x.send("PRIVMSG ember :café")
	y.expect("PRIVMSG ember :café")
}

// A long server name, a long nick and the drafts: CAP LS without 302 still
// splits with "*" (CAP 3.1) and no line passes 512 bytes.
func TestCapLSLongNo302(t *testing.T) {
	s := testServer(t, func(cfg *config.Config) {
		cfg.Server.Name = strings.Repeat("h", 46) + ".bee.example.org"
		cfg.Server.IRCv3Drafts = true
		cfg.Limits.NickLen = 30
	})
	c := raw(t, s)
	nick := strings.Repeat("N", 30)
	c.send("NICK " + nick)
	c.send("CAP LS")
	var all []string
	for {
		l := c.expect(" CAP " + nick + " LS ")
		if len(l)+2 > 512 {
			t.Fatalf("CAP LS line is %d bytes: %s", len(l)+2, l)
		}
		i := strings.Index(l, " LS ")
		rest := l[i+4:]
		more := strings.HasPrefix(rest, "* ")
		all = append(all, strings.Fields(strings.TrimPrefix(strings.TrimPrefix(rest, "* "), ":"))...)
		if !more {
			break
		}
	}
	want := strings.Fields(s.capLS(false, ""))
	if len(strings.Join(want, " ")) < 420 {
		t.Fatalf("CAP LS is too short to need a split")
	}
	if strings.Join(all, " ") != strings.Join(want, " ") {
		t.Errorf("CAP LS lost caps:\n got %v\nwant %v", all, want)
	}
}
