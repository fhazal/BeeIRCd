// Package luamod holds what BeeIRCd modules are made of, apart from the
// server itself: the Lua sandbox, the names of the hooks and functions a
// module may use, and the check behind `beeircd module check`.
package luamod

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// API is the version of the module interface. A module that asks for a
// newer one is refused.
const API = 1

// Kind says when a hook runs.
type Kind int

const (
	Event Kind = iota + 1 // after something happened; can't stop it
	Check                 // before something happens; may refuse or change it
)

// Hook is one hook a module can register with on(...).
type Hook struct {
	Kind  Kind
	Built bool // false: named in the design, not there yet in this version
	About string
}

// Hooks are all hook names.
var Hooks = map[string]Hook{
	"load":            {Event, true, "this module was loaded"},
	"unload":          {Event, true, "this module is about to be unloaded"},
	"rehash":          {Event, true, "beeircd.conf was read again; config is fresh"},
	"tick":            {Event, true, "once a second"},
	"connect":         {Event, true, "someone joined the network"},
	"quit":            {Event, true, "someone left the network"},
	"nick":            {Event, true, "someone changed nick"},
	"join":            {Event, true, "someone joined a channel"},
	"part":            {Event, true, "someone left a channel"},
	"kick":            {Event, true, "someone was kicked"},
	"topic":           {Event, true, "a topic was set"},
	"channel_mode":    {Event, true, "channel modes changed"},
	"user_mode":       {Event, true, "user modes changed"},
	"away":            {Event, true, "someone went away"},
	"back":            {Event, true, "someone came back"},
	"login":           {Event, true, "someone logged in to an account"},
	"logout":          {Event, true, "someone logged out"},
	"host_change":     {Event, true, "someone's shown host changed"},
	"realname_change": {Event, true, "someone's real name changed"},
	"invite":          {Event, true, "someone was invited"},
	"message":         {Event, true, "a message from someone on this server was delivered"},
	"check_message":   {Check, true, "a message from someone on this server, before it is delivered"},

	"tagmsg":           {Event, false, "a tag-only message was delivered"},
	"channel_create":   {Event, false, "a channel was made"},
	"channel_destroy":  {Event, false, "a channel emptied"},
	"account_register": {Event, false, "an account was registered"},
	"account_drop":     {Event, false, "an account was dropped"},
	"channel_register": {Event, false, "a channel was registered"},
	"channel_drop":     {Event, false, "a channel was dropped"},
	"oper":             {Event, false, "someone became an oper"},
	"deoper":           {Event, false, "someone stopped being an oper"},
	"server_link":      {Event, false, "a server joined"},
	"server_split":     {Event, false, "a server left"},
	"ban_add":          {Event, false, "a network ban was added"},
	"ban_remove":       {Event, false, "a network ban was removed"},
	"check_connect":    {Check, true, "a new connection, before it is let in"},
	"check_nick":       {Check, true, "a nick change"},
	"check_join":       {Check, true, "a join"},
	"check_create":     {Check, true, "making a new channel"},
	"check_tagmsg":     {Check, true, "a tag-only message"},
	"check_part":       {Check, true, "a part message"},
	"check_quit":       {Check, true, "a quit message"},
	"check_topic":      {Check, true, "a topic change"},
	"check_kick":       {Check, true, "a kick"},
	"check_mode":       {Check, true, "a channel mode change"},
	"check_invite":     {Check, true, "an invite"},
	"check_knock":      {Check, true, "a knock"},
	"check_away":       {Check, true, "an away message"},
	"check_command":    {Check, true, "any command"},
	"check_oper":       {Check, true, "/oper"},
	"check_whois":      {Check, true, "a WHOIS answer"},
	"check_list":       {Check, true, "a channel in LIST"},
	"check_names":      {Check, true, "a member in NAMES or WHO"},
}

// Functions are the functions the server gives a module.
var Functions = []string{
	"module", "on", "command",
	"notice", "numeric", "fail", "snotice", "log",
	"find_user", "find_channel", "users", "channels", "members",
	"after", "every", "has_right", "data",
	"kill", "kick", "set_topic", "force_join", "set_user_mode", "ban",
}

// Tables are the tables of functions the server gives a module.
var Tables = map[string][]string{
	"store": {"get", "set", "delete", "keys"},
	"http":  {"get", "post"},
}

// Rights are what [module.NAME] allow can list.
var Rights = map[string]string{
	"kill":       "disconnect people",
	"kick":       "kick people from channels",
	"topic":      "set channel topics",
	"join":       "make people join channels",
	"usermode":   "set user modes on people of this server",
	"ban":        "ban an IP address from the network for a time",
	"http":       "make web requests to the internet",
	"http-local": "make web requests to this machine and private networks too",
}

var nameOK = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidName reports whether name can be a module's name (and file name).
func ValidName(name string) bool { return nameOK.MatchString(name) }

var commandOK = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,31}$`)

// ValidCommand reports whether name can be a module's command.
func ValidCommand(name string) bool { return commandOK.MatchString(name) }

// maxRep caps string.rep, the one easy way to ask for a huge string.
const maxRep = 1 << 20

// NewState makes a Lua state a module can't escape from: no files, no
// programs, no loading of other code.
func NewState() *lua.LState {
	L := lua.NewState(lua.Options{
		SkipOpenLibs:        true,
		CallStackSize:       128,
		RegistrySize:        1024,
		RegistryMaxSize:     1024 * 256,
		RegistryGrowStep:    1024,
		MinimizeStackMemory: true,
	})
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
	for _, gone := range []string{"dofile", "loadfile", "load", "loadstring", "require", "collectgarbage", "newproxy", "getfenv", "setfenv", "_printregs", "print"} {
		L.SetGlobal(gone, lua.LNil)
	}
	if str, ok := L.GetGlobal("string").(*lua.LTable); ok {
		str.RawSetString("dump", lua.LNil)
		str.RawSetString("rep", L.NewFunction(func(L *lua.LState) int {
			s, n := L.CheckString(1), L.CheckInt(2)
			if n < 0 {
				n = 0
			}
			if len(s)*n > maxRep {
				L.RaiseError("string.rep: the result would be over %d bytes", maxRep)
			}
			L.Push(lua.LString(strings.Repeat(s, n)))
			return 1
		}))
	}
	return L
}

// Run compiles src as file and runs it in L, for at most limit.
func Run(L *lua.LState, file, src string, limit time.Duration) error {
	fn, err := L.Load(strings.NewReader(src), file)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	L.SetContext(ctx)
	defer L.RemoveContext()
	err = L.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true})
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%s ran longer than %s", file, limit)
	}
	return err
}

// Info is what a module says about itself in module { ... }.
type Info struct {
	Name, Version, Author, About string
	API                          int
}

// ReadInfo reads the table given to module { ... }.
func ReadInfo(t *lua.LTable) Info {
	str := func(k string) string {
		if s, ok := t.RawGetString(k).(lua.LString); ok {
			return string(s)
		}
		if n, ok := t.RawGetString(k).(lua.LNumber); ok {
			return n.String()
		}
		return ""
	}
	in := Info{Name: str("name"), Version: str("version"), Author: str("author"), About: str("about"), API: 1}
	if n, ok := t.RawGetString("api").(lua.LNumber); ok {
		in.API = int(n)
	}
	return in
}

// CheckSource looks at a module's source without a server and returns what is
// wrong with it, in plain words. name is the file name without ".lua".
func CheckSource(name, src string) []string {
	var bad []string
	add := func(format string, a ...any) { bad = append(bad, fmt.Sprintf(format, a...)) }
	if !ValidName(name) {
		add("the file name %q can't be a module name: use small letters, digits, - and _, starting with a letter", name)
	}
	L := NewState()
	defer L.Close()
	var info *Info
	hooks, cmds := map[string]bool{}, map[string]bool{}
	nop := func(L *lua.LState) int { return 0 }
	for _, f := range Functions {
		L.SetGlobal(f, L.NewFunction(nop))
	}
	L.SetGlobal("config", L.NewTable())
	for name, fns := range Tables {
		t := L.NewTable()
		for _, f := range fns {
			t.RawSetString(f, L.NewFunction(nop))
		}
		L.SetGlobal(name, t)
	}
	L.SetGlobal("module", L.NewFunction(func(L *lua.LState) int {
		in := ReadInfo(L.CheckTable(1))
		info = &in
		return 0
	}))
	L.SetGlobal("on", L.NewFunction(func(L *lua.LState) int {
		h := L.CheckString(1)
		L.CheckFunction(2)
		hooks[h] = true
		return 0
	}))
	L.SetGlobal("command", L.NewFunction(func(L *lua.LState) int {
		c := L.CheckString(1)
		if L.GetTop() < 3 {
			L.RaiseError("command(%q, {...}, function): three things are needed", c)
		}
		L.CheckTable(2)
		L.CheckFunction(3)
		cmds[c] = true
		return 0
	}))
	if err := Run(L, name+".lua", src, time.Second); err != nil {
		add("%s", cleanError(err))
		return bad
	}
	switch {
	case info == nil:
		add("no module { name = ..., version = ... } line at the top")
	case info.Name != name:
		add("module { name = %q } does not match the file name %s.lua", info.Name, name)
	case info.API > API:
		add("it asks for module interface %d; this BeeIRCd has %d", info.API, API)
	}
	if info != nil && info.Version == "" {
		add("module { ... } has no version")
	}
	for _, h := range sorted(hooks) {
		hk, ok := Hooks[h]
		switch {
		case !ok:
			add("on(%q, ...): there is no such hook%s", h, suggest(h))
		case !hk.Built:
			add("on(%q, ...): this hook is planned but not in this version yet", h)
		}
	}
	for _, c := range sorted(cmds) {
		if !ValidCommand(strings.ToUpper(c)) {
			add("command(%q, ...): use letters and digits only, starting with a letter", c)
		}
	}
	if len(hooks) == 0 && len(cmds) == 0 && len(bad) == 0 {
		add("it registers no hook and no command, so it would do nothing")
	}
	return bad
}

// cleanError turns a Lua error into one plain line.
func cleanError(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\nstack traceback"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

// CleanError is cleanError for the server.
func CleanError(err error) string { return cleanError(err) }

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// suggest names a hook close to the unknown one.
func suggest(h string) string {
	for name := range Hooks {
		if strings.EqualFold(name, h) || name == strings.TrimPrefix(h, "on_") || name == strings.ReplaceAll(h, "-", "_") {
			return " (did you mean " + name + "?)"
		}
	}
	return ""
}

// Starter is the file `beeircd module new NAME` writes.
func Starter(name string) string {
	return `-- ` + name + `.lua: a BeeIRCd module.
-- Load it by adding "` + name + `" to [modules] load in beeircd.conf, then
-- /REHASH, or try it at once with /MODULE LOAD ` + name + `.
-- Check it first with: beeircd module check ` + name + `.lua

module { name = "` + name + `", version = "0.1", author = "", about = "What this module does" }

-- Settings come from the [module.` + name + `] block of beeircd.conf:
--   [module.` + name + `]
--   text = "Hello"
local text = config.text or "Hello"

-- Told after someone joined a channel. e.user and e.channel are tables.
on("join", function(e)
  if e.channel.name == "#lobby" then
    notice(e.user, text .. ", " .. e.user.nick .. "!")
  end
end)

-- A command of your own: /HELLO <nick>
command("HELLO", { params = 1, help = "HELLO <nick>: says hello to someone" }, function(user, args)
  local to = find_user(args[1])
  if not to then
    return fail(user, "No such nick: " .. args[1])
  end
  notice(to, user.nick .. " says hello")
end)
`
}
