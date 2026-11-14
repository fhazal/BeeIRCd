package config

import (
	"os"
	"strings"
	"testing"
)

func TestMapPlaces(t *testing.T) {
	conf := good + `
[[map.place]]
server = "eu.example.net"
name = "Frankfurt"
lat = 50
lon = 8.68

[[map.place]]
server = "irc.example.net"
name = "Kuala Lumpur"
lat = 3.14
lon = 101.69
`
	c, err := Load(write(t, conf))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Map.Places) != 2 || c.Map.Place("EU.example.net").Lat != 50 || c.Map.Place("eu.example.net").Name != "Frankfurt" || c.Map.Place("nope") != nil {
		t.Fatalf("places: %+v", c.Map.Places)
	}
	for _, bad := range []struct{ from, to, want string }{
		{"lat = 50", "lat = 91", "lat is from -90 to 90"},
		{"lon = 8.68", "lon = -181", "lon is from -180 to 180"},
		{`server = "eu.example.net"`, `server = ""`, "give the server's name"},
		{`server = "eu.example.net"`, `server = "irc.example.net"`, "there twice"},
		{`name = "Frankfurt"`, `name = "` + strings.Repeat("x", 61) + `"`, "up to 60"},
		{"lat = 50", "lat = 50\nzoom = 3", "unknown setting map.place.zoom"},
	} {
		if _, err := Load(write(t, strings.Replace(conf, bad.from, bad.to, 1))); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v", bad.to, err)
		}
	}
	// Saved by the panel: a block per server, found again by its server.
	path := write(t, conf)
	if _, err := Edit(path, func(d *Doc) error {
		d.SetBlock("map.place", MapPlace{Server: "eu.example.net", Name: "Berlin", Lat: 52.52, Lon: 13.405}.KV())
		d.SetBlock("map.place", MapPlace{Server: "us.example.net", Name: "New York", Lat: 40.7128, Lon: -74.006012}.KV())
		d.DelBlock("map.place", "server", "irc.example.net")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		raw, _ := os.ReadFile(path)
		t.Fatalf("%v\n%s", err, raw)
	}
	if len(c.Map.Places) != 2 || c.Map.Place("eu.example.net").Name != "Berlin" || c.Map.Place("us.example.net").Lon != -74.006 || c.Map.Place("irc.example.net") != nil {
		t.Errorf("after edit: %+v", c.Map.Places)
	}
	if kv := (MapPlace{Server: "a.b", Name: "X", Lat: 50, Lon: -3}).KV(); kv[2].Value != "50.0" || kv[3].Value != "-3.0" {
		t.Errorf("floats: %+v", kv)
	}
}
