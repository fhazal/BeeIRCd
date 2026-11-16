package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// +U: what +m stops reaches the ops and nobody else.
func TestOpModerated(t *testing.T) {
	s := testServer(t, nil)
	op := login(t, s, "op")
	op.send("JOIN #hall")
	op.expect(" 366 op #hall ")
	a := login(t, s, "ann")
	a.send("JOIN #hall")
	a.expect(" 366 ann #hall ")
	b := login(t, s, "bob")
	b.send("JOIN #hall")
	b.expect(" 366 bob #hall ")
	op.send("MODE #hall +m")
	a.expect("MODE #hall +m")
	a.send("PRIVMSG #hall :can anyone hear me")
	a.expect(" 404 ann #hall ")
	op.send("MODE #hall +U")
	a.expect("MODE #hall +U")
	b.expect("MODE #hall +U")
	a.send("PRIVMSG #hall :a question for the ops")
	op.expect(":ann!~ann@127.0.0.1 PRIVMSG @#hall :a question for the ops")
	a.quiet(" 404 ")
	b.quiet("a question for the ops")
}

// +W: newcomers wait before they can talk; voice and up do not.
func TestDelayMsg(t *testing.T) {
	s := testServer(t, nil)
	op := login(t, s, "op")
	op.send("JOIN #lobby")
	op.expect(" 366 op #lobby ")
	op.send("MODE #lobby +W 0")
	op.expect(" 696 op #lobby W 0 ")
	op.send("MODE #lobby +W 30")
	op.expect("MODE #lobby +W 30")
	a := login(t, s, "ann")
	a.send("JOIN #lobby")
	a.expect(" 366 ann #lobby ")
	a.send("PRIVMSG #lobby :hi")
	a.expect("more seconds before you can talk in this channel (+W)")
	op.send("PRIVMSG #lobby :ops talk at once")
	a.expect("PRIVMSG #lobby :ops talk at once")
	op.send("MODE #lobby +v ann")
	a.expect("MODE #lobby +v ann")
	a.send("PRIVMSG #lobby :hi again")
	op.expect("PRIVMSG #lobby :hi again")
	// Long enough ago: no wait.
	b := login(t, s, "bob")
	b.send("JOIN #lobby")
	b.expect(" 366 bob #lobby ")
	s.onLoop(func() {
		ch := s.net.Channel("#lobby")
		ch.Member(s.net.UserByNick("bob")).Joined = ch.Member(s.net.UserByNick("bob")).Joined.Add(-31e9)
	})
	b.send("PRIVMSG #lobby :waited")
	op.expect("PRIVMSG #lobby :waited")
}

func TestMotdsRulesAndQuote(t *testing.T) {
	quotes := filepath.Join(t.TempDir(), "quotes.txt")
	os.WriteFile(quotes, []byte("\nBees never sleep.\n\n"), 0o600)
	s := testServer(t, func(c *config.Config) {
		c.MOTD.RulesText = "Be kind.\nNo spam."
		c.MOTD.BotText = "Bots: set +B."
		c.MOTD.OperText = "Opers: read the handbook."
		c.MOTD.QuotesFile = quotes
	})
	c := raw(t, s)
	c.nick = "ann"
	c.send("NICK ann")
	c.send("USER ann 0 * :Ann")
	c.expect(" 376 ann ")
	c.expect("NOTICE ann :Bees never sleep.")
	c.send("RULES")
	c.expect(" 308 ann ")
	c.expect(" 232 ann :- Be kind.")
	c.expect(" 232 ann :- No spam.")
	c.expect(" 309 ann ")
	c.send("BOTMOTD")
	c.expect(" 372 ann :- Bots: set +B.")
	c.send("OPERMOTD")
	c.expect(" 481 ann ")
}

func TestRulesMissing(t *testing.T) {
	s := testServer(t, nil)
	c := login(t, s, "ann")
	c.send("RULES")
	c.expect(" 434 ann ")
}

func TestOperExtras(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.onLoop(func() {
		s.cfg.MOTD.OperText = "Opers: read the handbook."
		s.cfg.Server.OperModes = "+W"
		s.cfg.Server.OperJoin = []string{"#opers"}
		s.cfg.Server.HideServers = true
	})
	u := login(t, s, "ann")
	u.send("LINKS")
	u.expect(" 364 ann irc.test.local ")
	u.expect(" 365 ann ")
	u.send("MAP")
	if l := u.expect(" 006 ann "); !strings.Contains(l, "irc.test.local") {
		t.Fatalf("MAP with hidden servers: %q", l)
	}
	b := boss(t, s)
	b.expect(" 372 Boss :- Opers: read the handbook.")
	b.expect("MODE Boss +W")
	b.expect("JOIN #opers")
	b.send("OPERMOTD")
	b.expect(" 372 Boss :- Opers: read the handbook.")
}

func TestConnectModesAndDisabledCommands(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.Server.ConnectModes = "+R"
		c.Limits.DisabledCommands = []string{"knock"}
		c.Channels.Create = "registered"
	})
	c := login(t, s, "ann")
	c.send("MODE ann")
	if l := c.expect(" 221 ann "); !strings.Contains(l, "R") {
		t.Fatalf("user modes after connecting: %q", l)
	}
	c.send("KNOCK #secret")
	c.expect(" 421 ann KNOCK ")
	c.send("JOIN #new")
	c.expect("You need to be logged in to NickServ to create a new channel.")
	r := registered(t, s, "Bee")
	r.send("JOIN #new")
	r.expect(" 366 Bee #new ")
	c.send("JOIN #new")
	c.expect(" 366 ann #new ")
}

func TestSmallOptionsCheck(t *testing.T) {
	for name, tweak := range map[string]func(*config.Config){
		"create":        func(c *config.Config) { c.Channels.Create = "nobody" },
		"disable NICK":  func(c *config.Config) { c.Limits.DisabledCommands = []string{"nick"} },
		"connect +o":    func(c *config.Config) { c.Server.ConnectModes = "+o" },
		"modes no plus": func(c *config.Config) { c.Server.OperModes = "W" },
		"oper_join":     func(c *config.Config) { c.Server.OperJoin = []string{"opers"} },
	} {
		cfg := &config.Config{Services: config.Services{Registration: "none"}}
		tweak(cfg)
		if err := cfg.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
