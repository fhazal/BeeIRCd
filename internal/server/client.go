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
	"github.com/fhazal/beeircd/internal/store"
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
	caps                  map[string]bool // IRCv3 capabilities this client asked for
	capture               *[]string       // lines held back for labeled-response

	// Services state.
	accountID   int64     // NickServ account, 0 when not identified
	enforceAt   time.Time // rename to Guest at this time, unless identified
	enforceNick string    // the nick enforceAt is about
	pwBusy      bool      // a password check is running
	dropCode    string    // confirms /ns drop
	dropUntil   time.Time // dropCode is valid until then
	saslMech    string    // SASL mechanism in progress
	saslBuf     strings.Builder
	saslBusy    bool
	saslAccount *store.Account // SASL done before registration finished

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
		caps:        make(map[string]bool),
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

// send queues one line. The part after the tags is cut to 510 bytes.
// Called from the loop only.
func (c *Client) send(line string) {
	if c.capture != nil {
		*c.capture = append(*c.capture, line)
		return
	}
	line = clip(line) + "\r\n"
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

// clip cuts a line to the IRC limits: 510 bytes after the tags, and tags
// that are too long are dropped.
func clip(line string) string {
	if strings.HasPrefix(line, "@") {
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			tags, rest := line[:sp+1], line[sp+1:]
			if len(tags) > 8191 {
				tags = ""
			}
			return tags + irc.Truncate(rest, irc.MaxLine)
		}
	}
	return irc.Truncate(line, irc.MaxLine)
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
	lim := c.s.cfg.Limits
	gap := time.Second / time.Duration(lim.FloodRate)
	allow := time.Duration(lim.FloodBurst) * gap
	var clock time.Time
	for {
		line, err := readLine(r)
		// Flood control: every line moves this connection's clock forward.
		// Once it runs more than the burst ahead, reading pauses, so a
		// flooder is slowed down instead of filling the server's memory.
		if err == nil {
			now := time.Now()
			if clock.Before(now) {
				clock = now
			}
			clock = clock.Add(gap)
			if ahead := clock.Sub(now); ahead > allow {
				time.Sleep(ahead - allow)
			}
		}
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
	c.capture = nil
	delete(s.clients, c.uid)
	if c.user != nil {
		u := c.user
		s.touchAccount(c)
		s.toNeighbours(u, s.out(u, "QUIT", reason).text(), false)
		s.addWhowas(u)
		s.net.RemoveUser(u)
		s.log.Printf("quit %s (%s): %s", u.Nick, c.ip, reason)
	}
	c.send("ERROR :Closing Link: " + c.host + " (" + reason + ")")
	c.close()
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
