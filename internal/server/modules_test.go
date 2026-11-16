package server

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/state"
)

// modServer starts a server with the given module files (name -> source)
// in its modules folder; load lists the ones loaded at start.
func modServer(t *testing.T, files map[string]string, load []string, tweak func(*config.Config)) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".lua"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := testServer(t, func(c *config.Config) {
		c.Modules.Dir = dir
		c.Modules.Load = load
		if tweak != nil {
			tweak(c)
		}
	})
	return s, dir
}

const greetModule = `
module { name = "greet", version = "1.0", author = "Bee", about = "Says hello" }

on("join", function(e)
  if e.channel.name == "#lobby" then
    notice(e.user, (config.text or "Welcome") .. ", " .. e.user.nick .. " (" .. e.channel.count .. " here)")
  end
end)

command("HUG", { params = 1, help = "HUG <nick>: sends a hug" }, function(user, args)
  local t = find_user(args[1])
  if not t then return fail(user, "No such nick: " .. args[1]) end
  notice(t, user.nick .. " hugs you")
end)

command("STAFFONLY", { opers = true }, function(user) notice(user, "hi staff") end)
`

func TestModuleHooksAndCommands(t *testing.T) {
	s, _ := modServer(t, map[string]string{"greet": greetModule}, []string{"greet"}, func(c *config.Config) {
		c.Module = map[string]map[string]any{"greet": {"text": "Selamat datang"}}
	})
	a := login(t, s, "ann")
	a.send("JOIN #lobby")
	a.expect("NOTICE ann :Selamat datang, ann (1 here)")
	b := login(t, s, "bob")
	b.send("HUG ann")
	a.expect("NOTICE ann :bob hugs you")
	b.send("HUG nobody")
	b.expect("NOTICE bob :*** No such nick: nobody")
	b.send("HUG")
	b.expect(" 461 bob HUG ")
	b.send("STAFFONLY")
	b.expect(" 481 bob ")
	b.send("HELP HUG")
	b.expect(" 704 bob HUG :HUG (module greet)")
	b.expect(" 705 bob HUG :HUG <nick>: sends a hug")
	b.send("NOSUCHCMD")
	b.expect(" 421 bob NOSUCHCMD ")
}

const eventsModule = `
module { name = "events", version = "1.0" }
local function say(text) snotice(text) end
on("load", function() say("loaded") end)
on("connect", function(e) say("connect " .. e.user.nick .. " tls=" .. tostring(e.user.tls)) end)
on("quit", function(e) say("quit " .. e.user.nick .. " (" .. e.reason .. ")") end)
on("nick", function(e) say("nick " .. e.old .. " -> " .. e.user.nick) end)
on("part", function(e) say("part " .. e.user.nick .. " " .. e.channel.name .. " [" .. e.reason .. "]") end)
on("kick", function(e) say("kick " .. e.user.nick .. " by " .. e.source.nick) end)
on("topic", function(e) say("topic " .. e.channel.name .. " = " .. e.text) end)
on("channel_mode", function(e) say("cmode " .. e.channel.name .. " " .. e.modes) end)
on("user_mode", function(e) say("umode " .. e.user.nick .. " " .. e.modes) end)
on("away", function(e) say("away " .. e.user.nick .. " " .. e.text) end)
on("back", function(e) say("back " .. e.user.nick) end)
on("message", function(e)
  if e.channel then say("msg " .. e.user.nick .. " " .. e.channel.name .. " " .. e.text)
  else say("pm " .. e.user.nick .. " " .. e.target.nick .. " " .. e.text) end
end)
on("rehash", function() say("rehash text=" .. tostring(config.text)) end)
on("unload", function() say("bye") end)
`

func TestModuleEvents(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "events.lua"), []byte(eventsModule), 0o600)
	s.onLoop(func() { s.cfg.Modules.Dir = dir })
	o := boss(t, s)
	o.send("MODULE LOAD events")
	o.expect("[events] loaded")
	o.expect("NOTICE Boss :events is loaded.")
	a := login(t, s, "ann")
	o.expect("[events] connect ann tls=false")
	a.send("JOIN #x")
	a.expect(" 366 ann #x ")
	a.send("TOPIC #x :hello there")
	o.expect("[events] topic #x = hello there")
	a.send("MODE #x +m")
	o.expect("[events] cmode #x +m")
	b := login(t, s, "bob")
	b.send("JOIN #x")
	b.expect(" 366 bob #x ")
	a.send("PRIVMSG #x :to all")
	o.expect("[events] msg ann #x to all")
	a.send("PRIVMSG bob :just you")
	o.expect("[events] pm ann bob just you")
	a.send("KICK #x bob :out")
	o.expect("[events] kick bob by ann")
	a.send("AWAY :lunch")
	o.expect("[events] away ann lunch")
	a.send("AWAY")
	o.expect("[events] back ann")
	a.send("MODE ann +w")
	o.expect("[events] umode ann +w")
	a.send("NICK anna")
	o.expect("[events] nick ann -> anna")
	a.send("PART #x :later")
	o.expect("[events] part anna #x [later]")
	a.send("QUIT :gone")
	o.expect("[events] quit anna (Quit: gone)")
	o.send("MODULE INFO events")
	o.expect("NOTICE Boss :Hooks: away, back, channel_mode, connect, kick, load, message, nick, part, quit, rehash, topic, unload, user_mode")
	o.send("MODULE LIST")
	o.expect("NOTICE Boss :events 1.0: (no description)")
	o.send("MODULE UNLOAD events")
	o.expect("[events] bye")
	o.expect("NOTICE Boss :events is unloaded.")
	b.send("PRIVMSG Boss :still there?")
	o.expect("PRIVMSG Boss :still there?")
	o.quiet("[events]")
}

const filterModule = `
module { name = "filter", version = "1.0" }
on("check_message", function(e)
  if e.text:find("badword") then return "That word is not welcome here" end
  if e.text:find("shh") then return false end
  if e.text:find("teh") then e.text = e.text:gsub("teh", "the") end
end)
`

func TestModuleCheckMessage(t *testing.T) {
	s, _ := modServer(t, map[string]string{"filter": filterModule}, []string{"filter"}, nil)
	a := login(t, s, "ann")
	b := login(t, s, "bob")
	a.send("JOIN #x")
	a.expect(" 366 ann #x ")
	b.send("JOIN #x")
	b.expect(" 366 bob #x ")
	a.expect("JOIN #x")
	b.send("PRIVMSG #x :a badword here")
	b.expect(" 404 bob #x :That word is not welcome here")
	b.send("PRIVMSG ann :a badword here")
	b.expect(" 531 bob ann :That word is not welcome here")
	b.send("PRIVMSG #x :shh secret")
	b.send("PRIVMSG #x :teh cat")
	a.expect(":bob!~bob@127.0.0.1 PRIVMSG #x :the cat")
	b.send("PRIVMSG ann :teh dog")
	a.expect("PRIVMSG ann :the dog")
}

const troubleModule = `
module { name = "trouble", version = "1.0" }
command("SPIN", {}, function() while true do end end)
command("OOPS", {}, function() local x = nil; return x.field end)
command("BIG", {}, function() return string.rep("x", 100000000) end)
command("FILES", {}, function(user) notice(user, "io=" .. tostring(io) .. " os=" .. tostring(os) .. " require=" .. tostring(require) .. " load=" .. tostring(loadstring)) end)
command("ZAP", {}, function(user, args) kill(args[1], "zapped") end)
`

func TestModuleSandboxAndLimits(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "trouble.lua"), []byte(troubleModule), 0o600)
	s.onLoop(func() {
		s.cfg.Modules.Dir = dir
		s.cfg.Modules.TimeLimit = config.Duration{Duration: 30 * time.Millisecond}
	})
	o := boss(t, s)
	o.send("MODULE LOAD trouble")
	o.expect("trouble is loaded.")
	a := login(t, s, "ann")
	a.send("FILES")
	a.expect("NOTICE ann :io=nil os=nil require=nil load=nil")
	a.send("ZAP Boss")
	o.expect(`Module trouble: trouble.lua:7: this needs the right "kill": add allow = ["kill"] to [module.trouble] in beeircd.conf`)
	start := time.Now()
	a.send("SPIN")
	o.expect("Module trouble: a call ran longer than 30ms and was stopped")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the endless loop held the server for %s", d)
	}
	// The server still answers, and the third error switches the module off.
	a.send("PING :alive")
	a.expect("PONG irc.test.local :alive")
	a.send("BIG")
	o.expect("Module trouble: trouble.lua:5: string.rep: the result would be over")
	o.expect("Module trouble was unloaded: 3 errors within a minute")
	a.send("OOPS")
	a.expect(" 421 ann OOPS ")
}

func TestModuleRightsAndRehash(t *testing.T) {
	s, path, _ := fileServer(t, "")
	dir := filepath.Join(filepath.Dir(path), "modules") // the default folder: next to beeircd.conf
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "trouble.lua"), []byte(troubleModule), 0o600)
	os.WriteFile(filepath.Join(dir, "events.lua"), []byte(eventsModule), 0o600)
	o := boss(t, s)
	conf, _ := os.ReadFile(path)
	add := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, append(append([]byte{}, conf...), []byte(text)...), 0o600); err != nil {
			t.Fatal(err)
		}
		o.send("REHASH")
	}
	add("\n[modules]\nload = [\"trouble\", \"events\"]\n\n[module.trouble]\nallow = [\"kill\"]\n\n[module.events]\ntext = \"one\"\n")
	o.expect("[events] loaded")
	a := login(t, s, "ann")
	b := login(t, s, "bob")
	a.send("ZAP bob")
	b.expect("zapped")
	add("\n[modules]\nload = [\"events\"]\n\n[module.events]\ntext = \"two\"\n")
	o.expect("[events] rehash text=two")
	a.send("ZAP Boss")
	a.expect(" 421 ann ZAP ")
	s.onLoop(func() {
		if s.mods.byName["trouble"] != nil || s.mods.byName["events"] == nil {
			t.Errorf("after the rehash: %v", s.mods.order)
		}
	})
}

func TestModuleLoadProblems(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	for name, src := range map[string]string{
		"syntax":  "module { name = \"syntax\", version = \"1\" }\non(\"join\", function(e)\n",
		"noname":  "on(\"join\", function(e) end)\n",
		"other":   "module { name = \"else\", version = \"1\" }\n",
		"badhook": "module { name = \"badhook\", version = \"1\" }\non(\"joined\", function(e) end)\n",
		"future":  "module { name = \"future\", version = \"1\", api = 99 }\n",
		"clash":   "module { name = \"clash\", version = \"1\" }\ncommand(\"JOIN\", {}, function() end)\n",
	} {
		os.WriteFile(filepath.Join(dir, name+".lua"), []byte(src), 0o600)
	}
	s.onLoop(func() { s.cfg.Modules.Dir = dir })
	o := boss(t, s)
	for name, want := range map[string]string{
		"syntax":   "syntax not loaded: syntax.lua",
		"noname":   "noname not loaded: it has no module {",
		"other":    `other not loaded: it calls itself "else"`,
		"badhook":  "there is no such hook",
		"future":   "it needs module interface 99",
		"clash":    "BeeIRCd has a command of that name itself",
		"missing":  "missing not loaded: can't read",
		"../etc/x": "is not a module name",
	} {
		o.send("MODULE LOAD " + name)
		o.expect(want)
	}
	s.onLoop(func() {
		if len(s.mods.order) != 0 || len(s.mods.cmds) != 0 {
			t.Errorf("something stayed loaded: %v %v", s.mods.order, s.mods.cmds)
		}
	})
	u := login(t, s, "ann")
	u.send("MODULE LIST")
	u.expect(" 481 ann ")
}

const timerModule = `
module { name = "timer", version = "1.0" }
local n = 0
every(1, function() n = n + 1; snotice("beat " .. n) end)
after(1, function() snotice("once") end)
`

func TestModuleTimers(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "timer.lua"), []byte(timerModule), 0o600)
	s.onLoop(func() { s.cfg.Modules.Dir = dir })
	o := boss(t, s)
	o.send("MODULE LOAD timer")
	o.expect("timer is loaded.")
	later := time.Now().Add(1100 * time.Millisecond)
	s.onLoop(func() { s.modTick(later) })
	o.expect("[timer] beat 1")
	o.expect("[timer] once")
	s.onLoop(func() { s.modTick(later.Add(1100 * time.Millisecond)) })
	if l := o.expect("[timer] "); !strings.Contains(l, "beat 2") {
		t.Fatalf("second round: %q", l)
	}
}

// BenchmarkModuleCheckMessage is what one check_message hook costs per
// channel message.
func BenchmarkModuleCheckMessage(b *testing.B) {
	for _, mode := range []string{"no module", "one module"} {
		b.Run(mode, func(b *testing.B) {
			dir := b.TempDir()
			os.WriteFile(filepath.Join(dir, "filter.lua"), []byte(filterModule), 0o600)
			cfg := &config.Config{}
			cfg.Modules.Dir = dir
			s := &Server{cfg: cfg, net: state.NewNetwork("0AA"), clients: map[string]*Client{},
				mods: &modules{byName: map[string]*module{}, cmds: map[string]*modCommand{}, hooked: map[string]int{}}}
			s.log = log.New(io.Discard, "", 0)
			if mode == "one module" {
				if err := s.modLoad("filter", true); err != nil {
					b.Fatal(err)
				}
			}
			u := &state.User{UID: "0AAAAAAAB", Nick: "ann", Ident: "ann", Host: "example.org", Modes: map[byte]bool{}}
			ch := &state.Channel{Name: "#x", Modes: map[byte]bool{}, Members: map[string]*state.Member{}}
			c := &Client{user: u}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.modCheckMessage(c, ch, nil, "PRIVMSG", "hello there, how is everyone doing today")
			}
		})
	}
}
