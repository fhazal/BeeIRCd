package server

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// Connect throttle, unregistered caps, opers skip fakelag, PROXY.

// noExempt makes 127.0.0.1 count like any internet IP.
func noExempt(s *Server) { s.throttleSkip = func(string) bool { return false } }

// closedWith connects and reads until the server hangs up; it returns the
// ERROR line ("" when the connection was closed without one).
func closedWith(t *testing.T, addr string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(conn)
	for {
		l, err := r.ReadString('\n')
		if strings.HasPrefix(l, "ERROR ") {
			return strings.TrimSpace(l)
		}
		if err != nil {
			return ""
		}
	}
}

func TestConnectThrottle(t *testing.T) {
	s := testServerWith(t, func(c *config.Config) { c.Limits.ConnectRate = "3:60" }, noExempt)
	addr := s.Addrs()[0].String()
	for i := 0; i < 3; i++ {
		c := login(t, s, "ok"+strconv.Itoa(i))
		c.send("QUIT")
		c.expect("ERROR ")
	}
	if l := closedWith(t, addr); !strings.Contains(l, "Throttled: reconnecting too fast") {
		t.Fatalf("4th connection: %q", l)
	}
	closedWith(t, addr)
	closedWith(t, addr) // third refusal: ZLINE
	if l := closedWith(t, addr); !strings.Contains(l, "Z-lined: Reconnecting too fast") {
		t.Fatalf("after 3 refusals: %q", l)
	}
}

func TestUnregisteredCaps(t *testing.T) {
	s := testServerWith(t, func(c *config.Config) {
		c.Limits.ConnectRate = "off"
		c.Limits.MaxUnregisteredPerIP = 2
	}, noExempt)
	addr := s.Addrs()[0].String()
	a, b := raw(t, s), raw(t, s)
	a.nick, b.nick = "a", "b"
	a.sync()
	b.sync()
	if l := closedWith(t, addr); !strings.Contains(l, "still logging in") {
		t.Fatalf("3rd unregistered: %q", l)
	}
	a.send("NICK a")
	a.send("USER a 0 * :a")
	a.expect(" 001 a ")
	c := raw(t, s) // a logged in, so there is room again
	c.nick = "c"
	c.sync()

}

func TestUnregisteredTotalCap(t *testing.T) {
	s := testServerWith(t, func(c *config.Config) {
		c.Limits.ConnectRate = "off"
		c.Limits.MaxUnregisteredPerIP = 100
		c.Limits.MaxUnregistered = 10
	}, noExempt)
	for i := 0; i < 10; i++ {
		x := raw(t, s)
		x.sync()
	}
	if l := closedWith(t, s.Addrs()[0].String()); !strings.Contains(l, "The server is busy") {
		t.Fatalf("11th unregistered: %q", l)
	}
}

func TestLANSkipsThrottle(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Limits.ConnectRate = "2:60"; c.Limits.MaxUnregisteredPerIP = 1 })
	for i := 0; i < 5; i++ {
		x := raw(t, s)
		x.sync() // loopback: neither the rate nor the per-IP cap apply
	}
}

func TestOpersSkipFakelag(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("AS SET flood-burst 2")
	b.expect("flood-burst")
	b.send("AS SET flood-rate 1")
	b.expect("flood-rate")
	b.send("QUIT")
	b.expect("ERROR ")

	// A new connection gets burst 2, 1 line a second ...
	slow := identified(t, s, "Boss")
	for i := 0; i < 4; i++ {
		slow.send("PING :n" + strconv.Itoa(i))
	}
	slow.quiet("PONG irc.test.local :n3") // slowed down: a second per line
	waitLong(slow, "PONG irc.test.local :n3", 15*time.Second)
	// ... until it opers up: then it isn't slowed down any more.
	slow.send("OPER Boss " + operPW)
	slow.expect(" 381 Boss ")
	for i := 0; i < 20; i++ {
		slow.send("PING :o" + strconv.Itoa(i))
	}
	slow.expect("PONG irc.test.local :o19")
}

func proxyServer(t *testing.T, from string) *Server {
	t.Helper()
	return testServer(t, func(c *config.Config) {
		c.Listen = append(c.Listen, config.Listen{Address: "127.0.0.1:0", Proxy: true, ProxyFrom: []string{from}})
	})
}

func proxyLogin(t *testing.T, s *Server, header []byte, nick string) *tc {
	t.Helper()
	conn, err := net.Dial("tcp", s.Addrs()[2].String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.Write(header)
	c := &tc{t: t, conn: conn, r: bufio.NewReader(conn), nick: nick}
	c.send("NICK " + nick)
	c.send("USER " + nick + " 0 * :" + nick)
	c.expect(" 001 " + nick + " ")
	return c
}

func TestProxyProtocol(t *testing.T) {
	s := proxyServer(t, "127.0.0.0/8")
	v1 := proxyLogin(t, s, []byte("PROXY TCP4 203.0.113.9 127.0.0.1 5555 6667\r\n"), "viaone")
	v1.send("WHOIS viaone")
	if l := v1.expect(" 311 viaone viaone "); !strings.Contains(l, "203.0.113.9") {
		t.Errorf("v1 address not used: %s", l)
	}

	v2 := append([]byte(nil), proxyV2Sig...)
	v2 = append(v2, 0x21, 0x11, 0, 12)
	v2 = append(v2, 198, 51, 100, 7, 127, 0, 0, 1)
	v2 = binary.BigEndian.AppendUint16(v2, 4444)
	v2 = binary.BigEndian.AppendUint16(v2, 6667)
	two := proxyLogin(t, s, v2, "viatwo")
	two.send("WHOIS viatwo")
	if l := two.expect(" 311 viatwo viatwo "); !strings.Contains(l, "198.51.100.7") {
		t.Errorf("v2 address not used: %s", l)
	}

	// LOCAL keeps the proxy's own address; UNKNOWN too.
	local := append(append([]byte(nil), proxyV2Sig...), 0x20, 0x00, 0, 0)
	l := proxyLogin(t, s, local, "vialocal")
	l.send("WHOIS vialocal")
	l.expect(" 311 vialocal vialocal ~vialocal 127.0.0.1 ")
	proxyLogin(t, s, []byte("PROXY UNKNOWN\r\n"), "viaunknown")

	// A port with proxy = true never takes plain IRC, or garbage.
	for _, bad := range []string{"NICK x\r\nUSER x 0 * :x\r\n", "PROXY TCP4 nonsense\r\n", "PROXY TCP4 ::1 ::1 1 2\r\n"} {
		conn, err := net.Dial("tcp", s.Addrs()[2].String())
		if err != nil {
			t.Fatal(err)
		}
		conn.Write([]byte(bad))
		conn.SetReadDeadline(time.Now().Add(6 * time.Second))
		if n, _ := io.ReadAll(conn); len(n) != 0 {
			t.Errorf("%q: got %q (want a silent close)", bad, n)
		}
		conn.Close()
	}
}

func TestProxyOnlyFromProxy(t *testing.T) {
	s := proxyServer(t, "10.9.9.9")
	conn, err := net.Dial("tcp", s.Addrs()[2].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("PROXY TCP4 203.0.113.9 127.0.0.1 5555 6667\r\n"))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _ := io.ReadAll(conn); len(n) != 0 { // closed (or reset) with nothing said
		t.Errorf("connection from outside proxy_from got %q", n)
	}
}

func TestConfigChecksConnections(t *testing.T) {
	for _, c := range []struct {
		tweak func(*config.Config)
		want  string
	}{
		{func(c *config.Config) { c.Limits.ConnectRate = "fast" }, "connect_rate"},
		{func(c *config.Config) { c.Listen[0].Proxy = true }, "proxy_from"},
		{func(c *config.Config) { c.Listen[0].Proxy = true; c.Listen[0].ProxyFrom = []string{"nope"} }, "not an IP or CIDR"},
	} {
		cfg := &config.Config{
			Server:   config.Server{Name: "irc.test.local", Network: "BeeNet", SID: "0BE"},
			Listen:   []config.Listen{{Address: "127.0.0.1:0"}},
			Services: config.Services{Database: "x.db"},
			Cloak:    config.Cloak{Secret: "0123456789abcdef-test"},
		}
		c.tweak(cfg)
		cfg.SetDefaults()
		if err := cfg.Check(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want an error about %s, got %v", c.want, err)
		}
	}
}

// waitLong is expect with a longer wait.
func waitLong(c *tc, want string, d time.Duration) {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(d))
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("%s: waiting for %q: %v", c.nick, want, err)
		}
		if strings.Contains(line, want) {
			return
		}
	}
}
