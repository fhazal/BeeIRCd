package server

import (
	"slices"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// The Connect classes card: classes in file order, default last, people
// counted, and which limits the class set itself.
func TestPanelClasses(t *testing.T) {
	s, _, _ := fileServerWith(t, classConf, nil)
	b := boss(t, s) // unknown-users when the class is picked: guests
	e := registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect(asLine("Boss", "ember is an oper now: class globop, hk (hosts, kill), from *@127.0.0.0/8."))
	_ = e
	s1 := login(t, s, "s1") // small (max_users 1)
	s1.sync()

	r := panelAsk(t, s, control.Request{Cmd: control.CmdClasses, As: "Boss", IP: "127.0.0.1"})
	if r.Classes == nil || len(r.Classes.Classes) != 4 {
		t.Fatalf("classes: %+v", r)
	}
	var names []string
	for _, k := range r.Classes.Classes {
		names = append(names, k.Name)
	}
	if !slices.Equal(names, []string{"nope", "small", "guests", "default"}) {
		t.Errorf("order: %v", names)
	}
	nope, small, guests, def := r.Classes.Classes[0], r.Classes.Classes[1], r.Classes.Classes[2], r.Classes.Classes[3]

	if !nope.Deny || nope.People != 0 || !slices.Equal(nope.Groups, []string{"blocked"}) || nope.Default {
		t.Errorf("deny class: %+v", nope)
	}
	// small sets only max_users: everything else is from [limits].
	if small.People != 1 || small.MaxUsers != 1 || small.Deny {
		t.Errorf("small: %+v", small)
	}
	for _, l := range []control.ClassLimit{small.SessionLimit, small.SendQ, small.PingInterval, small.FloodBurst, small.FloodRate, small.MaxChannels} {
		if l.Own || l.Value == 0 {
			t.Errorf("small should inherit a [limits] value: %+v", small)
		}
	}
	// guests: Boss and ember, with every limit the class sets itself.
	if guests.People != 2 || guests.MaxUsers != 0 || !slices.Equal(guests.Groups, []string{"unknown-users"}) {
		t.Errorf("guests: %+v", guests)
	}
	want := map[string]control.ClassLimit{
		"session": {Value: 3, Own: true}, "sendq": {Value: 8192, Own: true}, "ping": {Value: 30, Own: true},
		"burst": {Value: 50, Own: true}, "rate": {Value: 20, Own: true}, "channels": {Value: 2, Own: true},
	}
	got := map[string]control.ClassLimit{
		"session": guests.SessionLimit, "sendq": guests.SendQ, "ping": guests.PingInterval,
		"burst": guests.FloodBurst, "rate": guests.FloodRate, "channels": guests.MaxChannels,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("guests %s: %+v, want %+v", k, got[k], w)
		}
	}
	// default: [limits] as it is, nothing set by a class.
	if !def.Default || len(def.Groups) != 0 || def.People != 0 || def.MaxUsers != 0 || def.SessionLimit.Own || def.SessionLimit.Value != 100 {
		t.Errorf("default: %+v", def)
	}

	// An oper without flag a is refused.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdClasses, As: "ember", IP: "127.0.0.1"}); r.Denied != "flag" || r.Classes != nil {
		t.Errorf("classes for ember: %+v", r)
	}
}

// Without [[class]] blocks the card has the default class only.
func TestPanelClassesNone(t *testing.T) {
	s, _, _ := fileServer(t, "")
	boss(t, s)
	r := panelAsk(t, s, control.Request{Cmd: control.CmdClasses, As: "Boss", IP: "127.0.0.1"})
	if r.Classes == nil || len(r.Classes.Classes) != 1 || r.Classes.Classes[0].Name != "default" || !r.Classes.Classes[0].Default || r.Classes.Classes[0].People != 1 {
		t.Errorf("classes: %+v", r.Classes)
	}
}
