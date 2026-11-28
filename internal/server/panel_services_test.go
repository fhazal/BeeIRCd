package server

import (
	"os"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// The Services page uses AdminServ's settings with their checks.

func TestPanelServicesSettings(t *testing.T) {
	s, path, _ := fileServer(t, "")
	boss(t, s)
	ask := func(r control.Request) *control.Response {
		r.As, r.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}
	by := map[string]control.Setting{}
	for _, st := range ask(control.Request{Cmd: control.CmdSettings}).Settings {
		by[st.Name] = st
	}
	for _, n := range []string{"nick-expire", "chan-expire", "identify-within", "kill-quick", "allow-kill-immed", "ban-expiry", "guest-prefix", "log-channel", "log-connects"} {
		if st, ok := by[n]; !ok || st.Table != "services" {
			t.Errorf("%s: %+v", n, st)
		}
	}
	if by["allow-kill-immed"].Kind != "onoff" || by["nick-expire"].Kind != "time" || by["guest-prefix"].Kind != "text" {
		t.Errorf("kinds: %+v %+v %+v", by["allow-kill-immed"], by["nick-expire"], by["guest-prefix"])
	}
	bad := map[string]string{"nick-expire": "5m", "guest-prefix": "1bad", "log-channel": "nochan", "allow-kill-immed": "maybe", "identify-within": "soon"}
	for n, v := range bad {
		if r := ask(control.Request{Cmd: control.CmdSet, Name: n, Value: v}); r.Changed != nil || r.Error == "" {
			t.Errorf("%s=%s accepted: %+v", n, v, r)
		}
	}
	good := map[string]string{"nick-expire": "60d", "guest-prefix": "Bee", "log-channel": "#services", "allow-kill-immed": "on", "ban-expiry": "7d"}
	var ids []int64
	for n, v := range good {
		r := ask(control.Request{Cmd: control.CmdSet, Name: n, Value: v})
		if r.Changed == nil || r.Changed.New != v {
			t.Fatalf("%s=%s: %+v", n, v, r)
		}
		ids = append(ids, r.Changed.ID)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`nick_expire = "60d"`, `guest_prefix = "Bee"`, `log_channel = "#services"`, "allow_kill_immed = true", `ban_expiry = "7d"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s not saved:\n%s", want, raw)
		}
	}
	for _, id := range ids {
		if r := ask(control.Request{Cmd: control.CmdUndo, ID: id}); r.Changed == nil {
			t.Errorf("undo %d: %+v", id, r)
		}
	}
	if r := ask(control.Request{Cmd: control.CmdSet, Name: "log-channel", Value: "off"}); r.Changed == nil || r.Changed.New != "off" {
		t.Errorf("log-channel off: %+v", r)
	}
}

// log-events: the events in the services log, as a list.
func TestLogEventsSetting(t *testing.T) {
	s, path, _ := fileServer(t, "")
	boss(t, s)
	ask := func(r control.Request) *control.Response {
		r.As, r.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}
	set := func(v string) *control.Response {
		return ask(control.Request{Cmd: control.CmdSet, Name: "log-events", Value: v})
	}
	saved := func() string { raw, _ := os.ReadFile(path); return string(raw) }
	find := func() control.Setting {
		for _, st := range ask(control.Request{Cmd: control.CmdSettings}).Settings {
			if st.Name == "log-events" {
				return st
			}
		}
		t.Fatal("no log-events setting")
		return control.Setting{}
	}

	// Unset: the default list, without umode, host and channel.
	st := find()
	if st.Kind != "events" || st.Table != "services" || st.Value != "connect,quit,nick,oper,kill,server,bans,other" || len(st.Choices) != 12 {
		t.Fatalf("default: %+v", st)
	}
	if r := set("kill, Bans umode"); r.Changed == nil || r.Changed.New != "kill,bans,umode" {
		t.Fatalf("list: %+v", r)
	}
	if !strings.Contains(saved(), `log_events = ["kill", "bans", "umode"]`) {
		t.Errorf("not saved:\n%s", saved())
	}
	if r := set("*"); r.Changed == nil || r.Changed.New != "*" || !strings.Contains(saved(), `log_events = ["*"]`) {
		t.Fatalf("*: %+v\n%s", r, saved())
	}
	// Ticking exactly the default events again removes the key.
	if r := set("oper, connect,quit,nick,kill,server,bans,other"); r.Changed == nil || r.Changed.New != "connect,quit,nick,oper,kill,server,bans,other" || strings.Contains(saved(), "log_events") {
		t.Fatalf("default list: %+v\n%s", r, saved())
	}
	if r := set("*"); r.Changed == nil {
		t.Fatalf("*: %+v", r)
	}
	if r := set("off"); r.Changed == nil || r.Changed.New != "off" {
		t.Fatalf("off: %+v", r)
	}
	for _, bad := range []string{"kill,bogus", "", "  "} {
		r := set(bad)
		if r.Changed != nil || r.Error == "" {
			t.Errorf("%q accepted: %+v", bad, r)
		}
		if bad == "kill,bogus" && !strings.Contains(r.Error, `"bogus" is not an event`) {
			t.Errorf("error: %q", r.Error)
		}
	}
	if r := set("DEFAULT"); r.Changed == nil || r.Changed.New != "connect,quit,nick,oper,kill,server,bans,other" {
		t.Fatalf("default again: %+v", r)
	}
	if strings.Contains(saved(), "log_events") {
		t.Errorf("DEFAULT left the key:\n%s", saved())
	}
	if st := find(); st.Value != "connect,quit,nick,oper,kill,server,bans,other" {
		t.Errorf("after DEFAULT: %+v", st)
	}
	// Undo puts the earlier value back.
	r := set("nick")
	if r.Changed == nil {
		t.Fatalf("nick: %+v", r)
	}
	if u := ask(control.Request{Cmd: control.CmdUndo, ID: r.Changed.ID}); u.Changed == nil || u.Changed.New != "connect,quit,nick,oper,kill,server,bans,other" {
		t.Errorf("undo: %+v", u)
	}
	// It was unset, so undo unsets it again rather than writing the default list.
	if strings.Contains(saved(), "log_events") {
		t.Errorf("undo wrote the default list:\n%s", saved())
	}
	// A set value is put back as it was.
	set("kill")
	r = set("nick")
	if u := ask(control.Request{Cmd: control.CmdUndo, ID: r.Changed.ID}); u.Changed == nil || u.Changed.New != "kill" || !strings.Contains(saved(), `log_events = ["kill"]`) {
		t.Errorf("undo to a set value: %+v\n%s", u, saved())
	}
}
