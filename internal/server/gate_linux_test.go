//go:build linux

package server

import (
	"bufio"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
	"github.com/fhazal/beeircd/internal/store"
)

// BEEIRCD_TEST_GATE=1 runs every test server behind a gate in this process
// (gate.Loopback), the way `beeircd run` does on Linux.
func init() {
	if os.Getenv("BEEIRCD_TEST_GATE") != "" {
		newTestServer = gatedServer
	}
}

// gatedServer makes a server whose client ports live in a Front.
func gatedServer(cfg *config.Config) (*Server, func(), error) {
	quiet := log.New(io.Discard, "", 0)
	child, stop, err := gate.Loopback(func(d gate.Deliver) gate.Front { return NewFront(quiet, d) })
	if err != nil {
		return nil, nil, err
	}
	s, err := NewBehindGate(cfg, quiet, child)
	if err != nil {
		child.Close()
		stop()
		return nil, nil, err
	}
	return s, stop, nil
}

// Behind the gate a client sees what it sees in one process: a plain
// client, a TLS client with its certificate, a WebSocket client; ports
// added and removed by AdminServ open and close in the gate.
func TestGateClients(t *testing.T) {
	old := newTestServer
	newTestServer = gatedServer
	s := testServer(t, func(c *config.Config) {
		c.Listen = append(c.Listen, config.Listen{Address: "127.0.0.1:0", WebSocket: true})
	})
	newTestServer = old
	if s.gate == nil {
		t.Fatal("the server isn't behind the gate")
	}

	plain := login(t, s, "plainbee")
	cert, fp := clientCert(t)
	cb := certLogin(t, s, "certbee", &cert)
	cb.send("WHOIS certbee") // the fingerprint shows to the user and opers only
	cb.expect(" 671 certbee certbee :is using a secure connection")
	cb.expect(" 276 certbee certbee :has client certificate fingerprint " + fp)
	plain.send("WHOIS plainbee")
	plain.expect(" 318 plainbee plainbee ")

	w := wsDial(t, s, false, nil)
	if w.resp.StatusCode != 101 {
		t.Fatalf("upgrade: %d", w.resp.StatusCode)
	}
	w.register("webbee")
	w.send("PRIVMSG plainbee :hi from the web")
	plain.expect(":webbee!~webbee@127.0.0.1 PRIVMSG plainbee :hi from the web")

	// A client that quits gets the server's goodbye through the gate.
	plain.send("QUIT :bye")
	plain.expect("ERROR :Closing Link: 127.0.0.1 (Quit: bye)")
}

// AdminServ PORT ADD and DEL open and close the port in the gate.
func TestGatePorts(t *testing.T) {
	old := newTestServer
	newTestServer = gatedServer
	s, _, _ := fileServer(t, "")
	newTestServer = old
	if s.gate == nil {
		t.Fatal("the server isn't behind the gate")
	}
	b := boss(t, s)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close()

	b.send("AS PORT ADD " + addr)
	b.expect(asLine("Boss", addr+" is open (plain) and saved."))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c := &tc{t: t, conn: conn, r: bufio.NewReader(conn), nick: "porty"}
	t.Cleanup(func() { conn.Close() })
	c.send("NICK porty")
	c.send("USER porty 0 * :porty")
	c.expect(" 001 porty ")

	b.send("AS PORT DEL " + addr)
	b.expect(asLine("Boss", addr+" is closed and saved. People connected through it stay."))
	b.sync()
	if d, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		d.Close()
		t.Error("the port is still open in the gate")
	}
	c.send("PING :still here")
	c.expect("PONG")
}

// gateHas waits until the server has sent the gate a deny list with n
// entries (gateSync runs on the loop, so the gate has it then too).
func gateHas(t *testing.T, s *Server, n int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		got := -1
		s.onLoop(func() { got = len(s.gateDeny.sent) })
		if got == n {
			return
		}
	}
	t.Fatalf("the gate never got a deny list of %d", n)
}

// A ZLINEd IP is closed on a TLS port before any TLS handshake, with the
// same notice as without the gate; once the ZLINE is gone it gets in.
func TestGateZline(t *testing.T) {
	old := newTestServer
	newTestServer = gatedServer
	s, _, _ := fileServer(t, "")
	newTestServer = old
	if s.gate == nil {
		t.Fatal("the server isn't behind the gate")
	}
	b := boss(t, s)
	addBan(s, store.NetBan{Kind: store.BanZline, Mask: "127.0.0.1", Reason: "gate test"})
	gateHas(t, s, 1)

	conn, err := net.Dial("tcp", s.Addrs()[1].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	expectClosed(t, conn) // no handshake: it never sent a ClientHello
	b.expect("*** XLINE: Z-line 127.0.0.1 stopped a connection from 127.0.0.1")
	c := raw(t, s) // the plain port still gets the reason
	c.expect("ERROR :Closing Link: 127.0.0.1 (Z-lined: gate test)")
	b.expect("*** XLINE: Z-line 127.0.0.1 stopped a connection from 127.0.0.1")

	b.send("OS ZLINE DEL 127.0.0.1")
	b.expect("ZLINE on 127.0.0.1 removed.")
	gateHas(t, s, 0)
	certLogin(t, s, "welcomeback", nil)
}

// A throttled IP is refused in the gate once the server refused it, and
// those refusals count toward the throttle ZLINE as they would here.
func TestGateThrottle(t *testing.T) {
	old := newTestServer
	newTestServer = gatedServer
	s := testServerWith(t, func(c *config.Config) { c.Limits.ConnectRate = "1:60" }, noExempt)
	newTestServer = old
	certLogin(t, s, "first", nil)
	refused := rawTLS(t, s, nil) // the server refuses it after TLS: strike 1
	refused.expect("Throttled: reconnecting too fast")
	gateHas(t, s, 1)
	for i := 0; i < 2; i++ { // strikes 2 and 3, in the gate
		conn, err := net.Dial("tcp", s.Addrs()[1].String())
		if err != nil {
			t.Fatal(err)
		}
		expectClosed(t, conn)
		conn.Close()
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var throttled, zlined int
		s.onLoop(func() { throttled, zlined = int(s.safety().Throttled), int(s.safety().Zlined) })
		if throttled == 3 && zlined == 1 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("throttled %d, Z-lined %d; want 3 and 1", throttled, zlined)
		}
	}
	var zl bool
	s.onLoop(func() { zl = s.svc.ZlineFor("127.0.0.1") != nil })
	if !zl {
		t.Fatal("no throttle ZLINE")
	}
}

// A server that fails to start closes its gate link but leaves the client
// ports open in the gate, for the next server the gate starts.
func TestGateKeepsPortsWhenStartFails(t *testing.T) {
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close()
	busy, err := net.Listen("tcp", "127.0.0.1:0") // the RPC port is taken
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	off := false
	dir := t.TempDir()
	cfg := &config.Config{
		Server:   config.Server{Name: "irc.test.local", Network: "BeeNet", SID: "0BE", ResolveHosts: &off},
		Listen:   []config.Listen{{Address: addr}},
		TLS:      config.TLS{Cert: filepath.Join(dir, "cert.pem"), Key: filepath.Join(dir, "key.pem"), SelfSigned: true}, // the RPC API is HTTPS
		Cloak:    config.Cloak{Enabled: &off, Secret: "0123456789abcdef-test"},
		Services: config.Services{Database: filepath.Join(dir, "services.db")},
		Update:   config.Update{Check: &off},
		GeoIP:    config.GeoIP{Enabled: &off},
	}
	cfg.SetDefaults()
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	cfg.RPC.Listen = busy.Addr().String()

	quiet := log.New(io.Discard, "", 0)
	lp, err := gate.NewLoop(func(d gate.Deliver) gate.Front { return NewFront(quiet, d) })
	if err != nil {
		t.Fatal(err)
	}
	defer lp.Close()
	_, err = NewBehindGate(cfg, quiet, lp.Child())
	if err == nil || !strings.Contains(err.Error(), "rpc") {
		t.Fatalf("the server started with its RPC port taken, or failed before it: %v", err)
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("the gate closed the client port when the server failed to start: %v", err)
	}
	conn.Close()
}
