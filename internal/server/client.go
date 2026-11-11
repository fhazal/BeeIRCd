package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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
	s     *Server
	conn  net.Conn
	uid   string
	dirty bool        // has queued lines its writer hasn't been woken for (see Server.flushWrites)
	user  *state.User // set once registered

	nick, ident, realname string // before registration
	host, ip              string
	tls                   bool
	registered            bool
	capBusy               bool // CAP negotiation not finished
	hostDone              bool
	closed                bool
	caps                  map[string]bool // IRCv3 capabilities this client asked for
	capture               *[]string       // lines held back for labeled-response

	// SASL and oper state (services keep their own record per user).
	pwBusy      bool   // a SASL or /oper password check is running
	saslMech    string // SASL mechanism in progress
	saslBuf     strings.Builder
	saslBusy    bool
	saslAccount *store.Account // SASL done before registration finished
	operFlags   string         // oper flags like "acghks", "" when not an oper
	operTitle   string
	operAccount int64     // the oper entry (account ID) c opered with
	confirm     string    // confirms AdminServ RESTART and DIE
	confirmTill time.Time // confirm is valid until then

	floodBurst, floodRate int // read by readLoop, fixed at connect

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
		host:        irc.DisplayIP(ip),
		tls:         isTLS,
		connectedAt: now,
		lastRecv:    now,
		lastMsg:     now,
		notify:      make(chan struct{}, 1),
		caps:        make(map[string]bool),
		floodBurst:  s.cfg.Limits.FloodBurst,
		floodRate:   s.cfg.Limits.FloodRate,
	}
	s.clients[c.uid] = c
	s.perIP[c.ip]++
	s.wg.Add(1)
	go c.writeLoop()
	go c.readLoop()
	if z := s.svc.ZlineFor(ip); z != nil {
		s.snotice("ZLINE %s stopped a connection from %s", z.Mask, ip)
		s.quit(c, "Z-lined: "+z.Reason)
		return
	}
	if n, max := s.sessions(ip), s.svc.SessionLimit(ip); n > max {
		s.snotice("Too many connections from %s (%d, the limit is %d)", ip, n, max)
		s.quit(c, fmt.Sprintf("Too many connections from your IP (the limit is %d)", max))
		return
	}

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
	c.queueLine(clip(line) + crlf)
}

// crlf ends every line sent.
const crlf = "\r\n"

// queueLine queues a line that is already clipped and ends in CRLF.
func (c *Client) queueLine(line string) {
	if c.capture != nil {
		*c.capture = append(*c.capture, strings.TrimSuffix(line, crlf))
		return
	}
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
	// The writer is woken once the loop finishes its batch of work, so
	// many lines go out in one write (InspIRCd's model) instead of one
	// write per line.
	if !c.dirty {
		c.dirty = true
		c.s.dirty = append(c.s.dirty, c)
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

		// One write per batch, from a buffer borrowed for just this write:
		// idle connections hold no write buffer.
		bp := writeBufs.Get().(*[]byte)
		buf := (*bp)[:0]
		for _, l := range q {
			buf = append(buf, l...)
		}
		c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, err := c.conn.Write(buf)
		if cap(buf) <= maxPooledWrite {
			*bp = buf
			writeBufs.Put(bp)
		}
		if err != nil {
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
	r := bufio.NewReaderSize(c.conn, readBufSize)
	gap := time.Second / time.Duration(c.floodRate)
	allow := time.Duration(c.floodBurst) * gap
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
	var long []byte // a line longer than the read buffer, put together here
	tooLong := false
	for {
		b, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if !tooLong {
				if long = append(long, b...); len(long) > maxInput {
					long, tooLong = nil, true
				}
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if tooLong || len(long)+len(b) > maxInput {
			return "", errTooLong
		}
		if long != nil {
			return strings.TrimRight(string(append(long, b...)), "\r\n"), nil
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
}

// readBufSize is each connection's read buffer: normal lines fit, longer
// ones (IRCv3 tags, up to maxInput) are put together on the side.
const readBufSize = 1024

// maxPooledWrite is the largest write buffer kept for reuse.
const maxPooledWrite = 64 << 10

// writeBufs are write buffers shared by all connections.
var writeBufs = sync.Pool{New: func() any { b := make([]byte, 0, 4096); return &b }}

// quit takes c off the server, tells its channels and hangs up.
func (s *Server) quit(c *Client, reason string) {
	if c.closed {
		return
	}
	c.closed = true
	c.capture = nil
	delete(s.clients, c.uid)
	if s.perIP[c.ip]--; s.perIP[c.ip] <= 0 {
		delete(s.perIP, c.ip)
	}
	if c.registered {
		s.local--
	}
	delete(s.opers, c)
	if c.user != nil {
		u := c.user
		s.emit(&state.UserQuit{UID: u.UID, Reason: reason})
		s.log.Printf("quit %s (%s): %s", u.Nick, c.ip, reason)
		quit := fmt.Sprintf("Quit: %s (%s@%s) [%s]", u.Nick, u.Ident, u.RealHost, reason)
		s.snoticeOnly(quit)
		s.svc.LogUserQuit(u, reason)
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
