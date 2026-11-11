package irc

import "testing"

func TestMatchHost(t *testing.T) {
	for _, c := range []struct {
		mask, host string
		want       bool
	}{
		{"10.0.0.0/8", "10.20.30.40", true},
		{"10.0.0.0/8", "11.0.0.1", false},
		{"203.0.113.0/24", "::ffff:203.0.113.9", true}, // IPv4-mapped
		{"2001:db8::/32", "2001:db8:1::5", true},
		{"2001:db8::/32", "2001:db9::5", false},
		{"10.0.0.0/8", "host.example.org", false}, // CIDR only matches IPs
		{"bad/cidr", "10.0.0.1", false},
		{"*.example.org", "a.example.org", true},
		{"203.0.113.*", "203.0.113.5", true},
	} {
		if got := MatchHost(c.mask, c.host); got != c.want {
			t.Errorf("MatchHost(%q, %q) = %v", c.mask, c.host, got)
		}
	}
	if !MatchUserHost("~bot@10.0.0.0/8", "~bot", "box.example", "10.1.2.3") || MatchUserHost("~bot@10.0.0.0/8", "~bee", "", "10.1.2.3") {
		t.Error("MatchUserHost")
	}
	if MatchUserHost("nomask", "x", "h") {
		t.Error("a mask without @ matched")
	}
}

func TestNormalizeBan(t *testing.T) {
	for in, want := range map[string]string{
		"bee":                "bee!*@*",
		"~a:Bee":             "~a:Bee",
		"~A:bee":             "~a:bee",
		"~a:0":               "~a:0",
		"~r:*bot*":           "~r:*bot*",
		"~m:spam":            "~m:spam!*@*",
		"~q:*!*@bad.example": "~q:*!*@bad.example",
		"~m:~a:bee":          "~m:~a:bee",
		"~t:30:*.example":    "~t:30:*!*@*.example",
		"~t:5:~m:x":          "~t:5:~m:x!*@*",
	} {
		if got, ok := NormalizeBan(in); !ok || got != want {
			t.Errorf("NormalizeBan(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"~", "~a", "~a:", "~z:x", "~t:0:x", "~t:x:y", "~t:999999:x", "~t:5:~t:5:x", "~m:~m:x", "~ab:c"} {
		if got, ok := NormalizeBan(bad); ok {
			t.Errorf("NormalizeBan(%q) = %q, want refused", bad, got)
		}
	}
	if n, ok := TimedBan("~t:30:x!*@*"); !ok || n != 30 {
		t.Errorf("TimedBan = %d, %v", n, ok)
	}
	if _, ok := TimedBan("x!*@*"); ok {
		t.Error("TimedBan on a plain mask")
	}
}
