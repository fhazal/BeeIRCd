package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// More of what a module can call: data it keeps, things it does to
// people and channels (each behind a right), and web requests.

const (
	modStoreMax   = 10 << 20 // bytes a module may keep in the database
	modStoreValue = 64 << 10 // bytes in one value
	modHTTPBusy   = 4        // web requests of one module at a time
	modHTTPBody   = 256 << 10
	modHTTPTime   = 10 * time.Second
)

// modKept is the module's saved data, read from the database once.
func (s *Server) modKept(m *module) (map[string]string, error) {
	if m.kept != nil {
		return m.kept, nil
	}
	if s.store == nil {
		return nil, errors.New("this server has no database")
	}
	all, err := s.store.ModuleData(m.name)
	if err != nil {
		return nil, err
	}
	m.kept, m.keptSize = all, 0
	for k, v := range all {
		m.keptSize += len(k) + len(v)
	}
	return all, nil
}

// A kept value is its kind and its text: s text, n number, t true, f false.
func keptEncode(v lua.LValue) (string, bool) {
	switch v := v.(type) {
	case lua.LString:
		return "s" + string(v), true
	case lua.LNumber:
		return "n" + v.String(), true
	case lua.LBool:
		if bool(v) {
			return "t", true
		}
		return "f", true
	}
	return "", false
}

func keptDecode(s string) lua.LValue {
	if s == "" {
		return lua.LNil
	}
	switch s[0] {
	case 's':
		return lua.LString(s[1:])
	case 'n':
		n, _ := strconv.ParseFloat(s[1:], 64)
		return lua.LNumber(n)
	case 't':
		return lua.LTrue
	case 'f':
		return lua.LFalse
	}
	return lua.LNil
}

// modSweep forgets data(...) tables of people and channels that are gone.
func (s *Server) modSweep() {
	for _, m := range s.mods.byName {
		for uid := range m.udata {
			if s.net.UserByUID(uid) == nil {
				delete(m.udata, uid)
			}
		}
		for name := range m.cdata {
			if s.net.Channel(name) == nil {
				delete(m.cdata, name)
			}
		}
	}
}

// modAPIMore gives m's Lua state the functions of this file.
func (s *Server) modAPIMore(m *module) {
	L := m.L
	fn := func(name string, f lua.LGFunction) { L.SetGlobal(name, L.NewFunction(f)) }
	need := func(L *lua.LState, right string) {
		if !m.rights[right] {
			L.RaiseError("this needs the right %q: add allow = [\"%s\"] to [module.%s] in beeircd.conf", right, right, m.name)
		}
	}

	// ---- store: kept across restarts ----
	st := L.NewTable()
	L.SetGlobal("store", st)
	kept := func(L *lua.LState) map[string]string {
		all, err := s.modKept(m)
		if err != nil {
			L.RaiseError("store: %s", err.Error())
		}
		return all
	}
	st.RawSetString("get", L.NewFunction(func(L *lua.LState) int {
		L.Push(keptDecode(kept(L)[L.CheckString(1)]))
		return 1
	}))
	st.RawSetString("set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		all := kept(L)
		if L.Get(2) == lua.LNil {
			if old, ok := all[key]; ok {
				if err := s.store.ModuleDelete(m.name, key); err != nil {
					L.RaiseError("store: %s", err.Error())
				}
				m.keptSize -= len(key) + len(old)
				delete(all, key)
			}
			return 0
		}
		val, ok := keptEncode(L.Get(2))
		switch {
		case !ok:
			L.ArgError(2, "store keeps a text, a number, true or false (use one key per value for a table)")
		case key == "" || len(key) > 200:
			L.ArgError(1, "a key is 1 to 200 characters")
		case len(val) > modStoreValue:
			L.ArgError(2, fmt.Sprintf("one value can be %d KB at most", modStoreValue>>10))
		}
		size := m.keptSize + len(val)
		if old, had := all[key]; had {
			size -= len(old)
		} else {
			size += len(key)
		}
		if size > modStoreMax {
			L.RaiseError("store: this module keeps %d MB already, the most it may", modStoreMax>>20)
		}
		if err := s.store.ModuleSet(m.name, key, val); err != nil {
			L.RaiseError("store: %s", err.Error())
		}
		all[key], m.keptSize = val, size
		return 0
	}))
	st.RawSetString("delete", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		all := kept(L)
		if old, ok := all[key]; ok {
			if err := s.store.ModuleDelete(m.name, key); err != nil {
				L.RaiseError("store: %s", err.Error())
			}
			m.keptSize -= len(key) + len(old)
			delete(all, key)
		}
		return 0
	}))
	st.RawSetString("keys", L.NewFunction(func(L *lua.LState) int {
		prefix := L.OptString(1, "")
		var keys []string
		for k := range kept(L) {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		t := L.NewTable()
		for _, k := range keys {
			t.Append(lua.LString(k))
		}
		L.Push(t)
		return 1
	}))

	// ---- data(user or channel): a table that lives as long as they do ----
	fn("data", func(L *lua.LState) int {
		v, ok := L.Get(1).(*lua.LTable)
		if !ok {
			L.ArgError(1, "a user or channel table is expected")
		}
		pick := func(have map[string]*lua.LTable, key string) *lua.LTable {
			if have[key] == nil {
				have[key] = L.NewTable()
			}
			return have[key]
		}
		if uid, isUser := v.RawGetString("uid").(lua.LString); isUser {
			if s.net.UserByUID(string(uid)) == nil {
				L.Push(L.NewTable()) // gone: a table nobody keeps
				return 1
			}
			if m.udata == nil {
				m.udata = map[string]*lua.LTable{}
			}
			L.Push(pick(m.udata, string(uid)))
			return 1
		}
		if name, isChan := v.RawGetString("name").(lua.LString); isChan {
			ch := s.net.Channel(string(name))
			if ch == nil {
				L.Push(L.NewTable())
				return 1
			}
			if m.cdata == nil {
				m.cdata = map[string]*lua.LTable{}
			}
			L.Push(pick(m.cdata, ch.Name))
			return 1
		}
		L.ArgError(1, "a user or channel table is expected")
		return 0
	})

	// ---- acting, each with a right ----
	fn("kick", func(L *lua.LState) int {
		need(L, "kick")
		ch, u := s.modArgChannel(L, 1), s.modArgUser(L, 2)
		reason := irc.Truncate(L.OptString(3, "Kicked"), s.cfg.Limits.KickLen)
		if ch != nil && u != nil && ch.Member(u) != nil && !u.Service {
			s.emit(&state.Kick{Source: "", UID: u.UID, Channel: ch.Name, Reason: irc.CleanFraming(reason)})
		}
		return 0
	})
	fn("set_topic", func(L *lua.LState) int {
		need(L, "topic")
		ch := s.modArgChannel(L, 1)
		text := irc.Truncate(modText(L, 2), s.cfg.Limits.TopicLen)
		if ch != nil && ch.Topic != text {
			s.emit(&state.Topic{Source: "", Channel: ch.Name, Text: text, By: s.cfg.Server.Name, At: time.Now()})
		}
		return 0
	})
	fn("force_join", func(L *lua.LState) int {
		need(L, "join")
		u, name := s.modArgUser(L, 1), L.CheckString(2)
		if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
			L.ArgError(2, "not a channel name")
		}
		if u != nil && !u.Service {
			host{s}.ForceJoin(u.UID, name)
		}
		return 0
	})
	fn("set_user_mode", func(L *lua.LState) int {
		need(L, "usermode")
		u, modes := s.modArgUser(L, 1), L.CheckString(2)
		if strings.ContainsAny(modes, "oO") {
			L.ArgError(2, "a module can't make or unmake opers")
		}
		if u != nil {
			host{s}.SetUserModes(u.UID, "", modes) // people on this server only
		}
		return 0
	})
	fn("ban", func(L *lua.LState) int {
		need(L, "ban")
		ip := ""
		switch v := L.Get(1).(type) {
		case *lua.LTable:
			if u := s.modArgUser(L, 1); u != nil {
				ip = u.IP
			}
		case lua.LString:
			ip = string(v)
		}
		if net.ParseIP(ip) == nil {
			L.ArgError(1, "a user table or an IP address is expected")
		}
		reason := irc.Truncate(L.OptString(2, "Banned"), 200)
		secs := float64(L.OptNumber(3, 3600))
		if secs < 60 || secs > 86400*30 {
			L.ArgError(3, "between 60 seconds and 30 days")
		}
		s.svc.OperBan(false, ip, reason, "module "+m.name, time.Duration(secs)*time.Second)
		return 0
	})

	// ---- web requests that don't hold the server up ----
	web := L.NewTable()
	L.SetGlobal("http", web)
	request := func(method string) lua.LGFunction {
		return func(L *lua.LState) int {
			need(L, "http")
			url := L.CheckString(1)
			body, typ := "", ""
			at := 2
			if method == "POST" {
				body = L.CheckString(2)
				at = 3
			}
			f := L.CheckFunction(at)
			if method == "POST" {
				typ = L.OptString(4, "application/json")
			}
			switch {
			case !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://"):
				L.ArgError(1, "an address that starts with https:// or http:// is expected")
			case m.httpBusy >= modHTTPBusy:
				L.RaiseError("http: %d requests of this module are still running", modHTTPBusy)
			case len(body) > modHTTPBody:
				L.ArgError(2, "the body is too big")
			}
			m.httpBusy++
			local := m.rights["http-local"]
			go func() {
				status, text, err := modFetch(method, url, body, typ, local)
				s.post(func() {
					m.httpBusy--
					if s.mods.byName[m.name] != m {
						return // unloaded meanwhile
					}
					var problem lua.LValue = lua.LNil
					if err != nil {
						problem = lua.LString(err.Error())
					}
					s.modCall(m, f, lua.LNumber(status), lua.LString(text), problem)
				})
			}()
			return 0
		}
	}
	web.RawSetString("get", L.NewFunction(request("GET")))
	web.RawSetString("post", L.NewFunction(request("POST")))
}

// modFetch does one web request for a module. Unless local is set it
// refuses addresses on this machine and on private networks: a module
// has no business with what only the server can reach.
func modFetch(method, url, body, typ string, local bool) (int, string, error) {
	dialer := &net.Dialer{Timeout: modHTTPTime}
	if !local {
		dialer.Control = func(network, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(h)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				return fmt.Errorf("%s is an address on this machine or a private network (the right \"http-local\" allows those)", h)
			}
			return nil
		}
	}
	client := &http.Client{
		Timeout:   modHTTPTime,
		Transport: &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: modHTTPTime},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), modHTTPTime)
	defer cancel()
	var rd io.Reader
	if method == "POST" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "BeeIRCd-module")
	if method == "POST" {
		req.Header.Set("Content-Type", typ)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, modHTTPBody))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(data), nil
}
