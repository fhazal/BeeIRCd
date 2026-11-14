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
