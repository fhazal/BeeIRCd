package services

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
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
// does: one IP however it is written, or a CIDR, with no ELINE cutting
// into it.
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
	add(store.BanZline, "203.0.113.*", time.Time{})          // a prefix glob: becomes a /24
	add(store.BanZline, "203.0.*.5", time.Time{})            // any other glob: the server refuses it
	add(store.BanZline, "%192.0.2.9", time.Time{})           // soft
	add(store.BanZline, "2001:DB8::1", time.Time{})          // the same text, case aside
	add(store.BanZline, "2001:db8:0:0::2", time.Time{})      // long form: still that one IP
	add(store.BanZline, "0::3", time.Time{})                 // ::3 as zlineMask keeps it
	add(store.BanZline, "::ffff:192.0.2.66", time.Time{})    // mapped: ZlineFor unmaps it
	add(store.BanZline, "2001:db8:1::5/48", time.Time{})     // host bits set: the /48
	add(store.BanZline, "::ffff:192.0.2.0/120", time.Time{}) // mapped range: ZlineFor matches nobody
	add(store.BanZline, "192.0.2.77", time.Now().Add(-1))    // ran out

	want := []string{"192.0.2.5/32", "192.0.2.66/32", "198.51.100.0/24", "2001:db8:1::/48", "2001:db8::1/128", "2001:db8::2/128", "203.0.113.0/24", "::3/128"}
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
	want = []string{"192.0.2.5/32", "192.0.2.66/32", "2001:db8:1::/48", "2001:db8::1/128", "2001:db8::2/128", "203.0.113.0/24", "::3/128"}
	if got := gateNets(s); !slices.Equal(got, want) {
		t.Fatalf("with ELINEs: %v, want %v", got, want)
	}
	if s.ZlineFor("198.51.100.7") != nil || s.ZlineFor("198.51.100.8") == nil {
		t.Fatal("the ELINE should cut into the ZLINE")
	}

	// A prefix ELINE is an allow prefix like a CIDR one: it cuts the /24.
	add(store.BanEline, "*@203.0.*", time.Time{})
	want = []string{"192.0.2.5/32", "192.0.2.66/32", "2001:db8:1::/48", "2001:db8::1/128", "2001:db8::2/128", "::3/128"}
	if got := gateNets(s); !slices.Equal(got, want) {
		t.Fatalf("with a prefix ELINE: %v, want %v", got, want)
	}

	// An ELINE whose host mask may match IPs but is no prefix: the gate gets nothing.
	add(store.BanEline, "*@10.?", time.Time{})
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
		{"192.0.2.*", false, "192.0.2.0/24"},
		{"192.0.2.*", true, "192.0.2.0/24"},
		{"10.*.5", false, ""},
		{"2001:db8::0001", false, "2001:db8::1/128"},
		{"2001:db8::0001", true, "2001:db8::1/128"}, // banMatches compares the address
		{"0::1", true, "::1/128"},
		{"::ffff:192.0.2.1", true, "192.0.2.1/32"},
		{"::ffff:192.0.2.0/120", true, ""}, // MatchHost never matches it
		{"192.0.2.9/24", true, "192.0.2.0/24"},
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

// A converted wildcard matches exactly the IPs irc.MatchHost matches; the
// globs that are not a plain prefix stay out.
func TestWildcardNet(t *testing.T) {
	for m, want := range map[string]string{
		"1.2.3.*": "1.2.3.0/24", "1.2.*": "1.2.0.0/16", "1.2.*.*": "1.2.0.0/16", "1.*.*.*": "1.0.0.0/8",
		"10.*": "10.0.0.0/8", "10.*.*.*": "10.0.0.0/8", "10.*.*": "10.0.0.0/8", "0.0.0.*": "0.0.0.0/24",
		"255.255.255.*": "255.255.255.0/24",
		// not a prefix, or not written the way the server writes an IP
		"1.2.*.5": "", "1.*.3.*": "", "1.2.3.1?": "", "1.2.3*": "", "1.2.3.4": "", "*": "", "*.*.*.*": "",
		"1.2.3.*.*": "", "1.2.3.4.*": "", "01.2.*": "", "256.1.*": "", "1.2.+3.*": "", "-1.*": "", "a.b.*": "",
		"::*": "", "2001:db8:*": "", "*.example.com": "", "host.*": "", "1.2.3.*/24": "", "*@1.2.3.*": "", "": "",
	} {
		p, ok := wildcardNet(m)
		got := ""
		if ok {
			got = p.String()
		}
		if got != want {
			t.Errorf("wildcardNet(%q) = %q, want %q", m, got, want)
		}
		if ok != (want != "") {
			t.Errorf("wildcardNet(%q) ok = %v", m, ok)
		}
	}
	// Same IPs as MatchHost, over a sample.
	var ips []string
	for _, a := range []string{"0", "1", "2", "10", "11", "255"} {
		for _, b := range []string{"0", "2", "3", "99"} {
			for _, c := range []string{"0", "3", "4"} {
				for _, d := range []string{"0", "1", "5", "254"} {
					ips = append(ips, a+"."+b+"."+c+"."+d)
				}
			}
		}
	}
	ips = append(ips, "2001:db8::1", "::1", "1.2.3.4")
	for _, m := range []string{"1.2.3.*", "1.2.*", "1.2.*.*", "10.*", "10.*.*.*", "10.*.*", "0.0.0.*", "255.*"} {
		p, ok := wildcardNet(m)
		if !ok {
			t.Fatalf("wildcardNet(%q) not converted", m)
		}
		for _, ip := range ips {
			if got, want := p.Contains(netip.MustParseAddr(ip)), irc.MatchHost(m, ip); got != want {
				t.Errorf("%s: prefix %s contains %s = %v, MatchHost = %v", m, p, ip, got, want)
			}
		}
	}
}

// Every Z-line in a form the gate can take (a CIDR, one IPv4, IPv6 in any
// spelling, an IPv6 CIDR, an IPv4 prefix glob) refuses at the gate exactly
// the addresses ZlineFor refuses, so a banned IP can't get past the fast
// path while the slow check would stop it.
func TestGateZlinesMatchZlineFor(t *testing.T) {
	masks := []string{
		"1.2.3.0/24", "1.2.3.77/24", "5.6.7.8", "5.6.7.8/32",
		"2001:db8::1", "2001:DB8::2", "2001:0db8:0000:0000:0000:0000:0000:0003", "2001:DB8:0:0:0:0:0:4",
		"::ffff:9.9.9.9", "::FFFF:9.9.9.10", "0:0:0:0:0:ffff:9.9.9.11",
		"2001:db8:aa::/48", "2001:DB8:BB::/48", "2001:0db8:00cc:0000::/64",
		"10.20.30.*", "11.*", "12.13.*.*",
	}
	ips := []string{
		"1.2.3.0", "1.2.3.4", "1.2.3.255", "1.2.4.1", "5.6.7.8", "5.6.7.9",
		"2001:db8::1", "2001:db8::2", "2001:db8::3", "2001:db8::4", "2001:db8::5",
		"9.9.9.9", "9.9.9.10", "9.9.9.11", "9.9.9.12",
		"2001:db8:aa::1", "2001:db8:aa:ffff::9", "2001:db8:ab::1", "2001:db8:bb::7",
		"2001:db8:cc::1", "2001:db8:cc:1::1",
		"10.20.30.1", "10.20.31.1", "11.0.0.1", "11.255.1.2", "12.13.200.1", "12.14.0.1", "::1", "8.8.8.8",
	}
	for _, m := range masks {
		s, _ := newServices(t)
		if err := s.store.AddNetBan(store.NetBan{Kind: store.BanZline, Mask: m, Reason: "test", SetBy: "test", SetAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		s.reloadOperData()
		gb := s.GateZlines()
		if len(gb) != 1 {
			t.Errorf("%s: gate list %v, want one entry", m, gb)
			continue
		}
		for _, ip := range ips {
			slow := s.ZlineFor(ip) != nil
			fast := gb[0].Net.Contains(netip.MustParseAddr(ip).Unmap())
			if slow != fast {
				t.Errorf("ZLINE %s, client %s: ZlineFor refuses %v, gate %s refuses %v", m, ip, slow, gb[0].Net, fast)
			}
		}
	}
}
