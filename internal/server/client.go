package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// maxInput is the longest line we read: 8191 bytes of tags plus 512.
const maxInput = 8191 + 512

var errTooLong = errors.New("line too long")

// Client is one connection. Fields without a comment belong to the loop.
type Client struct {
	s    *Server
	conn net.Conn
	uid  string
	user *state.User // set once registered

	nick, ident, realname string // before registration
	host, ip              string
	tls                   bool
	registered            bool
	capBusy               bool // CAP negotiation not finished
	hostDone              bool
	closed                bool

	connectedAt time.Time
	lastRecv    time.Time
	lastMsg     time.Time // last PRIVMSG/NOTICE, for WHOIS idle
	pingSent    time.Time

	// The send queue is shared with the writer goroutine.
	mu      sync.Mutex
	queue   []string
	queued  int
	closing bool
	notify  chan struct{}
	over    bool
}

func (s *Server) newClient(conn net.Conn, isTLS bool) {
	now := time.Now()
	ip := remoteIP(conn)
	c := &Client{
		s:           s,
		conn:        conn,
		uid:         s.net.NextUID(),
		ip:          ip,
		host:        displayIP(ip),
		tls:         isTLS,
		connectedAt: now,
		lastRecv:    now,
		lastMsg:     now,
		notify:      make(chan struct{}, 1),
	}
	s.clients[c.uid] = c
	s.wg.Add(1)
	go c.writeLoop()
	go c.readLoop()

	if !s.cfg.ResolveHosts() {
		c.hostDone = true
		return
	}
	s.notice(c, "*** Looking up your hostname...")
	go func() {
		name, ok := s.resolve(context.Background(), ip)
		s.post(func() {
			if c.closed {
				return
			}
			if ok {
				c.host = name
				s.notice(c, "*** Found your hostname")
			} else {
				s.notice(c, "*** Couldn't look up your hostname")
			}
			c.hostDone = true
			s.tryRegister(c)
		})
	}()
}

func remoteIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// displayIP makes an IP safe to show as a host: an IPv6 address starting
// with ':' would break the IRC line format.
func displayIP(ip string) string {
	if strings.HasPrefix(ip, ":") {
		return "0" + ip
	}
	return ip
}

// target is the nick to address replies to, "*" before one is chosen.
func (c *Client) target() string {
	switch {
	case c.user != nil:
		return c.user.Nick
	case c.nick != "":
		return c.nick
	}
	return "*"
}

// send queues one line. Lines over 510 bytes are cut. Called from the loop.
func (c *Client) send(line string) {
	line = irc.Truncate(line, irc.MaxLine) + "\r\n"
	c.mu.Lock()
	if c.closing || c.over {
		c.mu.Unlock()
		return
	}
	if c.queued+len(line) > c.s.cfg.Limits.SendQ {
		c.over = true
		c.mu.Unlock()
		c.s.toKill = append(c.s.toKill, c)
		return
	}
	c.queue = append(c.queue, line)
	c.queued += len(line)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// close asks the writer to flush what is queued and then hang up.
func (c *Client) close() {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *Client) writeLoop() {
	defer c.s.wg.Done()
	w := bufio.NewWriter(c.conn)
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.closing {
			c.mu.Unlock()
			<-c.notify
			c.mu.Lock()
		}
		q := c.queue
		c.queue, c.queued = nil, 0
		closing := c.closing
		c.mu.Unlock()

		for _, l := range q {
			w.WriteString(l)
		}
		c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if err := w.Flush(); err != nil {
			c.conn.Close()
			return
		}
		if closing {
			c.mu.Lock()
			empty := len(c.queue) == 0
			c.mu.Unlock()
			if empty {
				c.conn.Close()
				return
			}
		}
	}
}

func (c *Client) readLoop() {
	r := bufio.NewReaderSize(c.conn, maxInput)
	for {
		line, err := readLine(r)
		switch {
		case errors.Is(err, errTooLong):
			c.s.post(func() { c.s.reply(c, irc.ErrInputTooLong, "Input line was too long") })
			continue
		case err != nil:
			reason := "Connection closed"
			if !errors.Is(err, io.EOF) {
				reason = "Read error"
			}
			c.s.post(func() { c.s.quit(c, reason) })
			return
		}
		c.s.post(func() { c.s.handleLine(c, line) })
	}
}

func readLine(r *bufio.Reader) (string, error) {
	tooLong := false
	for {
		b, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			tooLong = true
			continue
		}
		if err != nil {
			return "", err
		}
		if tooLong {
			return "", errTooLong
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
}

// quit takes c off the server, tells its channels and hangs up.
func (s *Server) quit(c *Client, reason string) {
	if c.closed {
		return
	}
	c.closed = true
	delete(s.clients, c.uid)
	if c.user != nil {
		u := c.user
		line := irc.Message{Source: u.Prefix(), Command: "QUIT", Params: []string{reason}, Trailing: true}.String()
		for _, n := range s.net.Neighbours(u) {
			s.sendTo(n, line)
		}
		s.addWhowas(u)
		s.net.RemoveUser(u)
		s.log.Printf("quit %s (%s): %s", u.Nick, c.ip, reason)
	}
	c.send("ERROR :Closing Link: " + c.host + " (" + reason + ")")
	c.close()
}

// sendTo queues a line for a user that is connected here.
func (s *Server) sendTo(u *state.User, line string) {
	if c := s.clients[u.UID]; c != nil {
		c.send(line)
	}
}

// reply sends a numeric whose last parameter is text.
func (s *Server) reply(c *Client, code string, params ...string) {
	p := append([]string{c.target()}, params...)
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: code, Params: p, Trailing: true}.String())
}

// replyBare sends a numeric whose parameters are all words (modes, times).
func (s *Server) replyBare(c *Client, code string, params ...string) {
	p := append([]string{c.target()}, params...)
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: code, Params: p}.String())
}

// notice sends a server notice to c.
func (s *Server) notice(c *Client, text string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "NOTICE", Params: []string{c.target(), text}, Trailing: true}.String())
}

// clientOf returns the connection of a local user.
func (s *Server) clientOf(u *state.User) *Client { return s.clients[u.UID] }
