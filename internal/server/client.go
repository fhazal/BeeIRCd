package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fhazal/beeircd/internal/gate"
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
	unregCounted          bool        // counted in Server.throttle as logging in
	noLag                 atomic.Bool // opers: readLoop skips flood slowdown
	capBusy               bool        // CAP negotiation not finished
	hostDone              bool
	dnsblBusy             bool         // a DNSBL lookup is running
	dnsblListed           *dnsblResult // a DNSBL hit, acted on once NICK and USER are in
	lookupGen             int          // bumped by WEBIRC: DNS/DNSBL answers for the old IP are dropped
	websocket             bool         // came in on a WebSocket port
	port                  int          // the port of ours they connected to
	webirc                string       // the [[webirc]] gateway this person came through, "" = none
	webircBusy            bool         // a hashed WEBIRC password is being checked
	tempShun              bool         // a spam filter shunned this connection (until it reconnects)
	shunLogged            bool         // a SHUN hit went to the services log once
	closed                bool
	caps                  map[string]bool      // IRCv3 capabilities this client asked for
	preAway               string               // AWAY sent before registration (draft/pre-away)
	mlIn                  *mlIn                // the draft/multiline batch being sent
	marks                 map[string]time.Time // read markers without an account (draft/read-marker)
	capture               *[]string            // lines held back for labeled-response

	// SASL and oper state (services keep their own record per user).
	pwBusy      bool     // a SASL or /oper password check is running
	heldLines   []string // lines that came in while a password check ran (O1)
	saslMech    string   // SASL mechanism in progress
	saslBuf     strings.Builder
	saslBusy    bool
	saslSeq     int            // counts relayed logins, so an old timeout is ignored
	adminFrom   string         // stand-in running AdminServ for an admin on that server
	operRelay   *operRelay     // a /oper sent to the services server, waiting (link_oper.go)
	operVia     *linkConn      // opered by the services server behind this link (its changes apply)
	operLookVia *linkConn      // leaf admin: asked this link for their WHOIS look
	saslAccount *store.Account // SASL done before registration finished

	// Soft bans and the login prompt (authprompt.go).
	authHeld    bool              // held at connect until /AUTH (or SASL) logs them in
	authUntil   time.Time         // when the hold runs out
	authReason  string            // the soft ban's reason, shown when it runs out
	authRelay   string            // leaf: the /AUTH login waiting for the services server
	authCmd     bool              // the login running is /AUTH (no SASL 903 for it)
	softReason  string            // a soft-reject DNSBL hit: only logged-in people pass
	certfp      string            // SHA-256 of the TLS client certificate, "" = none
	sessionLate bool              // over its IP's session limit at connect: check again with the hostname
	monitor     map[string]string // MONITOR list: folded nick -> nick as given
	watch       map[string]string // WATCH list, like monitor (watch.go)
	accept      map[string]bool   // ACCEPT list for user mode +g: UIDs (callerid.go)
	// greetWait holds the connect notices until the first line came, so a
	// web browser gets a clean HTTP answer ([server] web_redirect).
	greetWait   bool
	greetHeld   []string
	calleridAt  map[string]time.Time
	dccAllow    map[string]bool // DCCALLOW list: UIDs (dcc.go)
	dccWarned   bool            // told once what DCCALLOW is for
	amsgText    string          // [blockamsg]: the last channel text, where it went and when
	amsgTarget  string
	amsgAt      time.Time
	nickLocked  bool        // NICKLOCK: may not change nick
	identBusy   bool        // an ident lookup is running (ident.go)
	identAnswer string      // the username their identd gave
	silence     []string    // SILENCE masks (nick!ident@host)
	knocks      []time.Time // recent KNOCKs, for the knock flood limit
	operFlags   string      // oper flags like "abcghks", "" when not an oper
	operTitle   string
	operClass   string    // oper class in use ("" = flags only)
	operSwhois  []string  // extra WHOIS lines (320) while opered
	operVhost   string    // host shown because of the oper entry
	operAccount int64     // the oper entry (account ID) c opered with
	operReach   string    // store.ReachNetwork when c's admin powers count on other servers too
	snomask     string    // server notice letters (snomask.go), "" = none set
	confirm     string    // confirms AdminServ RESTART and DIE
	confirmTill time.Time // confirm is valid until then

	floodBurst, floodRate int // read by readLoop, fixed at connect

	connectedAt time.Time
	lastRecv    time.Time
	lastMsg     time.Time // last PRIVMSG/NOTICE, for WHOIS idle
	pingSent    time.Time
	cookie      string // the word of the first PING ([server] ping_cookie)
	cookieOK    bool   // they answered it

	// The send queue is shared with the writer goroutine.
	mu      sync.Mutex
	queue   []string
	queued  int
	closing bool
	notify  chan struct{}
	over    bool
	// Hand-over to the next server process (handover.go). handoff stops
	// the writer where it is: it leaves the queue alone and puts what a
	// started write didn't get out in outRest. A writer that ended on a
	// write error sets writeErr.
	handoff  bool
	outRest  []byte
	writeErr bool

	// frozen stops the reader between two lines. What it had read from
	// the connection but not handed to the loop is in inRest once
	// readDone is closed (inSkip: that is the rest of a line that was too
	// long, to be dropped); inFrozen tells that stop from one for an
	// error. floodAhead is how far the flood clock ran ahead.
	frozen     atomic.Bool
	readDone   chan struct{} // closed when readLoop returned
	writeDone  chan struct{} // closed when writeLoop returned
	inRest     []byte
	inSkip     bool
	inFrozen   bool
	floodAhead time.Duration
	// What the last server process left for this reader to start with.
	in0    []byte
	ahead0 time.Duration
}

func (s *Server) newClient(conn net.Conn, isTLS bool) {
	now := time.Now()
	ip := remoteIP(conn)
	_, ws := conn.(*wsConn)
	certfp := ""
	if gc, ok := conn.(*gate.Conn); ok { // TLS and WebSocket were done in the gate
		ws, certfp = gc.Info.WebSocket, gc.Info.CertFP
	}
	port := 0
	if a, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		port = a.Port
	}
	c := &Client{
		websocket:   ws,
		port:        port,
		s:           s,
		conn:        conn,
		uid:         s.net.NextUID(),
		ip:          ip,
		host:        irc.DisplayIP(ip),
		tls:         isTLS,
		certfp:      certfp,
		connectedAt: now,
		lastRecv:    now,
		lastMsg:     now,
		notify:      make(chan struct{}, 1),
		readDone:    make(chan struct{}),
		writeDone:   make(chan struct{}),
		caps:        make(map[string]bool),
		floodBurst:  s.cfg.Limits.FloodBurst,
		floodRate:   s.cfg.Limits.FloodRate,
		greetWait:   s.cfg.Server.WebRedirect != "" && !ws,
	}
	if s.hand.started {
		// Too late for this server: it is handing its clients over (only
		// without the gate, which holds new clients back itself).
		conn.Close()
		return
	}
	s.clients[c.uid] = c
	s.perIP[c.ip]++
	s.wg.Add(1)
	go c.writeLoop()
	go c.readLoop()
	if z := s.svc.ZlineFor(ip); z != nil {
		s.svc.LogBanHit(z.Kind, z.Mask, ip, z.SetBy, z.Reason)
		s.sno('x', "Z-line %s stopped a connection from %s", z.Mask, ip)
		s.quit(c, "Z-lined: "+z.Reason)
		return
	}
	if why := s.admitConn(c, now); why != "" {
		s.quit(c, why)
		return
	}
	if n, max := s.sessions(ip), s.svc.SessionLimit(ip); n > max && !s.webircGateway(ip) {
		if s.cfg.ResolveHosts() && s.svc.HostExcepts() {
			c.sessionLate = true // an exception on its hostname may still let it in
		} else {
			s.svc.LogSessionLimit(ip, max, c.logMask(c.nick))
			s.sno('a', "Client %s with IP %s rejected: maxperip limit exceeded (%d local, max %d)", orStr(c.nick, "[Unknown user]"), ip, n, max)
			s.quit(c, s.cfg.Messages.TooManyText())
			return
		}
	}
	s.dnsblStart(c)
	if c.closed {
		return
	}
	s.identStart(c)

	if !s.cfg.ResolveHosts() {
		c.hostDone = true
		return
	}
	s.notice(c, "*** Looking up your hostname...")
	gen := c.lookupGen
	go func() {
		name, ok := s.resolve(context.Background(), ip)
		s.post(func() {
			if c.closed || c.lookupGen != gen {
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
	line = irc.CleanFraming(line)
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
	defer close(c.writeDone)
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.closing && !c.handoff {
			c.mu.Unlock()
			<-c.notify
			c.mu.Lock()
		}
		if c.handoff {
			c.mu.Unlock()
			return // the queue goes to the next server process
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
		n, err := c.conn.Write(buf)
		handed := false
		if err != nil {
			c.mu.Lock()
			if c.handoff && errors.Is(err, os.ErrDeadlineExceeded) {
				// Cut short by the hand-over: the rest of this write
				// goes out from the next server process.
				c.outRest, handed = append([]byte(nil), buf[n:]...), true
			} else {
				c.writeErr = true
			}
			c.mu.Unlock()
		}
		if cap(buf) <= maxPooledWrite {
			*bp = buf
			writeBufs.Put(bp)
		}
		if handed {
			return
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
	defer close(c.readDone)
	// A TLS client certificate is fingerprinted before any line is read,
	// so the loop knows it when NICK and USER arrive.
	if tc, ok := c.conn.(*tls.Conn); ok && tc.Handshake() == nil {
		if fp := certFP(tc.ConnectionState()); fp != "" {
			c.s.post(func() { c.certfp = fp })
		}
	}
	var src io.Reader = c.conn
	if len(c.in0) > 0 {
		// Taken over from the last server process: what it had read
		// already comes first.
		src = io.MultiReader(bytes.NewReader(c.in0), c.conn)
		c.in0 = nil
	}
	lr := lineReader{r: bufio.NewReaderSize(src, readBufSize)}
	gap := time.Second / time.Duration(c.floodRate)
	allow := time.Duration(c.floodBurst) * gap
	var clock time.Time
	if c.ahead0 > 0 {
		clock = time.Now().Add(c.ahead0)
	}
	var batch mlRead
	for {
		// A hand-over stops the reader here, between two lines: either
		// right after one was handed in, or woken from a read that had no
		// full line yet (the loop sets a read deadline in the past).
		if c.frozen.Load() {
			c.freeze(&lr, clock)
			return
		}
		line, err := lr.read()
		if err != nil && c.frozen.Load() && errors.Is(err, os.ErrDeadlineExceeded) {
			c.freeze(&lr, clock)
			return
		}
		var lines []string
		if err == nil {
			lines = irc.SplitFraming(line)
		}
		// Flood control: every line moves this connection's clock forward.
		// Once it runs more than the burst ahead, reading pauses, so a
		// flooder is slowed down instead of filling the server's memory.
		if err == nil {
			now := time.Now()
			if clock.Before(now) {
				clock = now
			}
			cost := 0
			for _, l := range lines {
				if !batch.free(l) {
					cost++
				}
			}
			if len(lines) == 0 {
				cost = 1
			}
			clock = clock.Add(gap * time.Duration(cost))
			if c.noLag.Load() {
				clock = now // opers aren't slowed down (UnrealIRCd, InspIRCd)
			} else if ahead := clock.Sub(now); ahead > allow {
				c.s.floodSleep(ahead - allow)
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
		for _, l := range lines {
			if inputTooLong(l) {
				c.s.post(func() { c.s.reply(c, irc.ErrInputTooLong, "Input line was too long") })
				continue
			}
			c.s.post(func() { c.s.handleLineSafe(c, l) })
		}
	}
}

// Client input limits (IRCv3 message-tags): the tag section, '@' and the
// space after it included, holds at most 4096 bytes; the rest of the line
// at most 510 (512 with CRLF). Longer lines are refused with 417, not cut.
const (
	maxInputTags = 4096
	maxInputBody = irc.MaxLine
)

// inputTooLong reports whether line (without CRLF) breaks those limits.
func inputTooLong(line string) bool {
	if strings.HasPrefix(line, "@") {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return len(line) > maxInputTags
		}
		if sp+1 > maxInputTags {
			return true
		}
		line = line[sp+1:]
	}
	return len(line) > maxInputBody
}

func readLine(r *bufio.Reader) (string, error) {
	lr := lineReader{r: r}
	return lr.read()
}

// lineReader reads a connection line by line. Between two calls of read
// it can say what it took from the connection but hasn't returned as a
// line yet (rest), so another reader can go on from there.
type lineReader struct {
	r       *bufio.Reader
	long    []byte // the line being read, as far as it left the read buffer
	tooLong bool   // that line is over the limit: the rest of it is dropped
}

func (lr *lineReader) read() (string, error) {
	for {
		b, err := lr.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			if !lr.tooLong {
				if lr.long = append(lr.long, b...); len(lr.long) > maxInput {
					lr.long, lr.tooLong = nil, true
				}
			}
			continue
		}
		if err != nil {
			// ReadSlice gave the start of a line with the error and took
			// it out of its buffer: keep it for rest.
			if !lr.tooLong && len(b) > 0 {
				lr.long = append(lr.long, b...)
			}
			return "", err
		}
		long, tooLong := lr.long, lr.tooLong
		lr.long, lr.tooLong = nil, false
		if tooLong || len(long)+len(b) > maxInput {
			return "", errTooLong
		}
		if long != nil {
			return strings.TrimRight(string(append(long, b...)), "\r\n"), nil
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
}

// rest returns a copy of the bytes read from the connection that no line
// returned by read covers: the start of a line and whole lines behind it.
// skip: they begin inside a line that was too long.
func (lr *lineReader) rest() (b []byte, skip bool) {
	b = append(b, lr.long...)
	if n := lr.r.Buffered(); n > 0 {
		p, _ := lr.r.Peek(n)
		b = append(b, p...)
	}
	return b, lr.tooLong
}

// freeze ends readLoop for a hand-over: it notes what the next server
// process's reader starts with.
func (c *Client) freeze(lr *lineReader, clock time.Time) {
	c.inRest, c.inSkip = lr.rest()
	if ahead := time.Until(clock); ahead > 0 {
		c.floodAhead = ahead
	}
	c.inFrozen = true
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
	s.quitAs(c, reason, reason)
}

// quitAs is quit where others see reason but c itself (and the server
// log and quit notice) the real one, own.
func (s *Server) quitAs(c *Client, reason, own string) {
	if c.closed {
		return
	}
	c.closed = true
	c.capture = nil
	c.heldLines = nil
	delete(s.held, c)
	delete(s.clients, c.uid)
	s.monitorDrop(c)
	if s.perIP[c.ip]--; s.perIP[c.ip] <= 0 {
		delete(s.perIP, c.ip)
	}
	s.doneUnregistered(c)
	if c.registered {
		s.local--
	}
	delete(s.opers, c)
	if c.user != nil {
		u := c.user
		s.emit(&state.UserQuit{UID: u.UID, Reason: reason})
		s.log.Printf("quit %s (%s): %s", u.Nick, c.ip, own)
		s.snoOnly('q', "Client exiting: "+snoMaskIP(u)+" ["+own+"]")
		s.svc.LogUserQuit(u, own)
	}
	c.send("ERROR :Closing Link: " + c.host + " (" + own + ")")
	c.close()
}

// banQuit disconnects c for a ban: what is "Banned" or "K-Lined". It sends
// [messages] ban_notice first; others see the ban_quit wording, c itself
// the real reason.
func (s *Server) banQuit(c *Client, what, reason string) {
	m := &s.cfg.Messages
	if m.BanNotice != "" && !c.closed {
		c.send(irc.Message{Source: s.cfg.Server.Name, Command: "NOTICE", Params: []string{c.target(), m.BanNotice}, Trailing: true}.String())
	}
	s.quitAs(c, m.BanQuitText(what, reason), what+": "+reason)
}

// killedBy is who a KILL by by is shown as: [messages] hide_killed_by
// when set, else by itself. Kills this server makes keep its name.
func (s *Server) killedBy(by string) string {
	if h := s.cfg.Messages.HideKilledBy; h != "" && by != s.cfg.Server.Name {
		return h
	}
	return by
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
	if c.greetWait {
		c.greetHeld = append(c.greetHeld, text)
		return
	}
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "NOTICE", Params: []string{c.target(), text}, Trailing: true}.String())
}

// stdReply sends an IRCv3 standard reply (kind FAIL, WARN or NOTE) to a
// client that asked for standard-replies, and a server notice to everyone
// else: legacy, or "*** "+text when legacy is empty.
func (s *Server) stdReply(c *Client, kind, cmd, code, text, legacy string, context ...string) {
	if c.caps["standard-replies"] {
		p := append(append([]string{cmd, code}, context...), text)
		c.send(irc.Message{Source: s.cfg.Server.Name, Command: kind, Params: p, Trailing: true}.String())
		return
	}
	if legacy == "" {
		legacy = "*** " + text
	}
	s.notice(c, legacy)
}

// clientOf returns the connection of a local user.
func (s *Server) clientOf(u *state.User) *Client { return s.clients[u.UID] }
