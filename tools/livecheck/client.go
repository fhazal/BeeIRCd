package main

import (
	"bufio"
	"crypto/tls"
	"net"
	"regexp"
	"strings"
	"time"
)

// client is one test connection. wait() works like the old Python
// scripts: it searches what arrived so far, then reads more until the
// pattern shows up or the time runs out, and forgets everything up to the
// line that matched.
type client struct {
	nick  string
	conn  net.Conn
	r     *bufio.Reader
	lines []string
	sasl  bool // SASL EXTERNAL succeeded (TLS clients that asked for it)
}

func (c *client) send(line string) {
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	c.conn.Write([]byte(line + "\r\n"))
}

// read takes in whatever arrives within d.
func (c *client) read(d time.Duration) {
	c.conn.SetReadDeadline(time.Now().Add(d))
	for {
		line, err := c.r.ReadString('\n')
		if line != "" && strings.HasSuffix(line, "\n") {
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "PING") {
				c.send("PONG" + line[4:])
			}
			c.lines = append(c.lines, line)
		}
		if err != nil {
			return
		}
	}
}

// wait returns the first line matching pattern within t (5 s default), or "".
func (c *client) wait(pattern string, t ...time.Duration) string {
	limit := 5 * time.Second
	if len(t) > 0 {
		limit = t[0]
	}
	re := regexp.MustCompile(pattern)
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		for i, l := range c.lines {
			if re.MatchString(l) {
				c.lines = c.lines[i+1:]
				return l
			}
		}
		c.read(200 * time.Millisecond)
	}
	return ""
}

func (c *client) clear() {
	c.read(300 * time.Millisecond)
	c.lines = nil
}

func (c *client) has(sub string) bool {
	for _, l := range c.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// connect registers nick on the plain port, asking for caps first.
func (lc *liveCheck) connect(nick, caps string) *client {
	conn, err := net.DialTimeout("tcp", lc.addr, 10*time.Second)
	if err != nil {
		lc.fatal("connect %s: %v", nick, err)
	}
	c := &client{nick: nick, conn: conn, r: bufio.NewReader(conn)}
	lc.all = append(lc.all, c)
	if caps != "" {
		c.send("CAP REQ :" + caps)
		c.send("CAP END")
	}
	c.send("NICK " + nick)
	c.send("USER zt 0 * :live test")
	c.wait(" 001 ")
	return c
}

// connectTLS registers nick on the TLS port, with the test client
// certificate when withCert, trying SASL EXTERNAL when sasl.
func (lc *liveCheck) connectTLS(nick string, withCert, sasl bool) *client {
	cfg := &tls.Config{InsecureSkipVerify: true} // the test server's certificate is self-signed
	if withCert {
		cert, err := tls.LoadX509KeyPair(lc.cert, lc.key)
		if err != nil {
			lc.fatal("client certificate: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", lc.tlsAddr, cfg)
	if err != nil {
		lc.fatal("connect %s over TLS: %v", nick, err)
	}
	c := &client{nick: nick, conn: conn, r: bufio.NewReader(conn)}
	lc.all = append(lc.all, c)
	if sasl {
		c.send("CAP REQ :sasl")
		c.send("AUTHENTICATE EXTERNAL")
		c.wait(`AUTHENTICATE \+`)
		c.send("AUTHENTICATE +")
		c.sasl = c.wait(" 903 ") != ""
		c.send("CAP END")
	}
	c.send("NICK " + nick)
	c.send("USER zt 0 * :live test")
	c.wait(" 001 ")
	return c
}
