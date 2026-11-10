package cloak

import (
	"regexp"
	"strings"
	"testing"
)

const secret = "0123456789abcdef-test"

func TestShapes(t *testing.T) {
	cases := []struct {
		host, ip string
		isIP     bool
		pattern  string
	}{
		{"192.0.2.10", "192.0.2.10", true, `^beenet-[0-9a-f]{8}\.[0-9a-f]{6}\.ip$`},
		{"2001:db8::1", "2001:db8::1", true, `^beenet-[0-9a-f]{8}\.[0-9a-f]{6}\.ip$`},
		{"dsl-7.isp.Example.com", "192.0.2.10", false, `^beenet-[0-9a-f]{8}\.example\.com$`},
		{"localhost", "127.0.0.1", false, `^beenet-[0-9a-f]{8}\.cloak$`},
	}
	for _, c := range cases {
		got := Host(secret, "BeeNet", c.host, c.ip, c.isIP)
		if !regexp.MustCompile(c.pattern).MatchString(got) {
			t.Errorf("Host(%q) = %q, want %s", c.host, got, c.pattern)
		}
		if strings.Contains(got, "192") || strings.Contains(got, "dsl") {
			t.Errorf("cloak %q leaks the real host", got)
		}
	}
}

func TestStableAndRanges(t *testing.T) {
	a := Host(secret, "BeeNet", "192.0.2.10", "192.0.2.10", true)
	if a != Host(secret, "BeeNet", "192.0.2.10", "192.0.2.10", true) {
		t.Error("same input gave a different cloak")
	}
	b := Host(secret, "BeeNet", "192.0.2.99", "192.0.2.99", true)
	other := Host(secret, "BeeNet", "192.0.3.10", "192.0.3.10", true)
	if a == b || a[strings.Index(a, "."):] != b[strings.Index(b, "."):] {
		t.Errorf("same /24 should share the range part: %s %s", a, b)
	}
	if a[strings.Index(a, "."):] == other[strings.Index(other, "."):] {
		t.Errorf("different /24 shares the range part: %s %s", a, other)
	}
	if a == Host("another-secret-123", "BeeNet", "192.0.2.10", "192.0.2.10", true) {
		t.Error("the secret doesn't change the cloak")
	}
	if c := Host(secret, "", "x.y.z", "", false); !strings.HasPrefix(c, "cloak-") {
		t.Errorf("empty network prefix: %s", c)
	}
}
