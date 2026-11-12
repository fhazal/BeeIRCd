package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

func placeOf(s *Server, name string) (control.MapPlace, bool) {
	var out control.MapPlace
	var ok bool
	s.onLoop(func() {
		for _, p := range s.mapPlaces() {
			if strings.EqualFold(p.Server, name) {
				out, ok = p, true
			}
		}
	})
	return out, ok
}

// Servers place themselves from their time zone and tell each other; the
// owner's [[map.place]] wins; a server set to UTC stays off the map.
func TestMapPlacesFromTimeZone(t *testing.T) {
	hub, leaf := linkedPair(t)
	hub.s.onLoop(func() { hub.s.links.zone = "Europe/Berlin"; hub.s.announcePlace() })
	leaf.s.onLoop(func() { leaf.s.links.zone = "Asia/Kuala_Lumpur"; leaf.s.announcePlace() })
	waitLink(t, "places heard", func() bool {
		a, ok1 := placeOf(hub.s, "leaf.test.local")
		b, ok2 := placeOf(leaf.s, "hub.test.local")
		return ok1 && ok2 && a.Name == "Kuala Lumpur" && b.Name == "Berlin"
	})
	if p, _ := placeOf(hub.s, "leaf.test.local"); p.From != "its time zone" || p.Lat != 3.1667 || p.Lon != 101.7 {
		t.Errorf("hub sees the leaf at %+v", p)
	}
	if p, _ := placeOf(hub.s, "hub.test.local"); p.Name != "Berlin" || p.From != "its time zone" {
		t.Errorf("hub's own place %+v", p)
	}

	// The hub's owner moves the leaf: their place wins on the hub.
	path := filepath.Join(t.TempDir(), "beeircd.conf")
	os.WriteFile(path, []byte(fileConf), 0o600)
	hub.s.onLoop(func() { hub.s.cfg.Path = path })
	po := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	lat, lon := 1.35, 103.82
	var r control.Response
	hub.s.onLoop(func() {
		r = hub.s.panelMapPlace(po, "127.0.0.1", control.Request{Name: "leaf.test.local", Value: "Singapore", Lat: &lat, Lon: &lon})
	})
	if p, _ := placeOf(hub.s, "leaf.test.local"); r.Done == nil || p.Name != "Singapore" || p.From != "" {
		t.Errorf("owner's place: %+v %+v", r, p)
	}
	// Taking it off puts it back where its time zone says.
	hub.s.onLoop(func() { r = hub.s.panelMapPlace(po, "127.0.0.1", control.Request{Name: "leaf.test.local"}) })
	if p, _ := placeOf(hub.s, "leaf.test.local"); r.Done == nil || !strings.Contains(r.Done.Text, "back where its time zone puts it: Kuala Lumpur") || p.Name != "Kuala Lumpur" {
		t.Errorf("back to the time zone: %+v %+v", r, p)
	}

	// The hub places itself by hand: the leaf hears "its owner".
	lat, lon = 49.45, 11.08
	hub.s.onLoop(func() {
		r = hub.s.panelMapPlace(po, "127.0.0.1", control.Request{Name: "hub.test.local", Value: "Nuremberg", Lat: &lat, Lon: &lon})
	})
	waitLink(t, "leaf hears the move", func() bool { p, _ := placeOf(leaf.s, "hub.test.local"); return p.Name == "Nuremberg" })
	if p, _ := placeOf(leaf.s, "hub.test.local"); p.From != "its owner" {
		t.Errorf("leaf sees %+v", p)
	}

	// A server only places itself: a line about another server is dropped.
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			hub.s.remotePlace(l, "0LF", []string{"hub.test.local", "0", "0", "tz", "Null Island"})
		}
	})
	if p, _ := placeOf(hub.s, "hub.test.local"); p.Name != "Nuremberg" {
		t.Errorf("leaf moved the hub: %+v", p)
	}
}

func TestMapPlaceUTC(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.onLoop(func() { s.links.zone = "Etc/UTC" })
	if p, ok := placeOf(s, s.cfg.Server.Name); ok {
		t.Errorf("UTC server placed: %+v", p)
	}
}
