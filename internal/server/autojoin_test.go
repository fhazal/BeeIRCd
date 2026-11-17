package server

import (
	"os"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// auto_join on connect, auto_join_skip by security group, and oper_join
// plus oper_join_flag at /oper.
func TestAutoJoin(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.onLoop(func() {
		s.cfg.Server.AutoJoin = []string{"#chat", "#help"}
		s.cfg.Server.OperJoin = []string{"#opers"}
		s.cfg.Server.OperJoinFlag = map[string][]string{"s": {"#services", "#opers"}, "h": {"#helpers"}}
	})
	ann := login(t, s, "ann")
	ann.expect(":ann!")
	ann.expect(" 366 ann #chat ")
	ann.expect(" 366 ann #help ")

	// A group in auto_join_skip is left alone.
	s.onLoop(func() {
		s.cfg.SecurityGroups = []config.SecurityGroup{{Name: "lan", GroupMatch: config.GroupMatch{IP: []string{"127.0.0.0/8", "::1/128"}}}}
		s.cfg.Server.AutoJoinSkip = []string{"lan"}
	})
	bob := login(t, s, "bob")
	bob.send("PART #chat")
	bob.expect(" 442 bob #chat ") // not on it

	// /oper: oper_join, then each flag's channels, each once.
	s.onLoop(func() { s.cfg.Server.AutoJoinSkip = nil })
	b := boss(t, s)
	b.expect("JOIN #opers")
	b.expect("JOIN #helpers")
	b.expect("JOIN #services")
	s.onLoop(func() {
		if got := strings.Join(s.operJoins("abcghks"), ","); got != "#opers,#helpers,#services" {
			t.Errorf("netadmin joins %s", got)
		}
		if got := strings.Join(s.operJoins("h"), ","); got != "#opers,#helpers" {
			t.Errorf("helper joins %s", got)
		}
	})
}

// A banned channel in auto_join is a normal refused JOIN, not a forced one.
func TestAutoJoinKeepsBans(t *testing.T) {
	s, _, _ := fileServer(t, "")
	op := login(t, s, "op")
	op.send("JOIN #chat")
	op.expect(" 366 op #chat ")
	op.send("MODE #chat +b carl!*@*")
	op.expect("MODE #chat +b carl!*@*")
	s.onLoop(func() { s.cfg.Server.AutoJoin = []string{"#chat"} })
	c := login(t, s, "carl")
	c.expect(" 474 carl #chat ")
}

func TestAutoJoinConfigCheck(t *testing.T) {
	for name, tweak := range map[string]func(*config.Config){
		"auto_join no #":      func(c *config.Config) { c.Server.AutoJoin = []string{"chat"} },
		"auto_join comma":     func(c *config.Config) { c.Server.AutoJoin = []string{"#a,#b"} },
		"auto_join_skip":      func(c *config.Config) { c.Server.AutoJoinSkip = []string{"nobody-here"} },
		"oper_join_flag flag": func(c *config.Config) { c.Server.OperJoinFlag = map[string][]string{"z": {"#x"}} },
		"oper_join_flag two":  func(c *config.Config) { c.Server.OperJoinFlag = map[string][]string{"sk": {"#x"}} },
		"oper_join_flag chan": func(c *config.Config) { c.Server.OperJoinFlag = map[string][]string{"s": {"x"}} },
	} {
		cfg := &config.Config{Services: config.Services{Registration: "none"}}
		tweak(cfg)
		if err := cfg.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// AdminServ SET writes the lists to beeircd.conf, they load back, and
// UNDO puts them back.
func TestAdminServAutoJoin(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("AS SET auto-join #chat, #help #chat")
	b.expect(asLine("Boss", "auto-join is now #chat,#help (was off)."))
	b.send("AS SET auto-join chat")
	b.expect(asLine("Boss", `auto-join: "chat" is not a channel name. Write channels like #chat,#help, or OFF.`))
	b.send("AS SET auto-join-skip nobody-here")
	b.expect(asLine("Boss", `auto-join-skip: there is no security group "nobody-here".`))
	b.send("AS SET auto-join-skip unknown-users")
	b.expect("auto-join-skip is now unknown-users (was off)")
	b.send("AS SET oper-join #opers")
	b.expect("oper-join is now #opers (was off)")
	b.send("AS SET oper-join-flag s:#services k:#kills s:#more")
	b.expect("oper-join-flag is now k:#kills s:#services,#more (was off)")
	b.send("AS SET oper-join-flag z:#x")
	b.expect(asLine("Boss", "oper-join-flag: write it like s:#services k:#kills (flags abcghks), or OFF."))

	raw, _ := os.ReadFile(path)
	for _, want := range []string{`auto_join = ["#chat", "#help"]`, `auto_join_skip = ["unknown-users"]`, `oper_join = ["#opers"]`,
		`oper_join_flag = { k = ["#kills"], s = ["#services", "#more"] }`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lacks %q:\n%s", want, raw)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Server.OperJoinFlag["s"], ",") != "#services,#more" || len(cfg.Server.AutoJoin) != 2 {
		t.Errorf("loaded back: %+v", cfg.Server)
	}

	// The joins are live at once.
	c := login(t, s, "dan")
	c.send("PART #chat")
	c.expect(" 403 dan #chat ") // unknown-users skipped it, so nobody made it
	b.send("AS SET auto-join-skip off")
	b.expect("auto-join-skip is now off (was unknown-users)")
	e := login(t, s, "eve")
	e.expect(" 366 eve #chat ")

	// UNDO of the flag list writes the old value back.
	b.send("AS SET oper-join-flag off")
	b.expect("oper-join-flag is now off (was k:#kills s:#services,#more)")
	b.send("AS HISTORY 1")
	l := b.expect("oper-join-flag")
	n := strings.Fields(strings.SplitN(l, " :", 2)[1])[0]
	b.send("AS UNDO " + n)
	b.expect("oper-join-flag")
	s.onLoop(func() {
		if got := flagJoins(s.cfg); got != "k:#kills s:#services,#more" {
			t.Errorf("after undo: %s", got)
		}
	})
}
