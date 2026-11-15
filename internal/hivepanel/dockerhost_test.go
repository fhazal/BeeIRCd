package hivepanel

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayOf(t *testing.T) {
	route := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0100590A\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"eth0\t0000590A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	if g := gatewayOf(route); g != "10.89.0.1" {
		t.Errorf("gateway %q, want 10.89.0.1", g)
	}
	if g := gatewayOf("Iface\tDestination\tGateway\n"); g != "" {
		t.Errorf("no default route gave %q", g)
	}
}

// In Docker, the host's 127.0.0.1 arrives from the network's gateway; the
// panel must see 127.0.0.1 (oper hosts *@127.0.0.1, trusted_proxy), and
// other containers keep their own address.
func TestDockerHostCountsAsLoopback(t *testing.T) {
	p := New(Options{HostAddr: "10.89.0.1", TrustedProxy: []string{"127.0.0.1"}})
	var got string
	var proxied bool
	p.mux = http.NewServeMux()
	p.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		got, proxied = p.clientIP(r), p.fromProxy(r)
	})
	for _, c := range []struct {
		from, want string
		proxy      bool
	}{
		{"10.89.0.1:51000", "127.0.0.1", true},
		{"10.89.0.20:51000", "10.89.0.20", false},
	} {
		got, proxied = "not served", false
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "127.0.0.1:8787"
		r.RemoteAddr = c.from
		p.ServeHTTP(httptest.NewRecorder(), r)
		if got != c.want || proxied != c.proxy {
			t.Errorf("from %s: client %s proxy %v, want %s %v", c.from, got, proxied, c.want, c.proxy)
		}
	}
}
