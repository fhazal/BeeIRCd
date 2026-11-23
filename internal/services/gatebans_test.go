package services

import (
	"slices"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

func gateNets(s *Services) []string {
	var out []string
	for _, b := range s.GateZlines() {
		out = append(out, b.Net.String())
	}
	slices.Sort(out)
	return out
}

// GateZlines gives the gate only ZLINEs it can apply exactly as ZlineFor
// does: one IP written the way the server writes it, or a CIDR, with no
// ELINE cutting into it.
func TestGateZlines(t *testing.T) {
	s, _ := newServices(t)
	add := func(kind, mask string, expires time.Time) {
		t.Helper()
		if err := s.store.AddNetBan(store.NetBan{Kind: kind, Mask: mask, Reason: "test", SetBy: "test", SetAt: time.Now(), Expires: expires}); err != nil {
			t.Fatal(err)
		}
		s.reloadOperData()
	}
	later := time.Now().Add(time.Hour)
	add(store.BanZline, "192.0.2.5", later)
	add(store.BanZline, "198.51.100.0/24", time.Time{})
	add(store.BanZline, "203.0.113.*", time.Time{})       // a glob: the server refuses it
	add(store.BanZline, "%192.0.2.9", time.Time{})        // soft
	add(store.BanZline, "2001:DB8::1", time.Time{})       // the same text, case aside
	add(store.BanZline, "2001:db8:0:0::2", time.Time{})   // not as the server writes it
	add(store.BanZline, "192.0.2.77", time.Now().Add(-1)) // ran out

	want := []string{"192.0.2.5/32", "198.51.100.0/24", "2001:db8::1/128"}
	if got := gateNets(s); !slices.Equal(got, want) {
		t.Fatalf("gate ZLINEs %v, want %v", got, want)
	}
	for _, b := range s.GateZlines() {
		if b.Net.String() == "192.0.2.5/32" && !b.Until.Equal(later) && b.Until.Sub(later).Abs() > time.Second {
			t.Fatalf("192.0.2.5 until %v, want %v", b.Until, later)
		}
		if b.Net.String() == "198.51.100.0/24" && !b.Until.IsZero() {
			t.Fatalf("permanent ZLINE until %v", b.Until)
		}
		// Every address in it is one ZlineFor refuses.
		a := b.Net.Addr()
		if s.ZlineFor(a.String()) == nil {
			t.Fatalf("%s from the gate list, but ZlineFor lets it in", a)
		}
	}

	// An ELINE on one IP in the /24 takes the /24 out; a hostname ELINE
	// changes nothing.
	add(store.BanEline, "*@198.51.100.7", time.Time{})
	add(store.BanEline, "*@*.example.com", time.Time{})
	want = []string{"192.0.2.5/32", "2001:db8::1/128"}
	if got := gateNets(s); !slices.Equal(got, want) {
		t.Fatalf("with ELINEs: %v, want %v", got, want)
	}
	if s.ZlineFor("198.51.100.7") != nil || s.ZlineFor("198.51.100.8") == nil {
		t.Fatal("the ELINE should cut into the ZLINE")
	}

	// An ELINE whose host mask may match IPs: the gate gets nothing.
	add(store.BanEline, "*@10.*", time.Time{})
	if got := gateNets(s); len(got) != 0 {
		t.Fatalf("with a wide ELINE: %v, want none", got)
	}
}

func TestGateNet(t *testing.T) {
	for _, c := range []struct {
		m     string
		exact bool
		want  string
	}{
		{"192.0.2.1", true, "192.0.2.1/32"},
		{"192.0.2.0/24", true, "192.0.2.0/24"},
		{"::ffff:192.0.2.1", false, ""}, // the server writes it as 192.0.2.1
		{"fe80::1%eth0", false, ""},
		{"192.0.2.*", false, ""},
		{"2001:db8::0001", false, "2001:db8::1/128"},
		{"2001:db8::0001", true, ""},
	} {
		p, ok := gateNet(c.m, c.exact)
		got := ""
		if ok {
			got = p.String()
		}
		if got != c.want {
			t.Errorf("gateNet(%q, %v) = %q, want %q", c.m, c.exact, got, c.want)
		}
	}
	for h, want := range map[string]bool{"10.*": true, "*": true, "": true, "2001:db8:*": true, "*.example.com": false, "host?": false} {
		if got := mayMatchIP(h); got != want {
			t.Errorf("mayMatchIP(%q) = %v", h, got)
		}
	}
}
