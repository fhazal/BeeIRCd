//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
)

// A hand-over the way it runs in production, short of the second process:
// the server sits behind the gate, its clients' sockets cross to the next
// server as file descriptors, and TLS and WebSocket clients keep the
// session the gate holds for them.

// gateRig is a gate (in this process) with a server behind it.
type gateRig struct {
	t      *testing.T
	path   string
	lp     *gate.Loop
	s      *Server
	result chan error
}

func newGateRig(t *testing.T) *gateRig { return newGateRigWith(t, "") }

func newGateRigWith(t *testing.T, extra string) *gateRig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "beeircd.conf")
	conf := fileConf + extra + "\n[[listen]]\naddress = \"127.0.0.1:0\"\nwebsocket = true\n"
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	quiet := log.New(io.Discard, "", 0)
	lp, err := gate.NewLoop(func(d gate.Deliver) gate.Front { return NewFront(quiet, d) })
	if err != nil {
		t.Fatal(err)
	}
	g := &gateRig{t: t, path: path, lp: lp, result: make(chan error, 1)}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if g.s, err = NewBehindGate(cfg, quiet, lp.Child()); err != nil {
		lp.Close()
		t.Fatal(err)
	}
	go func() { g.result <- g.s.Serve(context.Background()) }()
	return g
}

// handOver switches to a new server behind the same gate and returns it.
// tamper may change what the next server is given.
func (g *gateRig) handOver(tamper func(*gate.Handover)) (*Server, error) {
	g.t.Helper()
	child, err := g.lp.Handover(10 * time.Second)
	if err != nil {
		g.t.Fatalf("the gate got no hand-over: %v", err)
	}
	select {
	case err := <-g.result:
		if !errors.Is(err, ErrHandover) {
			g.t.Fatalf("old server's Serve returned %v, want ErrHandover", err)
		}
	case <-time.After(10 * time.Second):
		g.t.Fatal("the old server didn't stop after handing over")
	}
	cfg, err := config.Load(g.path)
	if err != nil {
		g.t.Fatal(err)
	}
	var r *resume
	if h := child.Handover(); h != nil {
		if tamper != nil {
			tamper(h)
		}
		r = resumeFromGate(child, h)
	}
	n, err := newServer(cfg, log.New(io.Discard, "", 0), child, r)
	if err != nil {
		return nil, err
	}
	runNext(g.t, n, g.lp.Close)
	return n, nil
}

func TestGateHandoverKeepsEveryone(t *testing.T) {
	g := newGateRig(t)
	s := g.s
	plain := login(t, s, "Plain")
	cert, fp := clientCert(t)
	secure := certLogin(t, s, "Secure", &cert)
	web := wsDial(t, s, false, nil)
	if web.resp.StatusCode != 101 {
		t.Fatalf("upgrade: %d", web.resp.StatusCode)
	}
	web.register("Web")
	plain.send("JOIN #hive")
	plain.expect(" 366 Plain #hive ")
	plain.send("TOPIC #hive :through the gate")
	plain.expect("TOPIC #hive :through the gate")
	secure.send("JOIN #hive")
	secure.expect(" 366 Secure #hive ")
	web.send("JOIN #hive")
	web.expect(" 366 Web #hive ")
	secure.send("AWAY :tea")
	secure.expect(" 306 Secure ")
	plain.sync()
	secure.sync()
	// Half a line each, over a plain socket and through TLS.
	fmt.Fprintf(plain.conn, "PRIVMSG #hive :plain ha")
	fmt.Fprintf(secure.conn, "PRIVMSG #hive :secure ha")
	ports := fmt.Sprint(s.Addrs())

	// Someone connects in the middle of it: the gate holds them until the
	// next server is there.
	late := make(chan *tc, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		c := raw(t, s)
		c.nick = "Late"
		c.send("NICK Late")
		c.send("USER late 0 * :Late")
		late <- c
	}()
	n, err := g.handOver(nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.gate == nil {
		t.Fatal("the next server isn't behind the gate")
	}
	if got := fmt.Sprint(n.Addrs()); got != ports {
		t.Fatalf("the ports changed: %s, were %s", got, ports)
	}

	fmt.Fprintf(plain.conn, "lf\r\n")
	secure.expect(":Plain!~plain@127.0.0.1 PRIVMSG #hive :plain half")
	web.expect(":Plain!~plain@127.0.0.1 PRIVMSG #hive :plain half")
	fmt.Fprintf(secure.conn, "lf\r\n")
	plain.expect(":Secure!~secure@127.0.0.1 PRIVMSG #hive :secure half")
	web.send("PRIVMSG #hive :still on the web")
	plain.expect(":Web!~Web@127.0.0.1 PRIVMSG #hive :still on the web")
	secure.expect(":Web!~Web@127.0.0.1 PRIVMSG #hive :still on the web")

	// What the gate learned about each client came along.
	secure.send("WHOIS Secure")
	secure.expect(" 671 Secure Secure :is using a secure connection")
	secure.expect(" 276 Secure Secure :has client certificate fingerprint " + fp)
	secure.expect(" 318 Secure Secure ")
	plain.send("WHOIS Secure")
	plain.expect(" 301 Plain Secure :tea")
	plain.expect(" 318 Plain Secure ")
	plain.send("TOPIC #hive")
	plain.expect(" 332 Plain #hive :through the gate")
	onLoop(t, n, func() {
		if n.local < 3 {
			t.Errorf("next server has %d local clients, want the 3 from before", n.local)
		}
		for nick, want := range map[string][3]bool{"plain": {false, false, false}, "secure": {true, false, true}, "web": {false, true, true}} {
			u := n.net.UserByNick(nick)
			if u == nil {
				t.Errorf("%s is gone", nick)
				continue
			}
			c := n.clients[u.UID]
			_, piped := c.conn.(*gate.Conn)
			if c.tls != want[0] || c.websocket != want[1] || piped != want[2] {
				t.Errorf("%s: tls %v, websocket %v, behind a pipe %v", nick, c.tls, c.websocket, piped)
			}
			if c.port == 0 || c.ip != "127.0.0.1" {
				t.Errorf("%s: port %d, ip %s", nick, c.port, c.ip)
			}
		}
	})

	select {
	case c := <-late:
		c.expect(" 001 Late ")
		c.send("JOIN #hive")
		c.expect(" 366 Late #hive ")
		plain.expect(":Late!~late@127.0.0.1 JOIN #hive")
	case <-time.After(10 * time.Second):
		t.Fatal("the client that connected during the hand-over is stuck")
	}
	// TLS and WebSocket ports still take new people.
	tlsLogin(t, n, "Secure2")
	web2 := wsDial(t, n, false, nil)
	web2.register("Web2")
	// And leaving works through the gate's pipe.
	secure.send("QUIT :done")
	secure.gone()
	plain.expect(":Secure!~secure@127.0.0.1 QUIT :Quit: done")
}

// A hand-over through the gate while plain and TLS clients are all
// talking: no line is lost, doubled or out of order.
func TestGateHandoverUnderChatter(t *testing.T) {
	g := newGateRigWith(t, noTargetLimit)
	cs := make([]*tc, 8)
	for i := range cs {
		if nick := fmt.Sprintf("bee%d", i); i%2 == 0 {
			cs[i] = login(t, g.s, nick)
		} else {
			cs[i] = tlsLogin(t, g.s, nick)
		}
	}
	chatter(t, cs, 150, func() {
		if _, err := g.handOver(nil); err != nil {
			t.Error(err)
		}
	})
}

// The next server can't read the snapshot (another format version): the
// clients are disconnected with a goodbye, the ports stay open and the
// server works.
func TestGateHandoverOtherVersion(t *testing.T) {
	g := newGateRig(t)
	plain := login(t, g.s, "Plain")
	secure := tlsLogin(t, g.s, "Secure")
	onLoop(t, g.s, func() { g.s.hand.version = handoverVersion + 1 })
	n, err := g.handOver(nil)
	if err != nil {
		t.Fatal(err)
	}
	plain.gone()
	secure.gone()
	onLoop(t, n, func() {
		if len(n.clients) != 0 || n.net.UserByNick("plain") != nil {
			t.Errorf("the next server kept someone from a snapshot it can't read")
		}
	})
	login(t, n, "Plain")
	tlsLogin(t, n, "Secure")
}

// The snapshot is cut short on the way: same thing.
func TestGateHandoverDamagedSnapshot(t *testing.T) {
	g := newGateRig(t)
	plain := login(t, g.s, "Plain")
	n, err := g.handOver(func(h *gate.Handover) { h.Snapshot = h.Snapshot[:len(h.Snapshot)/2] })
	if err != nil {
		t.Fatal(err)
	}
	plain.gone()
	login(t, n, "Plain")
}

// Two hand-overs in a row through the same gate, with sockets that were
// passed on before.
func TestGateHandoverTwice(t *testing.T) {
	g := newGateRig(t)
	plain := login(t, g.s, "Plain")
	secure := tlsLogin(t, g.s, "Secure")
	for _, c := range []*tc{plain, secure} {
		c.send("JOIN #hive")
		c.expect(" 366 " + c.nick + " #hive ")
	}
	plain.expect("JOIN #hive")
	for round := 1; round <= 2; round++ {
		// The rig's next server replaces g.s; its Serve is run by runNext,
		// so wire up a result channel for the round after.
		child, err := g.lp.Handover(10 * time.Second)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		select {
		case err := <-g.result:
			if !errors.Is(err, ErrHandover) {
				t.Fatalf("round %d: Serve returned %v", round, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the old server didn't stop", round)
		}
		cfg, err := config.Load(g.path)
		if err != nil {
			t.Fatal(err)
		}
		n, err := NewBehindGate(cfg, log.New(io.Discard, "", 0), child)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		g.s = n
		go func() { g.result <- n.Serve(context.Background()) }()
		secure.send(fmt.Sprintf("PRIVMSG #hive :round %d", round))
		plain.expect(fmt.Sprintf(":Secure!~secure@127.0.0.1 PRIVMSG #hive :round %d", round))
		plain.send(fmt.Sprintf("PRIVMSG #hive :back %d", round))
		if line := secure.expect(fmt.Sprintf("PRIVMSG #hive :back %d", round)); !strings.HasPrefix(line, ":Plain!") {
			t.Fatalf("round %d: %q", round, line)
		}
	}
	stopped := make(chan struct{})
	go func() {
		g.s.post(func() { g.s.stopAfter = stopDie })
		<-g.result
		g.lp.Close()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the last server didn't stop")
	}
}
