package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestMapPlaceFromIP(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		if r.URL.RawQuery != "" || r.URL.Path != "/json" || r.Header.Get("Cookie") != "" {
			t.Errorf("request carried more than our own connection: %s %v", r.URL, r.Header)
		}
		w.Write([]byte(`{"ip":"203.0.113.7","city":"Kuala Lumpur","region":"Kuala Lumpur","country":"MY","loc":"3.1412,101.6865"}`))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "map-place.json")
	now := time.Now()
	p, err := lookupIPPlace(file, srv.URL+"/json", now)
	if err != nil || p.Name != "Kuala Lumpur" || p.Lat != 3.1 || p.Lon != 101.7 {
		t.Fatalf("lookup: %+v %v", p, err)
	}
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), "203.0.113") {
		t.Errorf("the IP address was kept: %s", raw)
	}
	// Kept for a month: no second ask.
	if p, err := lookupIPPlace(file, srv.URL+"/json", now.Add(29*24*time.Hour)); err != nil || p.Name != "Kuala Lumpur" || asked != 1 {
		t.Errorf("kept: %+v %v asked %d", p, err, asked)
	}
	if _, err := lookupIPPlace(file, srv.URL+"/json", now.Add(31*24*time.Hour)); err != nil || asked != 2 {
		t.Errorf("after a month: %v asked %d", err, asked)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"loc":"999,1"}`)) }))
	defer bad.Close()
	if _, err := lookupIPPlace("", bad.URL, now); err == nil {
		t.Error("a place off the earth was taken")
	}

	// On a server: the IP place first, the switch turns it off (time zone
	// then), the owner's place wins over both.
	placeLookupURL = srv.URL + "/json"
	defer func() { placeLookupURL = "" }()
	s, _, _ := fileServer(t, "")
	s.onLoop(func() { s.links.zone = "Europe/Berlin"; s.links.ipAsked = false; s.startPlaceLookup() })
	waitLink(t, "IP place", func() bool { p, _ := placeOf(s, s.cfg.Server.Name); return p.Name == "Kuala Lumpur" })
	if p, _ := placeOf(s, s.cfg.Server.Name); p.From != "its IP address (ipinfo.io)" {
		t.Errorf("from: %+v", p)
	}
	po := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	var r control.Response
	s.onLoop(func() { r = s.panelSet(po, "127.0.0.1", "find-place-by-ip", "off") })
	if p, _ := placeOf(s, s.cfg.Server.Name); r.Changed == nil || p.Name != "Berlin" {
		t.Errorf("switched off: %+v %+v", r, p)
	}
	var l *control.Links
	s.onLoop(func() { l = s.panelLinks() })
	if l.FindByIP {
		t.Error("links says IP places are on")
	}
}

// Taking the owner's place off (panel or rehash) starts the IP lookup the
// owner's place had held back, and the server keeps a place.
func TestMapPlaceOwnerOffFindsIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"city":"Nuremberg","country":"DE","loc":"49.4521,11.0767"}`))
	}))
	defer srv.Close()
	placeLookupURL = srv.URL + "/json"
	defer func() { placeLookupURL = "" }()
	s, path, _ := fileServer(t, "\n[[map.place]]\nserver = \"irc.test.local\"\nname = \"Kuala Lumpur\"\nlat = 3.139\nlon = 101.6869\n")
	name := s.cfg.Server.Name
	po := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	var r control.Response
	s.onLoop(func() { s.links.zone = "UTC" })
	if p, _ := placeOf(s, name); p.Name != "Kuala Lumpur" {
		t.Fatalf("owner place: %+v", p)
	}
	s.onLoop(func() { r = s.panelMapPlace(po, "127.0.0.1", control.Request{Name: name}) })
	if r.Done == nil || !strings.Contains(r.Done.Text, "until its IP address") {
		t.Errorf("take off: %+v", r)
	}
	waitLink(t, "IP place after take off", func() bool { p, _ := placeOf(s, name); return p.Name == "Nuremberg" })

	// Rehash: owner's place back in the file wins, gone again -> the IP place.
	raw, _ := os.ReadFile(path)
	conf := string(raw) + "\n[[map.place]]\nserver = \"" + name + "\"\nname = \"Penang\"\nlat = 5.4\nlon = 100.3\n"
	os.WriteFile(path, []byte(conf), 0o600)
	s.onLoop(func() { _, _ = s.rehash("test") })
	if p, _ := placeOf(s, name); p.Name != "Penang" {
		t.Errorf("after rehash with place: %+v", p)
	}
	os.WriteFile(path, raw, 0o600)
	s.onLoop(func() { s.links.ipAsked, s.links.ipPlace = false, nil; _, _ = s.rehash("test") })
	waitLink(t, "IP place after rehash", func() bool { p, _ := placeOf(s, name); return p.Name == "Nuremberg" })
}
