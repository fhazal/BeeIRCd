package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// Step 3d: the Opers page.

func TestPanelOpers(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	for _, n := range []string{"ember", "fern", "gale"} {
		registered(t, s, n)
	}
	// fern: a non-owner admin with a h (no k) to check "only flags you have".
	b.send("AS OPER fern ADD FLAGS ah HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("fern is an oper now")

	ask := func(as string, r control.Request) *control.Response {
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}

	// List: owner first and locked, then the entries.
	o := ask("Boss", control.Request{Cmd: control.CmdOpers}).Opers
	if o == nil || len(o.Rows) != 2 || !o.Rows[0].Owner || o.Rows[0].All != "acghks" || o.Rows[1].Account != "fern" || o.Rows[1].All != "ah" || len(o.Classes) < 4 {
		t.Fatalf("opers: %+v", o)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdOperSave, Name: "Boss", Flags: "g", Hosts: "*@*"}); !strings.Contains(r.Error, "owner") {
		t.Errorf("owner changed: %+v", r)
	}

	// Add: by class, by own flags; checks.
	add := func(as string, r control.Request) *control.Response {
		r.Cmd, r.New = control.CmdOperSave, true
		return ask(as, r)
	}
	if r := add("Boss", control.Request{Name: "nobody", Class: "helper", Hosts: "*@*"}); !strings.Contains(r.Error, "isn't a registered nick") {
		t.Errorf("unregistered: %+v", r)
	}
	if r := add("Boss", control.Request{Name: "ember", Hosts: "*@*"}); !strings.Contains(r.Error, "class or at least one flag") {
		t.Errorf("no flags: %+v", r)
	}
	if r := add("Boss", control.Request{Name: "ember", Flags: "x", Hosts: "*@*"}); !strings.Contains(r.Error, "x isn't an oper flag") {
		t.Errorf("bad flag: %+v", r)
	}
	if r := add("Boss", control.Request{Name: "ember", Flags: "g", Hosts: "a@b@c"}); !strings.Contains(r.Error, "Allowed from") {
		t.Errorf("bad host: %+v", r)
	}
	if r := add("fern", control.Request{Name: "ember", Class: "globop", Hosts: "*@*"}); !strings.Contains(r.Error, "can't give flag k") {
		t.Errorf("fern gives k through a class: %+v", r)
	}
	r := add("Boss", control.Request{Name: "ember", Class: "globop", Flags: "g", Hosts: "*@127.0.0.0/8, *@*.example.org", Password: "Ember-Panel-1"})
	if r.Changed == nil || r.Changed.New != "ghk" || r.Changed.ID == 0 {
		t.Fatalf("add ember: %+v", r)
	}
	if r := add("Boss", control.Request{Name: "ember", Flags: "g", Hosts: "*@*"}); !strings.Contains(r.Error, "already") {
		t.Errorf("add twice: %+v", r)
	}
	// The password works for /oper and the panel.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: "Ember-Panel-1", IP: "127.0.0.1"}); r.Oper == nil || r.Oper.Flags != "ghk" {
		t.Errorf("ember signs in: %+v", r)
	}

	// Change: fern (a h) may not take ember's k away, nor remove ember.
	save := func(as string, r control.Request) *control.Response { r.Cmd = control.CmdOperSave; return ask(as, r) }
	if r := save("fern", control.Request{Name: "ember", Flags: "g", Hosts: "*@*"}); !strings.Contains(r.Error, "can't take flag k away") {
		t.Errorf("fern takes k: %+v", r)
	}
	if r := ask("fern", control.Request{Cmd: control.CmdOperDel, Name: "ember"}); !strings.Contains(r.Error, "they have flags g and k and you don't") {
		t.Errorf("fern removes ember: %+v", r)
	}
	// Nobody locks themselves out.
	if r := save("fern", control.Request{Name: "fern", Flags: "h", Hosts: "*@*"}); !strings.Contains(r.Error, "flag a from yourself") {
		t.Errorf("fern drops own a: %+v", r)
	}
	if r := ask("fern", control.Request{Cmd: control.CmdOperDel, Name: "fern"}); !strings.Contains(r.Error, "remove yourself") {
		t.Errorf("fern removes self: %+v", r)
	}
	if r := save("Boss", control.Request{Name: "ember", Class: "globop", Flags: "g", Hosts: "*@127.0.0.0/8 *@*.example.org"}); r.Error != "Nothing changed." {
		t.Errorf("no change: %+v", r)
	}

	// Change + undo through HISTORY.
	r = save("Boss", control.Request{Name: "ember", Class: "helper", Hosts: "*@*"})
	if r.Changed == nil || r.Changed.Old != "ghk" || r.Changed.New != "h" {
		t.Fatalf("change: %+v", r)
	}
	if u := ask("Boss", control.Request{Cmd: control.CmdUndo, ID: r.Changed.ID}); u.Changed == nil || u.Changed.New != "ghk" {
		t.Errorf("undo change: %+v", u)
	}
	b.send("AS HISTORY")
	b.expect("Boss (Hive Panel)")

	// Remove + undo brings the password back.
	r = ask("Boss", control.Request{Cmd: control.CmdOperDel, Name: "ember"})
	if r.Changed == nil || r.Changed.ID == 0 {
		t.Fatalf("remove: %+v", r)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: "Ember-Panel-1", IP: "127.0.0.1"}); r.Oper != nil {
		t.Errorf("removed oper signs in: %+v", r)
	}
	if u := ask("Boss", control.Request{Cmd: control.CmdUndo, ID: r.Changed.ID}); u.Changed == nil || u.Changed.New != "ghk" {
		t.Errorf("undo remove: %+v", u)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: "Ember-Panel-1", IP: "127.0.0.1"}); r.Oper == nil {
		t.Errorf("password not back after undo: %+v", r)
	}

	// Flag a for all of it.
	if r := ask("ember", control.Request{Cmd: control.CmdOpers}); r.Denied != "flag" {
		t.Errorf("opers without a: %+v", r)
	}
	ev := ask("Boss", control.Request{Cmd: control.CmdPanelLog}).Events
	if ev[0].Who != "ember" || ev[1].What != "put the oper entry of ember back" || ev[3].What != "removed ember as an oper" {
		t.Errorf("events: %+v", ev[:4])
	}
}

func TestFlagWords(t *testing.T) {
	for in, want := range map[string]string{"k": "flag k", "gk": "flags g and k", "ack": "flags a, c and k"} {
		if got := flagWords(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
