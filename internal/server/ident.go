package server

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Ident lookups (RFC 1413), off by default ([server] ident_lookup): the
// server asks port 113 of a connecting computer who owns the connection.
// With an answer the username shows without the leading ~.

// identTimeout covers the whole question: connecting and the answer.
const identTimeout = 3 * time.Second

// identStart asks c's ident server; registration waits for the answer.
func (s *Server) identStart(c *Client) {
	if !s.cfg.Server.IdentLookup || c.websocket || s.proxyPort(c.port) {
		return // behind a proxy the other end of the connection isn't theirs
	}
	ra, ok := c.conn.RemoteAddr().(*net.TCPAddr)
	la, ok2 := c.conn.LocalAddr().(*net.TCPAddr)
	if !ok || !ok2 {
		return
	}
	port := s.identPort
	if port == 0 {
		port = 113
	}
	c.identBusy = true
	s.notice(c, "*** Checking Ident")
	gen := c.lookupGen
	go func() {
		id := identQuery(la.IP, net.JoinHostPort(ra.IP.String(), strconv.Itoa(port)), ra.Port, la.Port, identTimeout)
		s.post(func() {
			if c.closed || c.lookupGen != gen {
				return
			}
			c.identBusy = false
			if id != "" {
				c.identAnswer = id
				s.notice(c, "*** Got Ident response")
			} else {
				s.notice(c, "*** No Ident response")
			}
			s.tryRegister(c)
		})
	}()
}

// proxyPort reports whether our port takes PROXY protocol connections.
func (s *Server) proxyPort(port int) bool {
	for _, l := range s.cfg.Listen {
		if !l.Proxy {
			continue
		}
		if _, p, err := net.SplitHostPort(l.Address); err == nil && p == strconv.Itoa(port) {
			return true
		}
	}
	return false
}

// identQuery asks the ident server at addr about the connection from
// theirPort (on their side) to ourPort; "" when there is no usable answer.
func identQuery(from net.IP, addr string, theirPort, ourPort int, timeout time.Duration) string {
	d := net.Dialer{Timeout: timeout, LocalAddr: &net.TCPAddr{IP: from}}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return ""
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintf(conn, "%d , %d\r\n", theirPort, ourPort); err != nil {
		return ""
	}
	line, _ := bufio.NewReader(&limitedConn{conn, 512}).ReadString('\n')
	return identUser(line, theirPort, ourPort)
}

// limitedConn stops reading after n bytes: an ident answer is one line.
type limitedConn struct {
	net.Conn
	n int
}

func (l *limitedConn) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("ident answer too long")
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	n, err := l.Conn.Read(p)
	l.n -= n
	return n, err
}

// identUser reads "<their port> , <our port> : USERID : <os> : <user>"
// and returns the user, cleaned for use as an ident; "" for anything else.
func identUser(line string, theirPort, ourPort int) string {
	f := strings.SplitN(strings.TrimRight(line, "\r\n"), ":", 4)
	if len(f) != 4 || !strings.EqualFold(strings.TrimSpace(f[1]), "USERID") {
		return ""
	}
	a, b, ok := strings.Cut(f[0], ",")
	if !ok || strings.TrimSpace(a) != strconv.Itoa(theirPort) || strings.TrimSpace(b) != strconv.Itoa(ourPort) {
		return ""
	}
	var out strings.Builder
	for _, ch := range []byte(strings.TrimSpace(f[3])) {
		if out.Len() == maxIdentLen {
			break
		}
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-' {
			out.WriteByte(ch)
		}
	}
	return out.String()
}
