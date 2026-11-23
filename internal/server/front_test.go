package server

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
)

// frontRig is a Front with its clients going to two channels: the ones it
// handed on, and the ones it refused.
type frontRig struct {
	f       *Front
	files   *gate.TLSFiles
	got     chan gate.Info
	refused chan gate.Info
}

func newFrontRig(t *testing.T) *frontRig {
	t.Helper()
	r := &frontRig{got: make(chan gate.Info, 8), refused: make(chan gate.Info, 8)}
	dir := t.TempDir()
	r.files = &gate.TLSFiles{Cert: filepath.Join(dir, "cert.pem"), Key: filepath.Join(dir, "key.pem"), SelfSigned: true, Host: "gate.test"}
	r.f = NewFront(log.New(io.Discard, "", 0), func(conn net.Conn, info gate.Info) bool {
		if conn == nil {
			r.refused <- info
			return true
		}
		conn.Close()
		r.got <- info
		return true
	})
	t.Cleanup(r.f.Close)
	return r
}

// apply opens the ports with deny; it returns their addresses.
func (r *frontRig) apply(t *testing.T, ports []config.Listen, deny ...gate.Deny) []string {
	t.Helper()
	res := r.f.Apply(gate.Spec{Ports: ports, TLS: r.files, Deny: deny})
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	for _, e := range res.Errors {
		if e != "" {
			t.Fatal(e)
		}
	}
	return res.Addrs
}

// expectClosed: the gate closes conn without a word, at once (a TLS
// handshake would wait for the client's hello).
func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(make([]byte, 64))
	if n != 0 || err == nil {
		t.Fatalf("read %d bytes, %v; want the connection closed with nothing sent", n, err)
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the gate left the connection open")
	}
}

func (r *frontRig) expectRefused(t *testing.T, ip string, isTLS bool) {
	t.Helper()
	select {
	case info := <-r.refused:
		host, _, _ := net.SplitHostPort(info.Remote)
		if host != ip || info.TLS != isTLS {
			t.Fatalf("refused %+v, want %s (TLS %v)", info, ip, isTLS)
		}
	case info := <-r.got:
		t.Fatalf("handed on %+v, want it refused", info)
	case <-time.After(5 * time.Second):
		t.Fatal("the server wasn't told about the refused client")
	}
}

func (r *frontRig) expectTLS(t *testing.T, addr string) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS: %v", err)
	}
	conn.Close()
	select {
	case info := <-r.got:
		if !info.TLS {
			t.Fatalf("handed on %+v, want TLS", info)
		}
	case info := <-r.refused:
		t.Fatalf("refused %+v", info)
	case <-time.After(5 * time.Second):
		t.Fatal("the client wasn't handed on")
	}
}

// A ZLINEd IP on a TLS port is closed before any TLS handshake, and the
// server is told; without the entry (removed, or run out) it gets in.
func TestFrontDenyTLS(t *testing.T) {
	r := newFrontRig(t)
	ports := []config.Listen{{Address: "127.0.0.1:0", TLS: true}, {Address: "127.0.0.1:0"}}
	addrs := r.apply(t, ports, gate.Deny{Net: "127.0.0.1/32"})

	conn, err := net.Dial("tcp", addrs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	expectClosed(t, conn)
	r.expectRefused(t, "127.0.0.1", true)

	// A TLS client gets no handshake either.
	if c, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addrs[0], &tls.Config{InsecureSkipVerify: true}); err == nil {
		c.Close()
		t.Fatal("TLS handshake done for a denied IP")
	}
	r.expectRefused(t, "127.0.0.1", true)

	// The plain port is left to the server (it can send the reason there).
	p, err := net.Dial("tcp", addrs[1])
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	select {
	case info := <-r.got:
		if info.TLS {
			t.Fatalf("plain client handed on as %+v", info)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the plain client wasn't handed on")
	}

	// Another network, a wider one, then none: the same ports stay open.
	r.apply(t, ports, gate.Deny{Net: "192.0.2.0/24"})
	r.expectTLS(t, addrs[0])
	r.apply(t, ports, gate.Deny{Net: "127.0.0.0/8"})
	conn2, err := net.Dial("tcp", addrs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	expectClosed(t, conn2)
	r.expectRefused(t, "127.0.0.1", true)
	r.apply(t, ports)
	r.expectTLS(t, addrs[0])

	// An entry that ran out no longer refuses; one still running does.
	r.apply(t, ports, gate.Deny{Net: "127.0.0.1/32", Until: time.Now().Add(-time.Second).UnixMilli()})
	r.expectTLS(t, addrs[0])
	r.apply(t, ports, gate.Deny{Net: "127.0.0.1/32", Until: time.Now().Add(time.Hour).UnixMilli()})
	conn3, err := net.Dial("tcp", addrs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer conn3.Close()
	expectClosed(t, conn3)
	r.expectRefused(t, "127.0.0.1", true)
}

// Behind a PROXY port the gate checks the address in the PROXY header,
// before TLS.
func TestFrontDenyProxy(t *testing.T) {
	r := newFrontRig(t)
	ports := []config.Listen{{Address: "127.0.0.1:0", TLS: true, Proxy: true, ProxyFrom: []string{"127.0.0.1"}}}
	addrs := r.apply(t, ports, gate.Deny{Net: "192.0.2.9/32"})

	dial := func(ip string) net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", addrs[0])
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "PROXY TCP4 %s 127.0.0.1 40000 6697\r\n", ip)
		return conn
	}
	conn := dial("192.0.2.9")
	defer conn.Close()
	expectClosed(t, conn)
	r.expectRefused(t, "192.0.2.9", true)

	// The proxy's own address isn't what counts.
	ok := tls.Client(dial("192.0.2.10"), &tls.Config{InsecureSkipVerify: true})
	ok.SetDeadline(time.Now().Add(5 * time.Second))
	if err := ok.Handshake(); err != nil {
		t.Fatalf("TLS for an allowed IP: %v", err)
	}
	ok.Close()
	select {
	case info := <-r.got:
		if host, _, _ := net.SplitHostPort(info.Remote); host != "192.0.2.10" {
			t.Fatalf("handed on %+v", info)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the allowed client wasn't handed on")
	}
}

func TestDenyList(t *testing.T) {
	now := time.Now()
	d := newDenyList([]gate.Deny{
		{Net: "192.0.2.1/32"},
		{Net: "2001:db8::/32", Until: now.Add(time.Minute).UnixMilli()},
		{Net: "198.51.100.0/24", Until: now.Add(-time.Minute).UnixMilli()},
		{Net: "not an address"},
	})
	for ip, want := range map[string]bool{
		"192.0.2.1": true, "192.0.2.2": false, "2001:db8::5": true, "2001:db9::5": false,
		"198.51.100.1": false, "::ffff:192.0.2.1": true, // IPv4 on a dual-stack port
	} {
		a, ok := addrOf(&net.TCPAddr{IP: net.ParseIP(ip), Port: 1})
		if !ok {
			t.Fatalf("addrOf(%s)", ip)
		}
		if got := d.has(a, now); got != want {
			t.Errorf("has(%s) = %v, want %v", ip, got, want)
		}
	}
	if newDenyList(nil) != nil {
		t.Error("an empty list should be nil")
	}
}

// On a WebSocket port behind a proxy the visitor's address comes with the
// HTTP request; the gate checks it before it answers the upgrade, and
// tells the server the visitor's address, not the proxy's.
func TestFrontDenyWebSocketProxy(t *testing.T) {
	r := newFrontRig(t)
	ports := []config.Listen{{Address: "127.0.0.1:0", WebSocket: true, Proxy: true, ProxyFrom: []string{"127.0.0.1"}}}
	addrs := r.apply(t, ports, gate.Deny{Net: "192.0.2.0/24"}, gate.Deny{Net: "2001:db8::/32"})

	upgrade := func(hdr string) (*bufio.Reader, net.Conn) {
		t.Helper()
		conn, err := net.Dial("tcp", addrs[0])
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n%s\r\n", hdr)
		br := bufio.NewReader(conn)
		return br, conn
	}
	status := func(br *bufio.Reader) int {
		t.Helper()
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}

	for _, c := range []struct{ hdr, ip string }{
		{"X-Forwarded-For: 192.0.2.9\r\n", "192.0.2.9"},
		{"X-Forwarded-For: 10.1.1.1, 192.0.2.10\r\n", "192.0.2.10"}, // the entry our proxy added
		{"X-Real-IP: 192.0.2.11\r\n", "192.0.2.11"},
		{"X-Forwarded-For: 2001:db8::7\r\n", "2001:db8::7"},
		{"X-Forwarded-For: ::ffff:192.0.2.12\r\n", "192.0.2.12"},
	} {
		br, conn := upgrade(c.hdr)
		if got := status(br); got != http.StatusForbidden {
			t.Fatalf("%q: status %d, want 403", c.hdr, got)
		}
		conn.Close()
		select {
		case info := <-r.refused:
			host, _, _ := net.SplitHostPort(info.Remote)
			if netip.MustParseAddr(host).Unmap().String() != c.ip || !info.WebSocket {
				t.Fatalf("%q: refused %+v, want %s", c.hdr, info, c.ip)
			}
		case info := <-r.got:
			t.Fatalf("%q: handed on %+v", c.hdr, info)
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: the server wasn't told", c.hdr)
		}
	}

	// A visitor that isn't denied (or gives no address) is answered as before.
	for _, hdr := range []string{"X-Forwarded-For: 198.51.100.4\r\n", ""} {
		br, conn := upgrade(hdr)
		if got := status(br); got != http.StatusSwitchingProtocols {
			t.Fatalf("%q: status %d, want 101", hdr, got)
		}
		conn.Close()
		select {
		case info := <-r.got:
			if hdr != "" {
				if host, _, _ := net.SplitHostPort(info.Remote); host != "198.51.100.4" {
					t.Fatalf("handed on %+v", info)
				}
			}
		case info := <-r.refused:
			t.Fatalf("%q: refused %+v", hdr, info)
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: not handed on", hdr)
		}
	}
}
