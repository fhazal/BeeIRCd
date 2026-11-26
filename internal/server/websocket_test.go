package server

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// wsc is a test WebSocket client.
type wsc struct {
	t     *testing.T
	conn  net.Conn
	r     *bufio.Reader
	proto string
	resp  *http.Response
}

func wsServer(t *testing.T, l config.Listen) *Server {
	t.Helper()
	return testServer(t, func(c *config.Config) {
		l.WebSocket = true
		if l.Address == "" {
			l.Address = "127.0.0.1:0"
		}
		c.Listen = append(c.Listen, l)
	})
}

// wsDial opens the upgrade; hdr adds or replaces request headers.
func wsDial(t *testing.T, s *Server, useTLS bool, hdr map[string]string) *wsc {
	t.Helper()
	addr := s.Addrs()[len(s.Addrs())-1].String()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if useTLS {
		conn = tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	k := make([]byte, 16)
	rand.Read(k)
	key := base64.StdEncoding.EncodeToString(k)
	h := map[string]string{"Upgrade": "websocket", "Connection": "Upgrade", "Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key": key, "Origin": "https://chat.example.org"}
	for n, v := range hdr {
		h[n] = v
	}
	var b strings.Builder
	b.WriteString("GET / HTTP/1.1\r\nHost: irc.test.local\r\n")
	for n, v := range h {
		if v != "" {
			b.WriteString(n + ": " + v + "\r\n")
		}
	}
	b.WriteString("\r\n")
	io.WriteString(conn, b.String())
	c := &wsc{t: t, conn: conn, r: bufio.NewReader(conn)}
	c.resp, err = http.ReadResponse(c.r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.resp.StatusCode == http.StatusSwitchingProtocols {
		sum := sha1.Sum([]byte(key + wsGUID))
		if got := c.resp.Header.Get("Sec-WebSocket-Accept"); got != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Fatalf("Sec-WebSocket-Accept %q is wrong", got)
		}
		c.proto = c.resp.Header.Get("Sec-WebSocket-Protocol")
	}
	return c
}

// frame sends one masked frame.
func (c *wsc) frame(op byte, fin bool, data []byte) {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	out := []byte{b0}
	switch n := len(data); {
	case n < 126:
		out = append(out, 0x80|byte(n))
	case n <= 0xffff:
		out = append(out, 0x80|126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, 0x80|127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	mask := []byte{0x12, 0x34, 0x56, 0x78}
	out = append(out, mask...)
	for i, ch := range data {
		out = append(out, ch^mask[i&3])
	}
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(out); err != nil {
		c.t.Fatal(err)
	}
}

func (c *wsc) send(line string) { c.frame(0x1, true, []byte(line)) }

// next reads one frame from the server.
func (c *wsc) next() (byte, []byte) {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		c.t.Fatalf("reading a frame: %v", err)
	}
	if h[1]&0x80 != 0 {
		c.t.Fatal("the server masked a frame")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(c.r, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(c.r, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(c.r, data); err != nil {
		c.t.Fatal(err)
	}
	return h[0] & 0x0f, data
}

// expect reads messages until one contains want; each must be one line.
func (c *wsc) expect(want string) string {
	c.t.Helper()
	var seen []string
	for range 200 {
		op, data := c.next()
		if op == 0x9 || op == 0xA {
			continue
		}
		if op == 0x8 {
			c.t.Fatalf("closed while waiting for %q; seen %q", want, seen)
		}
		line := string(data)
		if strings.ContainsAny(line, "\r\n") {
			c.t.Fatalf("message %q holds CR or LF", line)
		}
		if strings.HasPrefix(line, "PING ") {
			c.send("PONG " + line[5:])
		}
		if strings.Contains(line, want) {
			return line
		}
		seen = append(seen, line)
	}
	c.t.Fatalf("no %q in %q", want, seen)
	return ""
}

func (c *wsc) register(nick string) {
	c.send("NICK " + nick)
	c.send("USER " + nick + " 0 * :web user")
	c.expect(" 001 " + nick + " ")
}

func TestWebSocketChat(t *testing.T) {
	s := wsServer(t, config.Listen{})
	w := wsDial(t, s, false, map[string]string{"Sec-WebSocket-Protocol": "text.ircv3.net"})
	if w.resp.StatusCode != 101 || w.proto != "text.ircv3.net" {
		t.Fatalf("upgrade: %d, protocol %q", w.resp.StatusCode, w.proto)
	}
	w.register("webby")
	w.send("JOIN #web")
	w.expect(" 366 webby #web ")
	ann := login(t, s, "ann")
	ann.send("JOIN #web")
	ann.expect(" 366 ann #web ")
	w.expect(":ann!")

	ann.send("PRIVMSG #web :hi from tcp")
	w.expect("PRIVMSG #web :hi from tcp")
	// Trailing CR LF in a message is allowed and dropped.
	w.send("PRIVMSG #web :hi from the web\r\n")
	ann.expect(":webby!~webby@127.0.0.1 PRIVMSG #web :hi from the web")

	// A message in fragments, a ping between them, a long line.
	w.frame(0x1, false, []byte("PRIVMSG #web :frag"))
	w.frame(0x9, true, []byte("are you there"))
	if op, data := w.next(); op != 0xA || string(data) != "are you there" {
		t.Fatalf("ping answer: op %x %q", op, data)
	}
	w.frame(0x0, true, []byte("mented"))
	ann.expect("PRIVMSG #web :fragmented")
	long := strings.Repeat("b", 300)
	ann.send("PRIVMSG #web :" + long)
	w.expect(long)

	// Invalid UTF-8 from a TCP client reaches a text client as U+FFFD.
	ann.send("PRIVMSG #web :bad \xff byte")
	if l := w.expect("bad "); !strings.Contains(l, "bad � byte") {
		t.Errorf("got %q", l)
	}

	w.send("QUIT :bye")
	ann.expect(":webby!~webby@127.0.0.1 QUIT :Quit: bye")
	for {
		op, data := w.next()
		if op == 0x8 {
			if len(data) < 2 || binary.BigEndian.Uint16(data) != 1000 {
				t.Errorf("close frame %x", data)
			}
			break
		}
	}
}

func TestWebSocketProtocols(t *testing.T) {
	s := wsServer(t, config.Listen{Origins: []string{"*.example.org"}})
	b := wsDial(t, s, false, map[string]string{"Sec-WebSocket-Protocol": "text.ircv3.net, binary.ircv3.net"})
	if b.proto != "binary.ircv3.net" {
		t.Fatalf("both offered: got %q, want binary", b.proto)
	}
	b.register("bin")
	b.frame(0x2, true, []byte("PING :x"))
	if op, _ := b.next(); op != 0x2 {
		t.Errorf("binary client got op %x", op)
	}
	if n := wsDial(t, s, false, nil); n.resp.StatusCode != 101 || n.proto != "" {
		t.Errorf("no protocol asked: %d %q", n.resp.StatusCode, n.proto)
	}
	if o := wsDial(t, s, false, map[string]string{"Origin": "https://evil.test"}); o.resp.StatusCode != 403 {
		t.Errorf("other origin: %d, want 403", o.resp.StatusCode)
	}
	if g := wsDial(t, s, false, map[string]string{"Upgrade": "", "Connection": ""}); g.resp.StatusCode != 426 {
		t.Errorf("plain GET: %d, want 426", g.resp.StatusCode)
	}
	if v := wsDial(t, s, false, map[string]string{"Sec-WebSocket-Version": "8"}); v.resp.StatusCode != 400 {
		t.Errorf("version 8: %d, want 400", v.resp.StatusCode)
	}

	// Broken frames close the connection with the right code.
	for _, tc := range []struct {
		name string
		send func(*wsc)
		code uint16
	}{
		{"text that isn't UTF-8", func(c *wsc) { c.frame(0x1, true, []byte("PING \xff")) }, 1007},
		{"continuation without a start", func(c *wsc) { c.frame(0x0, true, []byte("x")) }, 1002},
		{"too big", func(c *wsc) { c.frame(0x1, true, make([]byte, wsMaxMessage+1)) }, 1009},
	} {
		c := wsDial(t, s, false, nil)
		tc.send(c)
		op, data := c.next()
		if op != 0x8 || len(data) < 2 || binary.BigEndian.Uint16(data) != tc.code {
			t.Errorf("%s: op %x %x, want close %d", tc.name, op, data, tc.code)
		}
	}
}

func TestWebSocketTLSAndProxy(t *testing.T) {
	s := wsServer(t, config.Listen{TLS: true})
	w := wsDial(t, s, true, nil)
	w.register("secure")
	w.send("WHOIS secure")
	w.expect(" 671 secure secure :is using a secure connection")

	p := wsServer(t, config.Listen{Proxy: true, ProxyFrom: []string{"127.0.0.1"}})
	v := wsDial(t, p, false, map[string]string{"X-Forwarded-For": "10.9.9.9, 203.0.113.7", "X-Forwarded-Proto": "https"})
	v.register("visitor")
	v.send("WHOIS visitor")
	v.expect(" 671 visitor visitor :is using a secure connection")
	v.send("USERHOST visitor")
	v.expect("visitor=+~visitor@203.0.113.7")
}

// The upgrade request is capped: one visitor can't fill the server's
// memory with endless headers in the seconds the handshake may take, while
// a big cookie still gets through.
func TestWebSocketHeaderTooBig(t *testing.T) {
	s := wsServer(t, config.Listen{})
	if c := wsDial(t, s, false, map[string]string{"Cookie": strings.Repeat("c", 20000)}); c.resp.StatusCode != 101 {
		t.Fatalf("20 KB cookie: %d, want 101", c.resp.StatusCode)
	}
	conn, err := net.Dial("tcp", s.Addrs()[len(s.Addrs())-1].String())
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.WriteString(conn, "GET / HTTP/1.1\r\nHost: irc.test.local\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nCookie: ")
		io.WriteString(conn, strings.Repeat("a", 2<<20))
		io.WriteString(conn, "\r\n\r\n")
	}()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	conn.Close()
	<-done
	if err == nil && resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("2 MB of headers: %d, want 431 (or the connection closed)", resp.StatusCode)
	}

	// The reply itself (over TCP the unread rest can reset it away).
	sv, cl := net.Pipe()
	defer cl.Close()
	code := make(chan int, 1)
	go func() {
		resp, err := http.ReadResponse(bufio.NewReader(cl), nil)
		if err != nil {
			code <- 0
			return
		}
		code <- resp.StatusCode
	}()
	req := "GET / HTTP/1.1\r\nHost: irc.test.local\r\nCookie: " + strings.Repeat("a", 2*wsMaxHeader) + "\r\n\r\n"
	_, err = wsHandshake(sv, bufio.NewReader(&headerCap{r: strings.NewReader(req), n: wsMaxHeader}), listener{}, nil)
	sv.Close()
	if got := <-code; err == nil || got != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("too big a request: reply %d, err %v; want 431", got, err)
	}
}
