//go:build linux

package server

import (
	"bufio"
	"io"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
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
