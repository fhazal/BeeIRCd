package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/luamod"
	"github.com/fhazal/beeircd/internal/state"
)

// Modules: Lua files loaded into the server ([modules] in beeircd.conf).
// Each module has a Lua state of its own; everything it does goes through
// the functions in modules_api.go. All of it runs on the event loop.

const (
	modMaxDepth   = 4  // a module's action may set off its own hooks this deep
	modMaxTimers  = 64 // after(...) / every(...) per module
	modErrorsOff  = 3  // this many errors within modErrorsSpan unload the module
	modErrorsSpan = time.Minute
)

type module struct {
	name     string
	path     string
	L        *lua.LState
	info     luamod.Info
	hooks    map[string][]*lua.LFunction
	cmds     []string
	timers   []*modTimer
	rights   map[string]bool
	errs     []time.Time
	loaded   time.Time
	calls    uint64
	depth    int
	dead     string            // why it must be unloaded once the running call is over
	kept     map[string]string // store.*: what it keeps in the database
	keptSize int
	udata    map[string]*lua.LTable // data(user), by UID
	cdata    map[string]*lua.LTable // data(channel), by name
	httpBusy int
	fromConf bool // listed in [modules] load (not only /MODULE LOAD)
}

type modCommand struct {
	mod    *module
	name   string
	fn     *lua.LFunction
	params int
	help   string
	opers  bool
}

type modTimer struct {
	at    time.Time
	every time.Duration
	fn    *lua.LFunction
}

type modules struct {
	byName    map[string]*module
	order     []string
	cmds      map[string]*modCommand
	hooked    map[string]int // handlers per hook, over all modules
	lastTick  time.Time
	lastSweep time.Time
}

func (s *Server) modulesStart() {
	for _, name := range s.cfg.Modules.Load {
		if err := s.modLoad(name, true); err != nil {
			s.log.Printf("module %s not loaded: %v", name, err)
		}
	}
}

func (s *Server) modDir() string { return s.cfg.Modules.DirOr(s.cfg.Path) }

// modLoad loads NAME.lua from the modules folder.
func (s *Server) modLoad(name string, fromConf bool) error {
	if !luamod.ValidName(name) {
		return fmt.Errorf("%q is not a module name (small letters, digits, - and _)", name)
	}
	if s.mods.byName[name] != nil {
		return errors.New("it is loaded already")
	}
	path := filepath.Join(s.modDir(), name+".lua")
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("can't read %s: %w", path, errors.Unwrap(err))
	}
	if len(src) > 1<<20 {
		return fmt.Errorf("%s is over 1 MB", path)
	}
	m := &module{name: name, path: path, L: luamod.NewState(), hooks: map[string][]*lua.LFunction{}, loaded: time.Now(), fromConf: fromConf}
	m.rights = s.modRights(name)
	s.modAPI(m)
	s.modAPIMore(m)
	m.L.SetGlobal("config", s.modConfig(m))
	fail := func(err error) error {
		s.modDrop(m)
		return err
	}
	// Loading may take a little longer than one call.
	if err := luamod.Run(m.L, name+".lua", string(src), 10*s.cfg.Modules.LimitOr()); err != nil {
		return fail(errors.New(luamod.CleanError(err)))
	}
	switch {
	case m.info.Name == "":
		return fail(errors.New(`it has no module { name = "` + name + `", version = ... } line`))
	case m.info.Name != name:
		return fail(fmt.Errorf("it calls itself %q, but the file is %s.lua", m.info.Name, name))
	case m.info.API > luamod.API:
		return fail(fmt.Errorf("it needs module interface %d; this BeeIRCd has %d", m.info.API, luamod.API))
	}
	s.mods.byName[name] = m
	s.mods.order = append(s.mods.order, name)
	for h, fns := range m.hooks {
		s.mods.hooked[h] += len(fns)
	}
	s.log.Printf("module %s %s loaded", name, m.info.Version)
	s.modFireOne(m, "load", nil)
	return nil
}

// modDrop takes everything of m out of the server and closes its state.
func (s *Server) modDrop(m *module) {
	for _, c := range m.cmds {
		if mc := s.mods.cmds[c]; mc != nil && mc.mod == m {
			delete(s.mods.cmds, c)
		}
	}
	if s.mods.byName[m.name] == m {
		delete(s.mods.byName, m.name)
		for h, fns := range m.hooks {
			s.mods.hooked[h] -= len(fns)
		}
		for i, n := range s.mods.order {
			if n == m.name {
				s.mods.order = append(s.mods.order[:i], s.mods.order[i+1:]...)
				break
			}
		}
	}
	m.hooks, m.timers = nil, nil
	m.L.Close()
}

// modUnload unloads a module. While one of its own calls is running it
// only marks it; the call's end does the rest.
func (s *Server) modUnload(name, why string) error {
	m := s.mods.byName[name]
	if m == nil {
		return errors.New("it is not loaded")
	}
	if m.depth > 0 {
		m.dead = why
		return nil
	}
	s.modFireOne(m, "unload", nil)
	if s.mods.byName[name] != m {
		return nil // the unload hook failed for the third time: gone already
	}
	s.modDrop(m)
	s.log.Printf("module %s unloaded (%s)", name, why)
	return nil
}

// modCall runs fn of m with args, for at most the time limit. ok is
// false when it failed; the answer is then nil.
func (s *Server) modCall(m *module, fn *lua.LFunction, args ...lua.LValue) (ret lua.LValue, ok bool) {
	if m.dead != "" || m.depth >= modMaxDepth {
		return lua.LNil, false
	}
	L := m.L
	outer := m.depth == 0
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if outer {
		ctx, cancel = context.WithTimeout(ctx, s.cfg.Modules.LimitOr())
		L.SetContext(ctx)
	}
	defer cancel()
	m.depth++
	m.calls++
	top := L.GetTop()
	err := L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, args...)
	m.depth--
	if err == nil {
		ret = L.Get(-1)
	}
	L.SetTop(top)
	if outer {
		slow := ctx.Err() != nil
		L.RemoveContext()
		if err != nil && slow {
			err = fmt.Errorf("a call ran longer than %s and was stopped", s.cfg.Modules.LimitOr())
		}
	}
	if err != nil {
		s.modTrouble(m, err)
	}
	if m.depth == 0 && m.dead != "" {
		why := m.dead
		m.dead = ""
		if s.mods.byName[m.name] == m {
			s.modDrop(m)
			s.snotice("Module %s was unloaded: %s", m.name, why)
		}
		return lua.LNil, false
	}
	if err != nil {
		return lua.LNil, false
	}
	return ret, true
}

// modTrouble notes an error of m and switches m off when they pile up.
func (s *Server) modTrouble(m *module, err error) {
	now := time.Now()
	keep := m.errs[:0]
	for _, t := range m.errs {
		if now.Sub(t) < modErrorsSpan {
			keep = append(keep, t)
		}
	}
	m.errs = append(keep, now)
	s.snotice("Module %s: %s", m.name, luamod.CleanError(err))
	if len(m.errs) >= modErrorsOff && m.dead == "" {
		m.dead = fmt.Sprintf("%d errors within a minute", len(m.errs))
	}
}

// modHas reports whether any module listens to hook: the cheap test
// before building what the hook is given.
func (s *Server) modHas(hook string) bool { return s.mods != nil && s.mods.hooked[hook] > 0 }

// modFire tells every module that listens to hook. build makes the table
// the handlers get, once per module (tables can't cross Lua states).
func (s *Server) modFire(hook string, build func(m *module) *lua.LTable) {
	if !s.modHas(hook) {
		return
	}
	for _, name := range append([]string(nil), s.mods.order...) {
		if m := s.mods.byName[name]; m != nil && len(m.hooks[hook]) > 0 {
			s.modFireOne(m, hook, build)
		}
	}
}

func (s *Server) modFireOne(m *module, hook string, build func(m *module) *lua.LTable) {
	fns := m.hooks[hook]
	if len(fns) == 0 {
		return
	}
	var arg lua.LValue = lua.LNil
	if build != nil {
		arg = build(m)
	} else {
		arg = m.L.NewTable()
	}
	for _, fn := range fns {
		if _, ok := s.modCall(m, fn, arg); !ok && s.mods.byName[m.name] != m {
			return // unloaded meanwhile
		}
	}
}

// modRefused is what a module's `return false` says when the caller has
// no silent way to refuse.
const modRefused = "Refused by a module of this network"

// modAsk asks the modules that listen to a check hook. fill puts the
// people and channels into the table a handler gets; vals are the texts a
// handler may change by setting e.<key> (they are updated in place). why
// is the reason of a handler that refused by returning a text; drop is
// true when one returned false. answer, when not nil, sees what each
// handler returned (for hooks that give something back).
func (s *Server) modAsk(hook string, vals map[string]string, fill func(m *module, e *lua.LTable), answer func(lua.LValue)) (why string, drop bool) {
	if !s.modHas(hook) {
		return "", false
	}
	for _, name := range append([]string(nil), s.mods.order...) {
		m := s.mods.byName[name]
		if m == nil {
			continue
		}
		for _, fn := range m.hooks[hook] {
			e := m.L.NewTable()
			if fill != nil {
				fill(m, e)
			}
			for k, v := range vals {
				e.RawSetString(k, lua.LString(v))
			}
			ret, ok := s.modCall(m, fn, e)
			if !ok {
				if s.mods.byName[name] != m {
					break // unloaded meanwhile
				}
				continue
			}
			switch v := ret.(type) {
			case lua.LString:
				return irc.CleanFraming(string(v)), false
			case lua.LBool:
				if !bool(v) {
					return "", true
				}
			}
			if answer != nil {
				answer(ret)
			}
			for k := range vals {
				if nv, isText := e.RawGetString(k).(lua.LString); isText {
					vals[k] = irc.CleanFraming(string(nv))
				}
			}
		}
	}
	return "", false
}

// modRefuses asks a check hook for c's command cmd, and tells c when a
// module refused it. It reports whether one did.
func (s *Server) modRefuses(c *Client, cmd, hook string, vals map[string]string, fill func(m *module, e *lua.LTable)) bool {
	why, drop := s.modAsk(hook, vals, fill, nil)
	if drop {
		why = modRefused
	}
	if why == "" {
		return false
	}
	s.reply(c, irc.ErrCannotDoCommand, cmd, why)
	return true
}

// modCheckMessage asks the modules about a message from a local person,
// before it is delivered. why refuses it with that reason, drop drops it
// without a word, text is what goes on.
func (s *Server) modCheckMessage(c *Client, ch *state.Channel, t *state.User, verb, text string) (out, why string, drop bool) {
	if !s.modHas("check_message") {
		return text, "", false
	}
	vals := map[string]string{"text": text}
	why, drop = s.modAsk("check_message", vals, func(m *module, e *lua.LTable) {
		s.modMessageFill(m, e, c.user, ch, t, verb)
	}, nil)
	if vals["text"] != "" {
		text = vals["text"]
	}
	return text, why, drop
}

// modMessageDone tells the modules a local person's message was delivered.
func (s *Server) modMessageDone(u *state.User, ch *state.Channel, t *state.User, verb, text string) {
	s.modFire("message", func(m *module) *lua.LTable { return s.modMessageTable(m, u, ch, t, verb, text) })
}

func (s *Server) modMessageTable(m *module, u *state.User, ch *state.Channel, t *state.User, verb, text string) *lua.LTable {
	e := m.L.NewTable()
	s.modMessageFill(m, e, u, ch, t, verb)
	e.RawSetString("text", lua.LString(text))
	return e
}

func (s *Server) modMessageFill(m *module, e *lua.LTable, u *state.User, ch *state.Channel, t *state.User, verb string) {
	e.RawSetString("user", s.modUser(m, u))
	if ch != nil {
		e.RawSetString("channel", s.modChannel(m, ch))
	}
	if t != nil {
		e.RawSetString("target", s.modUser(m, t))
	}
	e.RawSetString("notice", lua.LBool(verb == "NOTICE"))
}

// modPrepare looks at ev before it is applied and returns what tells the
// modules afterwards, nil when no module wants to know.
func (s *Server) modPrepare(ev state.Event) func() {
	if s.mods == nil || len(s.mods.order) == 0 {
		return nil
	}
	user := func(uid string) *state.User { return s.net.UserByUID(uid) }
	// who is the person behind uid as a table, nil for the server.
	who := func(m *module, uid string) lua.LValue {
		if u := user(uid); u != nil {
			return s.modUser(m, u)
		}
		return lua.LNil
	}
	switch e := ev.(type) {
	case *state.UserAdd:
		if !s.modHas("connect") {
			return nil
		}
		return func() {
			s.modFire("connect", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", s.modUser(m, e.User))
				return t
			})
		}
	case *state.UserQuit:
		u := user(e.UID)
		if u == nil || !s.modHas("quit") {
			return nil
		}
		gone := u.Copy() // the user is gone when the hook runs
		return func() {
			s.modFire("quit", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", s.modUser(m, gone))
				t.RawSetString("reason", lua.LString(e.Reason))
				return t
			})
		}
	case *state.NickChange:
		u := user(e.UID)
		if u == nil || !s.modHas("nick") {
			return nil
		}
		old := u.Nick
		return func() {
			s.modFire("nick", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", who(m, e.UID))
				t.RawSetString("old", lua.LString(old))
				return t
			})
		}
	case *state.Join:
		if !s.modHas("join") {
			return nil
		}
		return func() {
			u, ch := user(e.UID), s.net.Channel(e.Channel)
			if u == nil || ch == nil {
				return
			}
			s.modFire("join", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", s.modUser(m, u))
				t.RawSetString("channel", s.modChannel(m, ch))
				t.RawSetString("created", lua.LBool(e.Created))
				t.RawSetString("burst", lua.LBool(e.Burst))
				return t
			})
		}
	case *state.Part, *state.Kick:
		hook, uid, name, reason, src := "part", "", "", "", ""
		switch e := e.(type) {
		case *state.Part:
			uid, name, reason = e.UID, e.Channel, e.Reason
		case *state.Kick:
			hook, uid, name, reason, src = "kick", e.UID, e.Channel, e.Reason, e.Source
		}
		u, ch := user(uid), s.net.Channel(name)
		if u == nil || ch == nil || !s.modHas(hook) {
			return nil
		}
		chName, chTopic, count := ch.Name, ch.Topic, len(ch.Members)-1
		return func() {
			s.modFire(hook, func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", s.modUser(m, u))
				// The channel may be gone (they were the last one in it).
				c := m.L.NewTable()
				c.RawSetString("name", lua.LString(chName))
				c.RawSetString("topic", lua.LString(chTopic))
				c.RawSetString("count", lua.LNumber(count))
				t.RawSetString("channel", c)
				t.RawSetString("reason", lua.LString(reason))
				if hook == "kick" {
					t.RawSetString("source", who(m, src))
				}
				return t
			})
		}
	case *state.Topic:
		if !s.modHas("topic") {
			return nil
		}
		return func() {
			ch := s.net.Channel(e.Channel)
			if ch == nil {
				return
			}
			s.modFire("topic", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("channel", s.modChannel(m, ch))
				t.RawSetString("text", lua.LString(e.Text))
				t.RawSetString("source", who(m, e.Source))
				return t
			})
		}
	case *state.ChannelMode:
		if !s.modHas("channel_mode") {
			return nil
		}
		return func() {
			ch := s.net.Channel(e.Channel)
			if ch == nil || len(e.Changes) == 0 {
				return
			}
			var parts []string
			for _, line := range modeLines(fromState(e.Changes)) {
				parts = append(parts, strings.Join(line, " "))
			}
			s.modFire("channel_mode", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("channel", s.modChannel(m, ch))
				t.RawSetString("modes", lua.LString(strings.Join(parts, " ")))
				t.RawSetString("source", who(m, e.Source))
				return t
			})
		}
	case *state.UserMode:
		if !s.modHas("user_mode") {
			return nil
		}
		return func() {
			u := user(e.UID)
			if u == nil || len(e.Changes) == 0 {
				return
			}
			var b strings.Builder
			last := byte(0)
			for _, c := range e.Changes {
				sign := byte('-')
				if c.Add {
					sign = '+'
				}
				if sign != last {
					b.WriteByte(sign)
					last = sign
				}
				b.WriteByte(c.Mode)
			}
			s.modFire("user_mode", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", s.modUser(m, u))
				t.RawSetString("modes", lua.LString(b.String()))
				return t
			})
		}
	case *state.Away:
		hook := "away"
		if e.Message == "" {
			hook = "back"
		}
		if !s.modHas(hook) {
			return nil
		}
		return func() {
			s.modFire(hook, func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", who(m, e.UID))
				t.RawSetString("text", lua.LString(e.Message))
				return t
			})
		}
	case *state.Login:
		hook, account := "login", e.Account
		if e.Account == "" {
			hook = "logout"
			if u := user(e.UID); u != nil {
				account = u.Account
			}
		}
		if !s.modHas(hook) {
			return nil
		}
		return func() {
			s.modFire(hook, func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", who(m, e.UID))
				t.RawSetString("account", lua.LString(account))
				return t
			})
		}
	case *state.HostChange:
		u := user(e.UID)
		if u == nil || !s.modHas("host_change") {
			return nil
		}
		old := u.Host
		return func() {
			s.modFire("host_change", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", who(m, e.UID))
				t.RawSetString("old", lua.LString(old))
				return t
			})
		}
	case *state.RealnameChange:
		u := user(e.UID)
		if u == nil || !s.modHas("realname_change") {
			return nil
		}
		old := u.Realname
		return func() {
			s.modFire("realname_change", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", who(m, e.UID))
				t.RawSetString("old", lua.LString(old))
				return t
			})
		}
	case *state.Invite:
		if e.Remove || !s.modHas("invite") {
			return nil
		}
		return func() {
			ch := s.net.Channel(e.Channel)
			if ch == nil {
				return
			}
			s.modFire("invite", func(m *module) *lua.LTable {
				t := m.L.NewTable()
				t.RawSetString("user", who(m, e.UID))
				t.RawSetString("channel", s.modChannel(m, ch))
				t.RawSetString("source", who(m, e.Source))
				return t
			})
		}
	}
	return nil
}

// modTick runs the timers of every module, and the tick hook once a second.
func (s *Server) modTick(now time.Time) {
	if s.mods == nil || len(s.mods.order) == 0 {
		return
	}
	for _, name := range append([]string(nil), s.mods.order...) {
		m := s.mods.byName[name]
		if m == nil || len(m.timers) == 0 {
			continue
		}
		due := m.timers
		m.timers = nil
		var keep []*modTimer
		for _, t := range due {
			if now.Before(t.at) {
				keep = append(keep, t)
				continue
			}
			if t.every > 0 {
				t.at = now.Add(t.every)
				keep = append(keep, t)
			}
			s.modCall(m, t.fn)
			if s.mods.byName[name] != m {
				break
			}
		}
		if s.mods.byName[name] == m {
			m.timers = append(keep, m.timers...) // plus the ones the calls added
		}
	}
	if now.Sub(s.mods.lastSweep) >= time.Minute {
		s.mods.lastSweep = now
		s.modSweep()
	}
	if now.Sub(s.mods.lastTick) >= time.Second {
		s.mods.lastTick = now
		s.modFire("tick", nil)
	}
}

// modRehash runs after beeircd.conf was read again: fresh settings and
// rights for every module, new ones loaded, removed ones unloaded.
func (s *Server) modRehash() {
	if s.mods == nil {
		return
	}
	want := map[string]bool{}
	for _, name := range s.cfg.Modules.Load {
		want[name] = true
	}
	for _, name := range append([]string(nil), s.mods.order...) {
		m := s.mods.byName[name]
		if m.fromConf && !want[name] {
			s.modUnload(name, "taken out of [modules] load")
			continue
		}
		m.rights = s.modRights(name)
		m.L.SetGlobal("config", s.modConfig(m))
		s.modFireOne(m, "rehash", nil)
	}
	for _, name := range s.cfg.Modules.Load {
		if s.mods.byName[name] == nil {
			if err := s.modLoad(name, true); err != nil {
				s.snotice("Module %s not loaded: %v", name, err)
			}
		} else {
			s.mods.byName[name].fromConf = true
		}
	}
}

// modulesStop unloads every module (the server is stopping).
func (s *Server) modulesStop() {
	if s.mods == nil {
		return
	}
	for _, name := range append([]string(nil), s.mods.order...) {
		s.modUnload(name, "the server is stopping")
	}
}

// modRights are the rights [module.NAME] allow gives.
func (s *Server) modRights(name string) map[string]bool {
	out := map[string]bool{}
	if list, ok := s.cfg.Module[name]["allow"].([]any); ok {
		for _, r := range list {
			if w, ok := r.(string); ok {
				out[strings.ToLower(w)] = true
			}
		}
	}
	return out
}

// modRunCommand runs a module's command for c. It reports whether name
// is one.
func (s *Server) modRunCommand(c *Client, m irc.Message) bool {
	if s.mods == nil {
		return false
	}
	mc := s.mods.cmds[m.Command]
	if mc == nil {
		return false
	}
	switch {
	case mc.opers && !s.needOper(c):
	case len(m.Params) < mc.params:
		s.reply(c, irc.ErrNeedMoreParams, m.Command, "Not enough parameters")
	default:
		args := mc.mod.L.NewTable()
		for _, p := range m.Params {
			args.Append(lua.LString(p))
		}
		s.modCall(mc.mod, mc.fn, s.modUser(mc.mod, c.user), args)
	}
	return true
}

// modHelp is the HELP text of a module's command, nil when there is none.
func (s *Server) modHelp(subject string) []string {
	if s.mods == nil {
		return nil
	}
	mc := s.mods.cmds[subject]
	if mc == nil {
		return nil
	}
	help := mc.help
	if help == "" {
		help = "A command of the module " + mc.mod.name + "."
	}
	return []string{mc.name + " (module " + mc.mod.name + ")", help}
}

// ---- /MODULE ----

func init() {
	commands["MODULE"] = command{0, false, (*Server).cmdModule}
	helpTopics["MODULE"] = []string{"MODULE LIST | INFO <name> | LOAD <name> | RELOAD <name> | UNLOAD <name>",
		"Shows and manages the Lua modules of this server (opers with flag a).",
		"LOAD reads <name>.lua from the modules folder; to keep it after a restart, add it to [modules] load in beeircd.conf."}
}

func (s *Server) cmdModule(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	if !c.can('a') {
		s.reply(c, irc.ErrNoPrivileges, "Permission Denied - You do not have the correct IRC operator privileges")
		return
	}
	sub, name := strings.ToUpper(m.Param(0)), strings.ToLower(m.Param(1))
	say := func(format string, a ...any) { s.notice(c, fmt.Sprintf(format, a...)) }
	needName := func() bool {
		if name == "" {
			s.reply(c, irc.ErrNeedMoreParams, "MODULE", "Not enough parameters")
			return false
		}
		return true
	}
	switch sub {
	case "", "LIST":
		if len(s.mods.order) == 0 {
			say("No modules are loaded. The folder is %s", s.modDir())
			return
		}
		for _, n := range s.mods.order {
			mod := s.mods.byName[n]
			say("%s %s: %s", n, mod.info.Version, orText(mod.info.About, "(no description)"))
		}
		say("%d module(s). /MODULE INFO <name> shows more.", len(s.mods.order))
	case "INFO":
		if !needName() {
			return
		}
		mod := s.mods.byName[name]
		if mod == nil {
			say("%s is not loaded.", name)
			return
		}
		say("%s %s by %s: %s", name, mod.info.Version, orText(mod.info.Author, "(nobody named)"), orText(mod.info.About, "(no description)"))
		say("File: %s, loaded %s ago, %d calls, %d errors in the last minute", mod.path, time.Since(mod.loaded).Round(time.Second), mod.calls, len(mod.errs))
		var hooks []string
		for h := range mod.hooks {
			hooks = append(hooks, h)
		}
		sort.Strings(hooks)
		say("Hooks: %s", orText(strings.Join(hooks, ", "), "none"))
		say("Commands: %s", orText(strings.Join(mod.cmds, ", "), "none"))
		var rights []string
		for r := range mod.rights {
			rights = append(rights, r)
		}
		sort.Strings(rights)
		say("Rights: %s", orText(strings.Join(rights, ", "), "none (it may read and send notices)"))
	case "LOAD":
		if !needName() {
			return
		}
		if err := s.modLoad(name, false); err != nil {
			say("%s not loaded: %v", name, err)
			return
		}
		s.snotice("%s loaded the module %s", c.nick, name)
		say("%s is loaded.", name)
	case "UNLOAD":
		if !needName() {
			return
		}
		if err := s.modUnload(name, "unloaded by "+c.nick); err != nil {
			say("%s: %v", name, err)
			return
		}
		s.snotice("%s unloaded the module %s", c.nick, name)
		say("%s is unloaded.", name)
	case "RELOAD":
		if !needName() {
			return
		}
		old := s.mods.byName[name]
		if old == nil {
			say("%s is not loaded.", name)
			return
		}
		fromConf := old.fromConf
		s.modUnload(name, "reloaded by "+c.nick)
		if err := s.modLoad(name, fromConf); err != nil {
			say("%s was unloaded and the new file was not loaded: %v", name, err)
			return
		}
		s.snotice("%s reloaded the module %s", c.nick, name)
		say("%s is reloaded.", name)
	default:
		say("Use /MODULE LIST, INFO, LOAD, RELOAD or UNLOAD.")
	}
}

func orText(s, or string) string {
	if s == "" {
		return or
	}
	return s
}
