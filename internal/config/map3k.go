package config

// Step 3k: where the servers of the network are, for the Hive Panel's
// network map. Set by the owner (in the panel, or by hand); never looked
// up from an address. One [[map.place]] per server, kept on the server the
// panel talks to, so it covers servers linked from elsewhere and other
// IRC daemons too.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MapConf is the [map] section.
type MapConf struct {
	Places []MapPlace `toml:"place"`
}

// MapPlace is one [[map.place]] block.
type MapPlace struct {
	// Server is the server's name, like "eu.example.net".
	Server string `toml:"server"`
	// Name is what the map calls the place, like "Frankfurt".
	Name string  `toml:"name"`
	Lat  float64 `toml:"lat"`
	Lon  float64 `toml:"lon"`
}

// MaxMapPlaces is the most [[map.place]] blocks a config may hold.
const MaxMapPlaces = 1000

// Check reports what is wrong with p, in words for the owner.
func (p MapPlace) Check() error {
	switch {
	case strings.TrimSpace(p.Server) == "" || len(p.Server) > 100 || strings.ContainsAny(p.Server, " \t\"\\"):
		return fmt.Errorf("[[map.place]] server %q: give the server's name, like eu.example.net", p.Server)
	case !utf8.ValidString(p.Name) || utf8.RuneCountInString(p.Name) > 60 || strings.ContainsAny(p.Name, "\r\n\t\"\\"):
		return fmt.Errorf("[[map.place]] %s: the place name is up to 60 letters, without quotes", p.Server)
	case math.IsNaN(p.Lat) || p.Lat < -90 || p.Lat > 90:
		return fmt.Errorf("[[map.place]] %s: lat is from -90 to 90", p.Server)
	case math.IsNaN(p.Lon) || p.Lon < -180 || p.Lon > 180:
		return fmt.Errorf("[[map.place]] %s: lon is from -180 to 180", p.Server)
	}
	return nil
}

func (m *MapConf) check() error {
	if len(m.Places) > MaxMapPlaces {
		return fmt.Errorf("[[map.place]]: %d blocks, %d at most", len(m.Places), MaxMapPlaces)
	}
	seen := map[string]bool{}
	for _, p := range m.Places {
		if err := p.Check(); err != nil {
			return err
		}
		k := strings.ToLower(p.Server)
		if seen[k] {
			return fmt.Errorf("[[map.place]] %s is there twice", p.Server)
		}
		seen[k] = true
	}
	return nil
}

// Place is the place of server, or nil.
func (m *MapConf) Place(server string) *MapPlace {
	for i := range m.Places {
		if strings.EqualFold(m.Places[i].Server, server) {
			return &m.Places[i]
		}
	}
	return nil
}

// KV is p as the lines of its [[map.place]] block; server first (the key
// Doc.SetBlock finds it by). Coordinates keep 4 decimals (about 10 m).
func (p MapPlace) KV() []KV {
	f := func(v float64) string {
		t := strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64)
		if !strings.Contains(t, ".") {
			t += ".0" // a TOML float, so hand edits keep the type
		}
		return t
	}
	return []KV{{Key: "server", Value: Quote(p.Server)}, {Key: "name", Value: Quote(p.Name)},
		{Key: "lat", Value: f(p.Lat)}, {Key: "lon", Value: f(p.Lon)}}
}
