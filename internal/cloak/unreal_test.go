package cloak

import (
	"strings"
	"testing"
)

// Throwaway test keys; the expected cloaks came from a real UnrealIRCd
// 6.2.7 (cloak_sha256) given the same keys, a client from 127.0.0.1 that
// resolved to localhost, with cloak-method host and then ip.
var testKeys = [3]string{
	"TestKeyOneAbcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789TestKeyOne01",
	"TestKeyTwoZyxwvutsrqponmlkjihgfedcbaZYXWVUTSRQPONMLKJIHGFEDCBA9876543210TestKeyTwo02",
	"TestKeyThreeQwertyuiopAsdfghjklZxcvbnmQWERTYUIOPASDFGHJKLZXCVBNM1234567890Three03",
}

func TestUnrealHostMatchesUnrealIRCd(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"localhost", "Clk-C0765214"},
		{"127.0.0.1", "8BDDFFC3.F2AE781B.358FAE06.IP"},
	} {
		if got := UnrealHost(testKeys, "Clk", c.host); got != c.want {
			t.Errorf("%s: got %s, UnrealIRCd says %s", c.host, got, c.want)
		}
	}
}

func TestUnrealHostShapes(t *testing.T) {
	h := UnrealHost(testKeys, "Bee", "dsl-7.isp.example.com")
	if !strings.HasPrefix(h, "Bee-") || !strings.HasSuffix(h, ".isp.example.com") {
		t.Errorf("hostname cloak %q", h)
	}
	// IPv6 is cloaked in its full form, so both spellings agree.
	a, b := UnrealHost(testKeys, "Clk", "2001:db8::1"), UnrealHost(testKeys, "Clk", "2001:db8:0:0:0:0:0:1")
	if a != b || !strings.HasSuffix(a, ":IP") || strings.Count(a, ":") != 3 {
		t.Errorf("ipv6 %q %q", a, b)
	}
	// Long hosts keep their tail within 63 characters.
	long := "a." + strings.Repeat("b", 70) + ".example"
	if got := UnrealHost(testKeys, "Clk", long); len(got) > 63 {
		t.Errorf("too long: %d", len(got))
	}
	if c := UnrealChecksum(testKeys); !strings.HasPrefix(c, "SHA256:") || len(c) != 63 {
		t.Errorf("checksum %q", c)
	}
}
