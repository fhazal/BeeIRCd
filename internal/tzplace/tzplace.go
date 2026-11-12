// Package tzplace says where a server is from its time zone: every IANA
// zone has a city with coordinates (zone.tab), so a machine set to
// Asia/Kuala_Lumpur is in Kuala Lumpur. Nothing is looked up online. A
// machine set to UTC says nothing about where it is.
package tzplace

import (
	"os"
	"path/filepath"
	"strings"
)

// Place is a zone's city.
type Place struct {
	Zone string
	Name string // the city, like "Kuala Lumpur"
	Lat  float64
	Lon  float64
}

// Lookup finds zone (old names too). ok is false for UTC and unknown zones.
func Lookup(zone string) (Place, bool) {
	zone = strings.TrimPrefix(strings.TrimSpace(zone), ":")
	// A zone of its own first: some (Asia/Kuala_Lumpur) are listed in
	// zone.tab and also as an old name of another zone.
	c, ok := zones[zone]
	for i := 0; !ok && i < 3; i++ {
		to, isLink := links[zone]
		if !isLink {
			break
		}
		zone = to
		c, ok = zones[zone]
	}
	if !ok {
		return Place{}, false
	}
	name := zone
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return Place{Zone: zone, Name: strings.ReplaceAll(name, "_", " "), Lat: c[0], Lon: c[1]}, true
}

// Local is this machine's zone name ("" when it can't tell): $TZ, then
// /etc/timezone, then where /etc/localtime points.
func Local() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" && !strings.HasPrefix(tz, "/") {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			return tz
		}
	}
	return fromLink(os.Readlink("/etc/localtime"))
}

func fromLink(target string, err error) string {
	if err != nil {
		return ""
	}
	target = filepath.ToSlash(target)
	if i := strings.Index(target, "zoneinfo/"); i >= 0 {
		return target[i+len("zoneinfo/"):]
	}
	return ""
}
