package tzplace

import "testing"

func TestLookup(t *testing.T) {
	for _, c := range []struct {
		zone, name string
		lat, lon   float64
	}{
		{"Asia/Kuala_Lumpur", "Kuala Lumpur", 3.1667, 101.7}, // a zone and an old name of Singapore: the zone wins
		{"Europe/Berlin", "Berlin", 52.5, 13.3667},
		{":Asia/Calcutta", "Kolkata", 22.5333, 88.3667},     // old name
		{"America/New_York", "New York", 40.7142, -74.0064}, // seconds in the coordinates
		{"Australia/Sydney", "Sydney", -33.8667, 151.2167},
	} {
		p, ok := Lookup(c.zone)
		if !ok || p.Name != c.name || p.Lat != c.lat || p.Lon != c.lon {
			t.Errorf("%s: %+v %v", c.zone, p, ok)
		}
	}
	for _, z := range []string{"Etc/UTC", "UTC", "", "Local", "Nowhere/Land"} {
		if p, ok := Lookup(z); ok {
			t.Errorf("%q placed: %+v", z, p)
		}
	}
	if len(zones) < 300 || TZVersion == "" {
		t.Errorf("%d zones, version %q", len(zones), TZVersion)
	}
}

func TestFromLink(t *testing.T) {
	if z := fromLink("/usr/share/zoneinfo/Asia/Kuala_Lumpur", nil); z != "Asia/Kuala_Lumpur" {
		t.Errorf("%q", z)
	}
	if z := fromLink("../usr/share/zoneinfo/Etc/UTC", nil); z != "Etc/UTC" {
		t.Errorf("%q", z)
	}
	if z := fromLink("/etc/foo", nil); z != "" {
		t.Errorf("%q", z)
	}
}
