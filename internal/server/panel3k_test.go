package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
)

// Step 3k: places on the network map.
func TestPanelMapPlace(t *testing.T) {
	s, path, _ := fileServer(t, "")
	boss(t, s)
	f := func(v float64) *float64 { return &v }
	ask := func(r control.Request) *control.Response {
		r.Cmd, r.As, r.IP = control.CmdMapPlace, "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}
	self := s.cfg.Server.Name
	r := ask(control.Request{Name: strings.ToUpper(self), Value: "  Kuala   Lumpur ", Lat: f(3.13901234), Lon: f(101.68685)})
	if r.Done == nil || r.Done.Text != self+" is on the map: Kuala Lumpur." {
		t.Fatalf("place: %+v", r)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "[[map.place]]\nserver = \""+self+"\"\nname = \"Kuala Lumpur\"\nlat = 3.139\nlon = 101.6869") {
		t.Errorf("conf:\n%s", raw)
	}
	if c, err := config.Load(path); err != nil || len(c.Map.Places) != 1 {
		t.Errorf("conf loads: %v", err)
	}
	l := panelAsk(t, s, control.Request{Cmd: control.CmdLinks, As: "Boss", IP: "127.0.0.1"}).Links
	if l == nil || len(l.Places) != 1 || l.Places[0].Lat != 3.139 || l.Places[0].Name != "Kuala Lumpur" {
		t.Fatalf("links places: %+v", l)
	}
	// Moving it changes the one block.
	if r := ask(control.Request{Name: self, Value: "KL", Lat: f(3.2), Lon: f(101.7)}); r.Done == nil {
		t.Errorf("move: %+v", r)
	}
	raw, _ = os.ReadFile(path)
	if strings.Count(string(raw), "[[map.place]]") != 1 || !strings.Contains(string(raw), `name = "KL"`) {
		t.Errorf("after move:\n%s", raw)
	}
	for _, bad := range []struct {
		r    control.Request
		want string
	}{
		{control.Request{Name: "nowhere.example.net", Lat: f(1), Lon: f(1)}, "isn't a server of this network"},
		{control.Request{Name: self, Lat: f(91), Lon: f(1)}, "Lat is from -90 to 90."},
		{control.Request{Name: self, Lat: f(1), Lon: f(-181)}, "Lon is from -180 to 180."},
		{control.Request{Name: self, Value: `a "quoted" place`, Lat: f(1), Lon: f(1)}, "without quotes"},
		{control.Request{Name: self, Value: strings.Repeat("x", 61), Lat: f(1), Lon: f(1)}, "up to 60"},
	} {
		if r := ask(bad.r); r.Done != nil || !strings.Contains(r.Error, bad.want) {
			t.Errorf("%+v: %+v", bad.r, r)
		}
	}
	// No lat/lon takes it off.
	if r := ask(control.Request{Name: self}); r.Done == nil || !strings.Contains(r.Done.Text, "off the map") {
		t.Errorf("remove: %+v", r)
	}
	if r := ask(control.Request{Name: self}); !strings.Contains(r.Error, "isn't on the map") {
		t.Errorf("remove twice: %+v", r)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "map.place") {
		t.Errorf("block left:\n%s", raw)
	}
	ev := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLog, As: "Boss", IP: "127.0.0.1"}).Events
	if ev[0].What != "took "+self+" off the map" || ev[1].What != "put "+self+" on the map (KL)" {
		t.Errorf("events: %+v", ev[:2])
	}
}

// A hub places servers linked to it, and keeps them after a split.
func TestPanelMapPlaceLinked(t *testing.T) {
	hub, leaf := linkedPair(t)
	path := filepath.Join(t.TempDir(), "beeircd.conf")
	os.WriteFile(path, []byte(fileConf), 0o600)
	hub.s.onLoop(func() { hub.s.cfg.Path = path })
	po := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	lat, lon := 50.11, 8.68
	var r control.Response
	hub.s.onLoop(func() {
		r = hub.s.panelMapPlace(po, "127.0.0.1", control.Request{Name: "LEAF.test.local", Value: "Frankfurt", Lat: &lat, Lon: &lon})
	})
	if r.Done == nil || !strings.Contains(r.Done.Text, "leaf.test.local is on the map: Frankfurt") {
		t.Fatalf("place leaf: %+v", r)
	}
	var l *control.Links
	hub.s.onLoop(func() { l = hub.s.panelLinks() })
	if len(l.Places) != 1 || l.Places[0].Server != "leaf.test.local" {
		t.Errorf("places: %+v", l.Places)
	}
	_ = leaf
}
