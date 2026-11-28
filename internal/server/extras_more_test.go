package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// +u: the audience sees the ops only, the ops see everyone.
func TestAuditorium(t *testing.T) {
	s := testServer(t, nil)
	op := login(t, s, "op")
	op.send("JOIN #stage")
	op.expect(" 366 op #stage ")
	op.send("MODE #stage +u")
	op.expect("MODE #stage +u")
	ann := login(t, s, "ann")
	ann.send("JOIN #stage")
	if l := ann.expect(" 353 ann = #stage "); !strings.Contains(l, "@op") {
		t.Fatalf("ann's names: %q", l)
	}
	ann.expect(" 366 ann #stage ")
	op.expect(":ann!~ann@127.0.0.1 JOIN #stage")
	bob := login(t, s, "bob")
	bob.send("JOIN #stage")
	if l := bob.expect(" 353 bob = #stage "); strings.Contains(l, "ann") || !strings.Contains(l, "@op") || !strings.Contains(l, "bob") {
		t.Fatalf("bob's names: %q", l)
	}
	bob.expect(" 366 bob #stage ")
	op.expect(":bob!~bob@127.0.0.1 JOIN #stage")
	ann.quiet("JOIN")

	// What the audience says still reaches everyone.
	ann.send("PRIVMSG #stage :a question")
	bob.expect(":ann!~ann@127.0.0.1 PRIVMSG #stage :a question")
	op.expect("PRIVMSG #stage :a question")

	// WHO and WHOIS keep the secret too.
	bob.send("WHO #stage")
	bob.expect(" 352 bob #stage ")
	bob.expect(" 315 bob ")
	bob.send("WHOIS ann")
	if l := bob.expect(" 318 bob ann "); strings.Contains(l, "#stage") {
		t.Fatalf("WHOIS shows the channel: %q", l)
	}

	// A new op walks on stage, and sees the room.
	op.send("MODE #stage +o ann")
	bob.expect(":ann!~ann@127.0.0.1 JOIN #stage")
	bob.expect("MODE #stage +o ann")
	ann.expect("MODE #stage +o ann")
	if l := ann.expect(" 353 ann = #stage "); !strings.Contains(l, "bob") {
		t.Fatalf("the new op's names: %q", l)
	}
	ann.expect(" 366 ann #stage ")
	// And walks off again.
	op.send("MODE #stage -o ann")
	bob.expect("MODE #stage -o ann")
	bob.expect(":ann!~ann@127.0.0.1 PART #stage")
	ann.expect(" 366 ann #stage ")

	// Leaving and quitting: only the ops are told.
	ann.send("PART #stage")
	op.expect(":ann!~ann@127.0.0.1 PART #stage")
	cat := login(t, s, "cat")
	cat.send("JOIN #stage")
	cat.expect(" 366 cat #stage ")
	op.expect("JOIN #stage")
	cat.send("QUIT :bye")
	op.expect(":cat!~cat@127.0.0.1 QUIT ")
	bob.quiet("cat")

	// Without +u everyone gets the full list again.
	op.send("MODE #stage -u")
	bob.expect("MODE #stage -u")
	bob.expect(" 366 bob #stage ")
}

func TestCensoredPrivateMessages(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Censor.Words = []string{"darn"} })
	a := login(t, s, "ann")
	b := login(t, s, "bob")
	b.send("MODE bob +G")
	b.expect("MODE bob +G")
	a.send("PRIVMSG bob :well darn it")
	b.expect("PRIVMSG bob :well <censored> it")
	b.send("PRIVMSG ann :well darn it")
	a.expect("PRIVMSG ann :well darn it")
}

func TestRestrictMsgAndHiddenBans(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.onLoop(func() {
		s.cfg.Limits.RestrictMsg = true
		s.cfg.Channels.HideBans = true
	})
	a := login(t, s, "ann")
	b := login(t, s, "bob")
	a.send("PRIVMSG bob :hi")
	a.expect(" 531 ann bob :You can only send private messages to IRC operators on this network")
	o := boss(t, s)
	a.send("PRIVMSG Boss :help me")
	o.expect("PRIVMSG Boss :help me")
	o.send("PRIVMSG ann :sure")
	a.expect("PRIVMSG ann :sure")
	a.send("PRIVMSG NickServ :HELP")
	a.expect("NOTICE ann :")

	b.send("JOIN #den")
	b.expect(" 366 bob #den ")
	b.send("MODE #den +b *!*@bad.example")
	b.expect("MODE #den +b")
	a.send("MODE #den b")
	a.expect(" 482 ann #den ")
	b.send("MODE #den b")
	b.expect(" 367 bob #den *!*@bad.example ")
	o.send("MODE #den b")
	o.expect(" 367 Boss #den *!*@bad.example ")
}

// [channels] hide_lists = "beI" hides those lists like hide_bans does
// +b; without b in it, everyone may read the bans again.
func TestHideLists(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.onLoop(func() { s.cfg.Channels.HideLists = "beI" })
	a := login(t, s, "ann")
	b := login(t, s, "bob")
	o := boss(t, s)
	b.send("JOIN #den")
	b.expect(" 366 bob #den ")
	b.send("MODE #den +beI *!*@bad.example *!*@good.example *!*@guest.example")
	b.expect("MODE #den +beI")
	for _, l := range []string{"b", "e", "I"} {
		a.send("MODE #den " + l)
		a.expect(" 482 ann #den ")
	}
	b.send("MODE #den e")
	b.expect(" 348 bob #den *!*@good.example ")
	o.send("MODE #den b")
	o.expect(" 367 Boss #den *!*@bad.example ")

	s.onLoop(func() { s.cfg.Channels.HideLists = "eI" })
	a.send("MODE #den b")
	a.expect(" 367 ann #den *!*@bad.example ")
	a.send("MODE #den e")
	a.expect(" 482 ann #den ")
}

func TestShowFileAndAbbreviations(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.ShowFiles = []config.ShowFile{
			{Command: "faq", Text: "Ask in #help.\nBe patient.", Title: "Questions"},
			{Command: "STAFF", Text: "For opers.", Opers: true},
		}
		c.Limits.Abbreviations = true
	})
	c := login(t, s, "ann")
	c.send("FAQ")
	c.expect(" 375 ann :- irc.test.local Questions -")
	c.expect(" 372 ann :- Ask in #help.")
	c.expect(" 372 ann :- Be patient.")
	c.expect(" 376 ann :End of /FAQ command.")
	c.send("STAFF")
	c.expect(" 481 ann ")
	c.send("LUS.")
	c.expect(" 251 ann ")
	c.send("WHO.")
	if l := c.expect(" 421 ann WHO. "); !strings.Contains(l, "WHOIS") || !strings.Contains(l, "WHOWAS") {
		t.Fatalf("ambiguous answer: %q", l)
	}
	c.send("NOSUCH.")
	c.expect(" 421 ann NOSUCH. :Unknown command")
	// A shunned person gets no answer from a text command either.
	s.onLoop(func() { s.clients[s.net.UserByNick("ann").UID].tempShun = true })
	c.send("FAQ")
	c.quiet(" 375 ")
	// Nor "Unknown command" or "Not enough parameters".
	c.send("NOSUCHTHING")
	c.quiet(" 421 ")
	c.send("WHOIS")
	c.quiet(" 461 ")
}

// A ~realmask that doesn't fit is refused, not cut short.
func TestLongRealmaskRefused(t *testing.T) {
	s := testServer(t, nil)
	c := login(t, s, "ann")
	c.send("JOIN #long")
	c.expect(" 366 ann #long ")
	long := "~realmask:*!*@" + strings.Repeat("h", 100) + "+*"
	c.send("MODE #long +b " + long)
	c.expect(" 696 ann #long b " + long + " :Ban mask is too long")
	fits := "~realmask:*!*@" + strings.Repeat("h", 80) + "+bot"
	c.send("MODE #long +b " + fits)
	c.expect(" MODE #long +b " + fits)
}

func TestAbbreviationsOffByDefault(t *testing.T) {
	s := testServer(t, nil)
	c := login(t, s, "ann")
	c.send("LUS.")
	c.expect(" 421 ann LUS. ")
}

func TestPingCookie(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Server.PingCookie = true })
	c := raw(t, s)
	c.nick = "ann"
	c.send("NICK ann")
	c.send("USER ann 0 * :Ann")
	l := c.expect("PING :")
	cookie := strings.TrimSpace(l[strings.Index(l, "PING :")+6:])
	c.send("PONG :wrong")
	c.quiet(" 001 ")
	c.send("PONG :" + cookie)
	c.expect(" 001 ann ")
}

func TestWebRedirect(t *testing.T) {
	// With lookups on, the connect notices would come before the answer
	// and no browser could read it: they wait for the first line.
	s := testServer(t, func(c *config.Config) {
		c.Server.WebRedirect = "https://chat.example/"
		c.Server.IdentLookup = true
	})
	n := raw(t, s)
	n.nick = "ann"
	n.send("NICK ann")
	n.expect("Checking Ident")
	c := raw(t, s)
	c.send("GET / HTTP/1.1")
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	all, _ := io.ReadAll(c.r)
	got := string(all)
	at := strings.Index(got, "HTTP/1.1 301 Moved Permanently\r\nLocation: https://chat.example/\r\n")
	if at < 0 || !strings.Contains(got[at:], "\r\n\r\n") {
		t.Fatalf("no redirect in %q", got)
	}
	if at != 0 {
		t.Fatalf("something came before the redirect: %q", got[:at])
	}
}

func TestExtJWT(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.ExtJWT.Secret = "one"
		c.ExtJWT.Services = []config.ExtJWTService{{Name: "calls", Secret: "two"}}
	})
	c := login(t, s, "ann")
	c.send("JOIN #room")
	c.expect(" 366 ann #room ")
	claims := func(want, secret string) map[string]any {
		t.Helper()
		token, line := "", ""
		for more := true; more; {
			line = c.expect(want)
			f := strings.Fields(strings.TrimSpace(line))
			more = len(f) == 6 && f[4] == "*" // a long token comes in pieces
			token += f[len(f)-1]
		}
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Fatalf("not a token: %q", line)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(parts[0] + "." + parts[1]))
		if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
			t.Fatalf("wrong signature on %q", line)
		}
		body, _ := base64.RawURLEncoding.DecodeString(parts[1])
		out := map[string]any{}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	c.send("EXTJWT *")
	got := claims(" EXTJWT * * ", "one")
	if got["sub"] != "ann" || got["iss"] != "irc.test.local" || got["channel"] != nil {
		t.Fatalf("claims: %v", got)
	}
	if exp := int64(got["exp"].(float64)); exp < time.Now().Unix() || exp > time.Now().Unix()+31 {
		t.Fatalf("exp: %v", got["exp"])
	}
	c.send("EXTJWT #room calls")
	got = claims(" EXTJWT #room calls ", "two")
	if got["channel"] != "#room" || got["joined"].(float64) == 0 || len(got["cmodes"].([]any)) != 1 || got["cmodes"].([]any)[0] != "o" {
		t.Fatalf("channel claims: %v", got)
	}
	c.send("EXTJWT * nosuch")
	c.expect("FAIL EXTJWT NO_SUCH_SERVICE nosuch ")
	c.send("EXTJWT #nowhere")
	c.expect("FAIL EXTJWT NO_SUCH_CHANNEL #nowhere ")
}

func TestExtJWTOff(t *testing.T) {
	s := testServer(t, nil)
	c := login(t, s, "ann")
	c.send("EXTJWT *")
	c.expect(" 421 ann EXTJWT ")
}

func TestMoreSmallOptionsCheck(t *testing.T) {
	for name, tweak := range map[string]func(*config.Config){
		"redirect":      func(c *config.Config) { c.Server.WebRedirect = "chat.example" },
		"showfile name": func(c *config.Config) { c.ShowFiles = []config.ShowFile{{Command: "two words", Text: "x"}} },
		"showfile text": func(c *config.Config) { c.ShowFiles = []config.ShowFile{{Command: "FAQ"}} },
		"showfile dup": func(c *config.Config) {
			c.ShowFiles = []config.ShowFile{{Command: "FAQ", Text: "x"}, {Command: "faq", Text: "y"}}
		},
		"jwt method":  func(c *config.Config) { c.ExtJWT.Method = "RS256" },
		"jwt service": func(c *config.Config) { c.ExtJWT.Services = []config.ExtJWTService{{Name: "calls"}} },
	} {
		cfg := &config.Config{Services: config.Services{Registration: "none"}}
		tweak(cfg)
		if err := cfg.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The small [server] options apply on a rehash, without a restart.
func TestSmallServerOptionsRehash(t *testing.T) {
	s, path, _ := fileServer(t, "")
	o := boss(t, s)
	conf, _ := os.ReadFile(path)
	text := strings.Replace(string(conf), "[server]\n", "[server]\nping_cookie = true\nconnect_modes = \"+R\"\nhide_servers = true\n", 1)
	if text == string(conf) {
		t.Fatal("no [server] line in the test config")
	}
	os.WriteFile(path, []byte(text), 0o600)
	o.send("REHASH")
	if l := o.expect("Rehash done:"); strings.Contains(l, "restart") {
		t.Fatalf("a restart is asked for: %q", l)
	}
	c := raw(t, s)
	c.nick = "ann"
	c.send("NICK ann")
	c.send("USER ann 0 * :Ann")
	l := c.expect("PING :")
	c.send("PONG :" + strings.TrimSpace(l[strings.Index(l, "PING :")+6:]))
	c.expect(" 001 ann ")
	c.send("MODE ann")
	if l := c.expect(" 221 ann "); !strings.Contains(l, "R") {
		t.Fatalf("user modes: %q", l)
	}
}
