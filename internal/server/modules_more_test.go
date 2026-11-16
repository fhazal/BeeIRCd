package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

const gateModule = `
module { name = "gate", version = "1.0" }
on("check_connect", function(e) if e.user.nick == "evil" then return "Not welcome" end end)
on("check_nick", function(e) if e.nick:find("admin") then return "That nick is kept for staff" end end)
on("check_join", function(e) if e.name == "#staff" and not e.user.oper then return "Staff only" end end)
on("check_create", function(e) if e.name:find("^#x%-") then return "No #x- channels" end end)
on("check_topic", function(e)
  if e.text:find("spam") then return "No spam in topics" end
  e.text = e.text:gsub("teh", "the")
end)
on("check_kick", function(e) if e.user.nick == "guard" then return "guard can't be kicked" end end)
on("check_part", function(e) if e.reason:find("advert") then e.reason = "" end end)
on("check_quit", function(e) e.reason = e.reason:gsub("advert", "***") end)
on("check_away", function(e) if e.text:find("advert") then return "No adverts in away messages" end end)
on("check_invite", function(e) if e.name == "#secret" then return false end end)
on("check_knock", function(e) if e.channel.name == "#closed" then return "Knocking is off there" end end)
on("check_mode", function(e) if e.modes:find("k") then return "No keys on this network" end end)
on("check_command", function(e) if e.command == "WHOWAS" then return "WHOWAS is off" end end)
on("check_oper", function(e) if e.user.nick == "notme" then return "Not from this nick" end end)
on("check_whois", function(e) if e.target.nick == "ann" then return { "is a friend of the module", "has two lines" } end end)
on("check_list", function(e) if e.channel.name == "#hidden" then return false end end)
on("check_names", function(e) if e.member.nick == "ghost" then return false end end)
on("check_tagmsg", function(e) if e.channel and e.channel.name == "#quiet" then return false end end)
`

func TestModuleCheckHooks(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gate.lua"), []byte(gateModule), 0o600)
	s.onLoop(func() {
		s.cfg.Modules.Dir = dir
		if err := s.modLoad("gate", false); err != nil {
			t.Error(err)
		}
	})
	evil := raw(t, s)
	evil.nick = "evil"
	evil.send("NICK evil")
	evil.send("USER evil 0 * :Evil")
	evil.expect("Not welcome")

	a := login(t, s, "ann")
	a.send("NICK admin2")
	a.expect(" 432 ann admin2 :That nick is kept for staff")
	a.send("JOIN #staff")
	a.expect(" 972 ann JOIN :Staff only")
	a.send("JOIN #x-files")
	a.expect(" 972 ann JOIN :No #x- channels")
	a.send("JOIN #room")
	a.expect(" 366 ann #room ")
	a.send("TOPIC #room :buy spam now")
	a.expect(" 972 ann TOPIC :No spam in topics")
	a.send("TOPIC #room :teh rules")
	a.expect("TOPIC #room :the rules")
	a.send("MODE #room +k secret")
	a.expect(" 972 ann MODE :No keys on this network")
	a.send("MODE #room +m")
	a.expect("MODE #room +m")
	a.send("WHOWAS someone")
	a.expect(" 972 ann WHOWAS :WHOWAS is off")
	a.send("AWAY :see my advert")
	a.expect(" 972 ann AWAY :No adverts in away messages")
	a.send("AWAY :lunch")
	a.expect(" 306 ann ")

	g := login(t, s, "guard")
	g.send("JOIN #room")
	g.expect(" 366 guard #room ")
	a.expect("JOIN #room")
	a.send("KICK #room guard :out")
	a.expect(" 972 ann KICK :guard can't be kicked")
	g.send("PART #room :see my advert")
	if l := a.expect(":guard!~guard@127.0.0.1 PART #room"); strings.Contains(l, "advert") {
		t.Fatalf("part message kept: %q", l)
	}

	b := login(t, s, "bob")
	b.send("JOIN #room")
	b.expect(" 366 bob #room ")
	a.expect("JOIN #room")
	b.send("QUIT :an advert here")
	a.expect(":bob!~bob@127.0.0.1 QUIT :Quit: an *** here")

	a.send("WHOIS ann")
	a.expect(" 320 ann ann :is a friend of the module")
	a.expect(" 320 ann ann :has two lines")
	a.expect(" 318 ann ann ")

	h := login(t, s, "ghost")
	h.send("JOIN #room")
	h.expect(" 366 ghost #room ")
	a.send("NAMES #room")
	if l := a.expect(" 353 ann = #room "); strings.Contains(l, "ghost") {
		t.Fatalf("NAMES shows the hidden member: %q", l)
	}
	h.send("JOIN #hidden")
	h.expect(" 366 ghost #hidden ")
	a.send("LIST")
	a.expect(" 322 ann #room ")
	a.send("LIST #hidden")
	a.expect(" 323 ann ")
	a.quiet(" 322 ann #hidden ")

	h.send("JOIN #closed")
	h.expect(" 366 ghost #closed ")
	h.send("MODE #closed +i")
	h.expect("MODE #closed +i")
	a.send("KNOCK #closed")
	a.expect("Cannot knock on #closed (Knocking is off there)")

	// The module still can't stop an oper taking it out.
	o := boss(t, s)
	o.send("MODULE UNLOAD gate")
	o.expect("gate is unloaded.")
	a.send("JOIN #staff")
	a.expect(" 366 ann #staff ")
}

func TestModuleCheckOper(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "gate.lua"), []byte(strings.Replace(gateModule, `"notme"`, `"Boss"`, 1)), 0o600)
	s.onLoop(func() {
		s.cfg.Modules.Dir = dir
		if err := s.modLoad("gate", false); err != nil {
			t.Error(err)
		}
	})
	c := registered(t, s, "Boss")
	c.send("OPER Boss " + operPW)
	c.expect(" 491 Boss :Not from this nick")
}

const keeperModule = `
module { name = "keeper", version = "1.0" }
command("KEEP", { params = 2 }, function(user, args)
  store.set(args[1], args[2])
  store.set("count", (store.get("count") or 0) + 1)
  store.set("flag", true)
  notice(user, "kept " .. args[1])
end)
command("RECALL", { params = 1 }, function(user, args)
  notice(user, args[1] .. "=" .. tostring(store.get(args[1])) .. " count=" .. tostring(store.get("count")) .. " flag=" .. tostring(store.get("flag")) .. " keys=" .. table.concat(store.keys(), ","))
end)
command("FORGET", { params = 1 }, function(user, args) store.delete(args[1]); notice(user, "forgot") end)
command("BADKEEP", {}, function(user) store.set("t", {}) end)
command("SEEN", {}, function(user)
  local d = data(user)
  d.n = (d.n or 0) + 1
  notice(user, "seen " .. d.n)
end)
`

func TestModuleStoreAndData(t *testing.T) {
	s, _, _ := fileServer(t, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "keeper.lua"), []byte(keeperModule), 0o600)
	s.onLoop(func() { s.cfg.Modules.Dir = dir })
	o := boss(t, s)
	o.send("MODULE LOAD keeper")
	o.expect("keeper is loaded.")
	a := login(t, s, "ann")
	a.send("KEEP colour yellow")
	a.expect("NOTICE ann :kept colour")
	a.send("KEEP shape hex")
	a.expect("NOTICE ann :kept shape")
	a.send("SEEN")
	a.expect("NOTICE ann :seen 1")
	a.send("SEEN")
	a.expect("NOTICE ann :seen 2")
	// A reload forgets data(...) and keeps store.
	o.send("MODULE RELOAD keeper")
	o.expect("keeper is reloaded.")
	a.send("RECALL colour")
	a.expect("NOTICE ann :colour=yellow count=2 flag=true keys=colour,count,flag,shape")
	a.send("SEEN")
	a.expect("NOTICE ann :seen 1")
	a.send("FORGET colour")
	a.expect("NOTICE ann :forgot")
	a.send("RECALL colour")
	a.expect("NOTICE ann :colour=nil count=2 flag=true keys=count,flag,shape")
	a.send("BADKEEP")
	o.expect("store keeps a text, a number, true or false")
	// Another module does not see it.
	os.WriteFile(filepath.Join(dir, "other.lua"), []byte("module { name = \"other\", version = \"1\" }\ncommand(\"PEEK\", {}, function(user) notice(user, \"shape=\" .. tostring(store.get(\"shape\"))) end)\n"), 0o600)
	o.send("MODULE LOAD other")
	o.expect("other is loaded.")
	a.send("PEEK")
	a.expect("NOTICE ann :shape=nil")
}

const actorModule = `
module { name = "actor", version = "1.0" }
command("MKICK", { params = 2 }, function(user, args) kick(args[1], args[2], "by the module") end)
command("MTOPIC", { params = 2 }, function(user, args) set_topic(args[1], args[2]) end)
command("MJOIN", { params = 2 }, function(user, args) force_join(args[1], args[2]) end)
command("MUMODE", { params = 2 }, function(user, args) set_user_mode(args[1], args[2]) end)
command("MBAN", { params = 1 }, function(user, args) ban(args[1], "bad address", 600) end)
command("MFETCH", { params = 1 }, function(user, args)
  http.get(args[1], function(status, body, err) notice(user, "got " .. status .. " " .. body .. " " .. tostring(err)) end)
end)
command("MPOST", { params = 2 }, function(user, args)
  http.post(args[1], args[2], function(status, body) notice(user, "posted " .. status .. " " .. body) end, "text/plain")
end)
`

func TestModuleActing(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			buf := make([]byte, 64)
			n, _ := r.Body.Read(buf)
			w.Write([]byte("echo:" + string(buf[:n]) + ":" + r.Header.Get("Content-Type")))
			return
		}
		w.Write([]byte("hello"))
	}))
	defer web.Close()
	s, path, _ := fileServer(t, "")
	dir := filepath.Join(filepath.Dir(path), "modules")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "actor.lua"), []byte(actorModule), 0o600)
	o := boss(t, s)
	conf, _ := os.ReadFile(path)
	rehash := func(allow string) {
		t.Helper()
		os.WriteFile(path, append(append([]byte{}, conf...), []byte("\n[modules]\nload = [\"actor\"]\n\n[module.actor]\nallow = ["+allow+"]\n")...), 0o600)
		o.send("REHASH")
		o.expect("re-read beeircd.conf")
	}
	rehash("")
	a := login(t, s, "ann")
	b := login(t, s, "bob")
	a.send("JOIN #x")
	a.expect(" 366 ann #x ")
	b.send("JOIN #x")
	b.expect(" 366 bob #x ")
	a.expect("JOIN #x")
	for cmd, right := range map[string]string{"MKICK #x bob": "kick", "MTOPIC #x hi": "topic", "MJOIN bob #y": "join", "MUMODE bob +R": "usermode", "MBAN 203.0.113.9": "ban", "MFETCH " + web.URL: "http"} {
		a.send(cmd)
		o.expect(`this needs the right "` + right + `"`)
		s.onLoop(func() { s.mods.byName["actor"].errs = nil }) // not the point of this test
	}
	rehash(`"kick", "topic", "join", "usermode", "ban", "http"`)
	a.send("MTOPIC #x :set by a module")
	b.expect(":irc.test.local TOPIC #x :set by a module")
	a.send("MJOIN bob #y")
	b.expect(":bob!~bob@127.0.0.1 JOIN #y")
	a.send("MUMODE bob +R")
	b.expect("MODE bob +R")
	a.send("MUMODE bob +o")
	o.expect("a module can't make or unmake opers")
	a.send("MKICK #x bob")
	b.expect(":irc.test.local KICK #x bob :by the module")
	a.send("MBAN 203.0.113.9")
	a.sync()
	o.send("OS AKILL LIST")
	o.expect("*@203.0.113.9")
	// The web server of the test is on this machine: refused without http-local.
	a.send("MFETCH " + web.URL)
	a.expect("is an address on this machine or a private network")
	rehash(`"http", "http-local"`)
	a.send("MFETCH " + web.URL)
	a.expect("NOTICE ann :got 200 hello nil")
	a.send("MPOST " + web.URL + " ping")
	a.expect("NOTICE ann :posted 200 echo:ping:text/plain")
	a.send("MFETCH ftp://example.org/")
	o.expect("an address that starts with https:// or http:// is expected")
}

func TestModuleCheckHooksOffCostNothing(t *testing.T) {
	s := testServer(t, func(c *config.Config) {})
	a := login(t, s, "ann")
	a.send("JOIN #x")
	a.expect(" 366 ann #x ")
	s.onLoop(func() {
		if s.modHas("check_join") || len(s.mods.order) != 0 {
			t.Error("a server without modules has hooks")
		}
	})
}
