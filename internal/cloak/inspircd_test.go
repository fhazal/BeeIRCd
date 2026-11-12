package cloak

import "testing"

// Expected cloaks: riv.ptv.0.127.ip came from a real UnrealIRCd 6.2.7
// loading cloak_inspircd with testKeys (a client from 127.0.0.1 that
// resolved to localhost); the rest from the module's own functions,
// compiled as they are with stubs (that build gives the same
// riv.ptv.0.127.ip).
func TestInspHostMatchesModule(t *testing.T) {
	for _, c := range []struct{ host, ip, want string }{
		{"localhost", "127.0.0.1", "riv.ptv.0.127.ip"},
		{"127.0.0.1", "127.0.0.1", "riv.ptv.0.127.ip"},
		{"192.0.2.10", "192.0.2.10", "jt1.4lg.0.192.ip"},
		{"2001:db8::1", "2001:db8::1", "5ll8ta.snok.rsuv.0db8.2001.ip"},
		{"dsl-7.isp.example.com", "192.0.2.10", "k8tqbi.isp.example.com"},
		{"Host.Example.COM", "192.0.2.10", "3apbtf.Example.COM"},
		{"isp.com", "192.0.2.10", "kj90bi.com"},
		{"a.very-long-hostname-label-that-goes-on.and-on-and-on.example.com", "192.0.2.10", "t8m9u3.and-on-and-on.example.com"},
	} {
		if got := InspHost(testKeys, c.host, c.ip); got != c.want {
			t.Errorf("%s (%s): got %s, the module says %s", c.host, c.ip, got, c.want)
		}
	}
	if got := InspChecksum(testKeys); got != "INSPMD5:19d9c33b92fae634603683dfb8699b5e" {
		t.Errorf("checksum %s", got)
	}
}
