package server

import (
	"fmt"
	"sort"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/luamod"
	"github.com/fhazal/beeircd/internal/state"
)

// What a module can call. People and channels are handed over as plain
// tables: a copy of how things were at that moment, not the live thing.

// modUser is u as a table.
func (s *Server) modUser(m *module, u *state.User) *lua.LTable {
	t := m.L.CreateTable(0, 16)
	set := func(k string, v lua.LValue) { t.RawSetString(k, v) }
	set("nick", lua.LString(u.Nick))
	set("ident", lua.LString(u.Ident))
	set("host", lua.LString(u.Host))
	set("realname", lua.LString(u.Realname))
	set("server", lua.LString(u.Server))
	set("uid", lua.LString(u.UID))
	set("ip", lua.LString(u.IP))
	set("modes", lua.LString(u.ModeString()))
	set("oper", lua.LBool(u.HasMode('o')))
	set("tls", lua.LBool(u.TLS))
	set("service", lua.LBool(u.Service))
	set("here", lua.LBool(s.clients[u.UID] != nil))
	if u.Account != "" {
		set("account", lua.LString(u.Account))
	}
	if u.Away != "" {
		set("away", lua.LString(u.Away))
	}
	return t
}

// modChannel is ch as a table.
func (s *Server) modChannel(m *module, ch *state.Channel) *lua.LTable {
	t := m.L.CreateTable(0, 4)
	modes, _ := ch.ModeString(false)
	t.RawSetString("name", lua.LString(ch.Name))
	t.RawSetString("topic", lua.LString(ch.Topic))
	t.RawSetString("modes", lua.LString(modes))
	t.RawSetString("count", lua.LNumber(len(ch.Members)))
	return t
}

// modConfig is the module's [module.NAME] block as a table, without allow.
func (s *Server) modConfig(m *module) *lua.LTable {
	t := m.L.NewTable()
	for k, v := range s.cfg.Module[m.name] {
		if k != "allow" {
			t.RawSetString(k, toLua(m.L, v))
		}
	}
	return t
}

func toLua(L *lua.LState, v any) lua.LValue {
	switch v := v.(type) {
	case string:
		return lua.LString(v)
	case bool:
		return lua.LBool(v)
	case int64:
		return lua.LNumber(v)
	case int:
		return lua.LNumber(v)
	case float64:
		return lua.LNumber(v)
	case []any:
		t := L.NewTable()
		for _, x := range v {
			t.Append(toLua(L, x))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		for k, x := range v {
			t.RawSetString(k, toLua(L, x))
		}
		return t
	case nil:
		return lua.LNil
	}
	return lua.LString(fmt.Sprint(v))
}

// modArgUser finds the person a module means: a user table or a nick.
func (s *Server) modArgUser(L *lua.LState, n int) *state.User {
	switch v := L.Get(n).(type) {
	case *lua.LTable:
		if uid, ok := v.RawGetString("uid").(lua.LString); ok {
			return s.net.UserByUID(string(uid))
		}
	case lua.LString:
		return s.net.UserByNick(string(v))
	default:
		L.ArgError(n, "a user (a table from a hook, or a nick) is expected")
	}
	return nil
}

// modArgChannel finds the channel a module means: a channel table or a name.
func (s *Server) modArgChannel(L *lua.LState, n int) *state.Channel {
	switch v := L.Get(n).(type) {
	case *lua.LTable:
		if name, ok := v.RawGetString("name").(lua.LString); ok {
			return s.net.Channel(string(name))
		}
	case lua.LString:
		return s.net.Channel(string(v))
	default:
		L.ArgError(n, "a channel (a table from a hook, or a name) is expected")
	}
	return nil
}

// modText is argument n as one clean line.
func modText(L *lua.LState, n int) string {
	return irc.CleanFraming(strings.TrimRight(L.CheckString(n), "\r\n"))
}

// modAPI gives m's Lua state the functions of the server.
func (s *Server) modAPI(m *module) {
	L := m.L
	fn := func(name string, f lua.LGFunction) { L.SetGlobal(name, L.NewFunction(f)) }
	need := func(L *lua.LState, right string) {
		if !m.rights[right] {
			L.RaiseError("this needs the right %q: add allow = [\"%s\"] to [module.%s] in beeircd.conf", right, right, m.name)
		}
	}

	fn("module", func(L *lua.LState) int {
		m.info = luamod.ReadInfo(L.CheckTable(1))
		return 0
	})
	fn("on", func(L *lua.LState) int {
		hook, f := L.CheckString(1), L.CheckFunction(2)
		h, ok := luamod.Hooks[hook]
		switch {
		case !ok:
			L.RaiseError("on(%q, ...): there is no such hook", hook)
		case !h.Built:
			L.RaiseError("on(%q, ...): this hook is planned but not in this version yet", hook)
		}
		m.hooks[hook] = append(m.hooks[hook], f)
		if s.mods.byName[m.name] == m { // registered after loading
			s.mods.hooked[hook]++
		}
		return 0
	})
	fn("command", func(L *lua.LState) int {
		name := strings.ToUpper(L.CheckString(1))
		opts, f := L.CheckTable(2), L.CheckFunction(3)
		switch {
		case !luamod.ValidCommand(name):
			L.RaiseError("command(%q, ...): use letters and digits only, starting with a letter", name)
		case commands[name].run != nil:
			L.RaiseError("command(%q, ...): BeeIRCd has a command of that name itself", name)
		case s.showFile(name) != nil:
			L.RaiseError("command(%q, ...): a [[showfile]] block has that name", name)
		}
		if other := s.mods.cmds[name]; other != nil && other.mod != m {
			L.RaiseError("command(%q, ...): the module %s has that command already", name, other.mod.name)
		}
		mc := &modCommand{mod: m, name: name, fn: f}
		if n, ok := opts.RawGetString("params").(lua.LNumber); ok {
			mc.params = int(n)
		}
		if h, ok := opts.RawGetString("help").(lua.LString); ok {
			mc.help = irc.CleanFraming(string(h))
		}
		mc.opers = lua.LVAsBool(opts.RawGetString("opers"))
		if s.mods.cmds[name] == nil {
			m.cmds = append(m.cmds, name)
			sort.Strings(m.cmds)
		}
		s.mods.cmds[name] = mc
		return 0
	})

	// ---- telling people things ----
	fn("notice", func(L *lua.LState) int {
		text := modText(L, 2)
		if u := s.modArgUser(L, 1); u != nil && text != "" {
			host{s}.ServerNotice(u.UID, text)
		}
		return 0
	})
	fn("fail", func(L *lua.LState) int {
		text := modText(L, 2)
		if u := s.modArgUser(L, 1); u != nil && text != "" {
			host{s}.ServerNotice(u.UID, "*** "+text)
		}
		return 0
	})
	fn("numeric", func(L *lua.LState) int {
		u := s.modArgUser(L, 1)
		code := L.CheckString(2)
		if len(code) != 3 || strings.Trim(code, "0123456789") != "" {
			L.ArgError(2, "a numeric is three digits, like \"372\"")
		}
		var params []string
		for i := 3; i <= L.GetTop(); i++ {
			params = append(params, modText(L, i))
		}
		if u != nil {
			if c := s.clients[u.UID]; c != nil {
				s.reply(c, code, params...)
			}
		}
		return 0
	})
	fn("snotice", func(L *lua.LState) int {
		s.snotice("[%s] %s", m.name, modText(L, 1))
		return 0
	})
	fn("log", func(L *lua.LState) int {
		s.log.Printf("module %s: %s", m.name, modText(L, 1))
		return 0
	})

	// ---- looking things up ----
	fn("find_user", func(L *lua.LState) int {
		if u := s.net.UserByNick(L.CheckString(1)); u != nil {
			L.Push(s.modUser(m, u))
			return 1
		}
		return 0
	})
	fn("find_channel", func(L *lua.LState) int {
		if ch := s.net.Channel(L.CheckString(1)); ch != nil {
			L.Push(s.modChannel(m, ch))
			return 1
		}
		return 0
	})
	fn("users", func(L *lua.LState) int {
		t := L.NewTable()
		for _, u := range s.net.Users() {
			t.Append(s.modUser(m, u))
		}
		L.Push(t)
		return 1
	})
	fn("channels", func(L *lua.LState) int {
		t := L.NewTable()
		for _, ch := range s.net.Channels() {
			t.Append(s.modChannel(m, ch))
		}
		L.Push(t)
		return 1
	})
	fn("members", func(L *lua.LState) int {
		t := L.NewTable()
		if ch := s.modArgChannel(L, 1); ch != nil {
			for _, mem := range ch.SortedMembers() {
				u := s.modUser(m, mem.User)
				u.RawSetString("prefix", lua.LString(mem.Prefix()))
				t.Append(u)
			}
		}
		L.Push(t)
		return 1
	})

	// ---- time ----
	timer := func(repeat bool) lua.LGFunction {
		return func(L *lua.LState) int {
			secs, f := float64(L.CheckNumber(1)), L.CheckFunction(2)
			if secs < 1 {
				secs = 1
			}
			if secs > 86400*30 {
				L.ArgError(1, "at most 30 days")
			}
			if len(m.timers) >= modMaxTimers {
				L.RaiseError("a module can have %d timers at most", modMaxTimers)
			}
			d := time.Duration(secs * float64(time.Second))
			t := &modTimer{at: time.Now().Add(d), fn: f}
			if repeat {
				t.every = d
			}
			m.timers = append(m.timers, t)
			return 0
		}
	}
	fn("after", timer(false))
	fn("every", timer(true))

	// ---- acting, with a right ----
	fn("has_right", func(L *lua.LState) int {
		L.Push(lua.LBool(m.rights[strings.ToLower(L.CheckString(1))]))
		return 1
	})
	fn("kill", func(L *lua.LState) int {
		need(L, "kill")
		reason := "Disconnected"
		if L.GetTop() >= 2 {
			reason = modText(L, 2)
		}
		if u := s.modArgUser(L, 1); u != nil && !u.Service {
			s.killUser(u, s.cfg.Server.Name, reason)
		}
		return 0
	})
}
