package server

import (
	"bytes"
	"log"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/link"
)

// syncBuf is a log the test reads while the server writes it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// linkNode is one test server in a linked network.
type linkNode struct {
	name, sid string
	role      string
	port      int
	cert, key string
	fp        string
	blocks    []config.LinkServer
	services  bool // runs services (one server per network, 2c)
	s         *Server
	log       *syncBuf
}

// linkLeakCheck checks for leftover goroutines once every server of the
// test stopped. Call it before starting any.
func linkLeakCheck(t *testing.T) {
	check := goroutineBaseline(t)
	skipLeakCheck = true
	t.Cleanup(func() { // runs after every server's own cleanup
		skipLeakCheck = false
		check()
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newLinkNode makes the node's link certificate up front, so other nodes
// can pin its fingerprint before it starts.
func newLinkNode(t *testing.T, name, sid, role string) *linkNode {
	t.Helper()
	if !skipLeakCheck {
		linkLeakCheck(t)
	}
	dir := t.TempDir()
	n := &linkNode{name: name, sid: sid, role: role, services: role == "hub", port: freePort(t), cert: filepath.Join(dir, "link-cert.pem"), key: filepath.Join(dir, "link-key.pem")}
	_, fp, err := link.LoadOrCreate(n.cert, n.key, name)
	if err != nil {
		t.Fatal(err)
	}
	n.fp = fp
	return n
}

// expects says n links with other; dial makes n connect to it.
func (n *linkNode) expects(other *linkNode, dial bool, password string) {
	b := config.LinkServer{Name: other.name, Fingerprint: other.fp, Password: password}
	if dial {
		b.Host, b.Port, b.Autoconnect = "127.0.0.1", other.port, true
	}
	n.blocks = append(n.blocks, b)
}

func (n *linkNode) start(t *testing.T) {
	t.Helper()
	n.log = &syncBuf{}
	n.s = testServerWith(t, func(c *config.Config) {
		c.Server.Name, c.Server.SID, c.Server.Description = n.name, n.sid, "test "+n.name
		c.Link = config.Link{Role: n.role, Port: n.port, Cert: n.cert, Key: n.key, Servers: n.blocks}
		on := n.services
		c.Services.Enabled = &on
	}, func(s *Server) { s.log = log.New(n.log, "", 0) })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("%s log:\n%s", n.name, n.log)
		}
	})
}

// links returns the node's link status (on its loop).
func (n *linkNode) servers() map[string]string {
	out := map[string]string{}
	n.s.onLoop(func() {
		for sid, r := range n.s.links.servers {
			out[sid] = r.name
		}
	})
	return out
}

// waitFor polls cond for up to 5 seconds.
func waitLink(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (n *linkNode) logHas(text string) bool { return strings.Contains(n.log.String(), text) }

func TestLinkHubLeaf(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "s3cret")
	leaf.expects(hub, true, "s3cret")
	hub.start(t)
	leaf.start(t)
	waitLink(t, "the link", func() bool { return len(hub.servers()) == 1 && len(leaf.servers()) == 1 })
	if got := hub.servers()["0LF"]; got != "leaf.test.local" {
		t.Errorf("hub knows %v", hub.servers())
	}
	waitLink(t, "sync notices", func() bool {
		return hub.logHas("Link with leaf.test.local (0LF") && hub.logHas("sync done") && leaf.logHas("Link with hub.test.local (0HB") && leaf.logHas("we connected")
	})
	if !hub.logHas("BeeIRCd-") {
		t.Errorf("hub log doesn't name the software:\n%s", hub.log)
	}

	// status
	var st []string
	hub.s.onLoop(func() {
		for _, l := range hub.s.linkStatus() {
			st = append(st, l.Name+" "+l.State+" "+l.SID)
		}
	})
	if len(st) != 1 || st[0] != "leaf.test.local up 0LF" {
		t.Errorf("hub status %v", st)
	}

	// PING after a quiet minute, PONG gives the lag
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.lastRecv = time.Now().Add(-linkPingIdle - time.Second)
		}
		hub.s.linkTick(time.Now())
	})
	waitLink(t, "PONG", func() bool {
		var ok bool
		hub.s.onLoop(func() {
			for l := range hub.s.links.conns {
				ok = l.pingSent.IsZero() && !l.lastPong.IsZero()
			}
		})
		return ok
	})

	// the leaf goes away: the hub sees it split, the leaf's autoconnect
	// would bring it back
	stopServer(leaf.s)
	waitLink(t, "the split", func() bool { return len(hub.servers()) == 0 })
	waitLink(t, "lost notice", func() bool { return hub.logHas("Link with leaf.test.local lost after") })
}

func TestLinkRefusals(t *testing.T) {
	t.Run("wrong fingerprint", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		impostor := newLinkNode(t, "x.test.local", "0XX", "leaf")
		hub.expects(impostor, false, "") // the hub pins someone else's key
		hub.blocks[0].Name = "leaf.test.local"
		leaf.expects(hub, true, "")
		hub.start(t)
		leaf.start(t)
		waitLink(t, "refusal", func() bool {
			return hub.logHas("Link with leaf.test.local refused: Link denied: certificate fingerprint does not match")
		})
		if len(hub.servers()) != 0 {
			t.Errorf("hub linked anyway: %v", hub.servers())
		}
	})
	t.Run("leaf pins a wrong hub key: no PASS sent", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		hub.expects(leaf, false, "s3cret")
		leaf.expects(hub, true, "s3cret")
		leaf.blocks[0].Fingerprint = leaf.fp // not the hub's
		hub.start(t)
		leaf.start(t)
		waitLink(t, "refusal", func() bool {
			return leaf.logHas("Link with hub.test.local refused: its certificate fingerprint")
		})
		if hub.logHas("wrong password") || hub.logHas("established") {
			t.Errorf("hub got further than TLS:\n%s", hub.log)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		hub.expects(leaf, false, "right")
		leaf.expects(hub, true, "wrong")
		hub.start(t)
		leaf.start(t)
		waitLink(t, "refusal", func() bool { return hub.logHas("refused: Link denied: wrong password") })
		waitLink(t, "the leaf hears why", func() bool { return leaf.logHas("wrong password") })
	})
	t.Run("plain TCP on the link port", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		hub.start(t)
		c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(hub.port)))
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("PASS :x\r\nPROTOCTL EAUTH=leaf.test.local\r\n"))
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 100)
		c.Read(buf)
		c.Close()
		waitLink(t, "TLS failure in the log", func() bool { return hub.logHas("TLS with") })
		if len(hub.servers()) != 0 {
			t.Error("linked over plain TCP")
		}
	})
}

// A hub with two leaves: each leaf learns of the other through SID, and a
// leaf leaving reaches the other as SQUIT.
func TestLinkTree(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	a := newLinkNode(t, "a.test.local", "0AA", "leaf")
	b := newLinkNode(t, "b.test.local", "0BB", "leaf")
	hub.expects(a, false, "")
	hub.expects(b, false, "")
	a.expects(hub, true, "")
	b.expects(hub, true, "")
	hub.start(t)
	a.start(t)
	waitLink(t, "a linked", func() bool { return len(hub.servers()) == 1 })
	b.start(t)
	waitLink(t, "b knows a", func() bool { return b.servers()["0AA"] == "a.test.local" && b.servers()["0HB"] == "hub.test.local" })
	waitLink(t, "a knows b", func() bool { return a.servers()["0BB"] == "b.test.local" })
	waitLink(t, "joined notice", func() bool { return a.logHas("Server b.test.local (0BB) joined the network behind hub.test.local") })
	var hops int
	a.s.onLoop(func() { hops = a.s.links.servers["0BB"].hops })
	if hops != 2 {
		t.Errorf("a sees b %d hops away, want 2", hops)
	}
	stopServer(b.s)
	waitLink(t, "a sees b split", func() bool { _, ok := a.servers()["0BB"]; return !ok })
	if _, ok := a.servers()["0HB"]; !ok {
		t.Error("a lost the hub too")
	}
	waitLink(t, "split notice", func() bool { return a.logHas("Server b.test.local split from hub.test.local") })
}

// Three hubs in a ring: the link that would close the loop is refused.
func TestLinkLoop(t *testing.T) {
	x := newLinkNode(t, "x.test.local", "0XA", "hub")
	y := newLinkNode(t, "y.test.local", "0YA", "hub")
	z := newLinkNode(t, "z.test.local", "0ZA", "hub")
	y.services, z.services = false, false
	x.expects(y, true, "")
	y.expects(x, false, "")
	y.expects(z, true, "")
	z.expects(y, false, "")
	z.expects(x, false, "")
	z.start(t) // listeners first: y dials z, x dials y
	y.start(t)
	waitLink(t, "y-z", func() bool { return len(z.servers()) == 1 })
	x.start(t)
	waitLink(t, "x knows y and z", func() bool { return len(x.servers()) == 2 })
	// now x is told to connect to z as well: a loop
	x.s.onLoop(func() {
		x.s.cfg.Link.Servers = append(x.s.cfg.Link.Servers, config.LinkServer{Name: "z.test.local", Host: "127.0.0.1", Port: z.port, Fingerprint: z.fp, Autoconnect: true, SendQMB: 20})
		x.s.linkTick(time.Now())
	})
	time.Sleep(500 * time.Millisecond)
	if n := len(x.servers()); n != 2 {
		t.Errorf("x knows %d servers, want 2: %v", n, x.servers())
	}
	var conns int
	x.s.onLoop(func() { conns = len(x.s.links.conns) })
	if conns != 1 {
		t.Errorf("x has %d links, want 1 (the loop must not link)", conns)
	}
}

func TestLinkSendQ(t *testing.T) {
	old := linkRetryFirst
	linkRetryFirst = 100 * time.Millisecond
	t.Cleanup(func() { linkRetryFirst = old })
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	hub.start(t)
	leaf.start(t)
	waitLink(t, "the link", func() bool { return len(hub.servers()) == 1 })
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.limit = 100
			l.send(strings.Repeat("x", 200))
		}
	})
	waitLink(t, "sendq drop", func() bool { return hub.logHas("SendQ exceeded") })
	// the leaf dials again by itself
	waitLink(t, "relink", func() bool { return strings.Count(hub.log.String(), "established") >= 2 })
}

func itoa(n int) string { return strconv.Itoa(n) }
