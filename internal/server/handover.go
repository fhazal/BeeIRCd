package server

// Hand-over: one server process passes its clients to the next one, so an
// upgrade doesn't disconnect anybody. It only works behind the gate
// (internal/gate), which keeps the client ports, the TLS sessions and the
// WebSocket framing while the server process is replaced.
//
// The old server (startHandover, on the loop):
//
//  1. stops every client's reader between two lines; what a reader had
//     taken from the connection but not handed to the loop is kept;
//  2. waits a moment for password checks and lookups that are under way;
//  3. drops its server links (the other side sees a split; links are not
//     handed over), stops every client's writer and keeps what wasn't
//     sent;
//  4. writes a snapshot: the network, every client's own state with its
//     unread input and unsent output, and what the server and services
//     only keep in memory;
//  5. gives the gate the snapshot and each client's socket, saves what a
//     normal stop saves and ends (Serve returns ErrHandover).
//
// The new server (takeOver, before its loop starts) loads the snapshot
// into its freshly started network, where its own service bots already
// are, and goes on reading and writing each socket where the old one
// stopped. No events run for that: nobody joined or connected.
//
// It is never worse than a restart. A snapshot the new server can't use
// (another format version, another server name) is thrown away, its
// clients are disconnected and the server starts empty. The format has a
// version number: bump handoverVersion whenever a field changes meaning or
// a new one can't safely be empty. A newer server reads an older snapshot
// of the same version (missing fields are empty); unknown fields are
// ignored. Fields are found by name, so renaming one is a format change.

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/gate"
	"github.com/fhazal/beeircd/internal/history"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/version"
)

const (
	handoverMagic = "BEEIRCD-HANDOVER"
	// handoverVersion is the snapshot format. A server only loads its
	// own version; any other one means "start empty".
	handoverVersion = 1
)

// HandoverVersion is the snapshot format this program writes and loads
// (`beeircd handover-version`, Status.HandoverVersion).
const HandoverVersion = handoverVersion

// How long the old server waits at each step; tests shorten them.
var (
	handoverReadWait   = 5 * time.Second        // for readers to stop
	handoverSettleWait = 2 * time.Second        // for password checks and lookups under way
	handoverWriteWait  = 2 * time.Second        // for writers to stop
	handoverSettleStep = 25 * time.Millisecond  // between two looks at them
	handoverGoodbye    = 200 * time.Millisecond // to tell one client it can't come along
)

// ErrHandover is returned by Serve when the server handed its clients to
// the next server process and stopped.
var ErrHandover = errors.New("handed over to the next server process")

// ErrHandoverBroken is returned by New when taking over the last server's
// clients failed half-way. The process should end, so that a clean one
// starts (the gate does that).
var ErrHandoverBroken = errors.New("taking over the last server's clients failed")

var errHandoverVersion = errors.New("the hand-over snapshot has another format version")

// handState is the server's hand-over bookkeeping.
type handState struct {
	started bool          // handing over: no new clients, no second hand-over
	freeze  chan struct{} // closed when it starts: wakes readers from flood pauses
	// give sends the hand-over away; nil: to the gate. Tests set it to
	// pass a hand-over on inside one process; keepConns then says the
	// connections went along as they are, so this server must not close
	// them.
	give       func(snapshot []byte, conns map[string]net.Conn) error
	keepConns  bool
	version    int    // tests: write this format version (0: handoverVersion)
	beforeSnap func() // tests: runs when readers and writers have stopped, before the snapshot
	// took is how this process took over from the last one (status, for
	// beeircd update); nil when it started fresh.
	took *control.Handover
}

// handoverSnap is everything one server process tells the next.
type handoverSnap struct {
	// Server, SID and Casemapping must be the loading server's own.
	Server, SID, Casemapping string
	From                     string // the version that wrote it, for the log
	Taken                    time.Time
	Net                      *state.Snapshot
	Clients                  []clientSnap
	Services                 *services.Handover
	MaxLocal, MaxGlobal      int
	DayPeak                  int
	DayPeakAt                time.Time
	TimedBans                []timedBanSnap
	FloodUnsets              []floodUnsetSnap
	// History is the channel history when it is kept in memory only
	// ([history] secret not set). With a secret it goes through the
	// history file, as at every stop and start.
	History history.Snapshot
}

type timedBanSnap struct {
	Channel string
	Mode    byte
	Mask    string
	Until   time.Time
}

type floodUnsetSnap struct {
	Channel string
	Mode    byte
	Until   time.Time
}

// clientSnap is one connection: what Client holds, as far as it means
// something to another process. Lookups and checks under way aren't in
// it: the old server waits for them, or the client doesn't come along.
type clientSnap struct {
	UID  string     // the old server's; also the name of its socket in the hand-over
	Pipe *gate.Info // behind a gate pipe (TLS, WebSocket, PROXY): what the gate said; nil: its own socket

	Registered            bool
	Nick, Ident, Realname string // before registration
	Host, IP              string
	TLS                   bool
	WebSocket             bool
	Port                  int
	WebIRC                string
	CertFP                string
	UnregCounted          bool
	CapBusy               bool
	HostDone              bool
	DNSBL                 *dnsblSnap
	TempShun, ShunLogged  bool
	Caps                  []string
	PreAway               string
	Multiline             *mlInSnap
	Marks                 map[string]time.Time

	SASLMech, SASLBuf string
	SASLAccount       string // logged in by SASL, not registered yet

	AuthHeld    bool
	AuthUntil   time.Time
	AuthReason  string
	AuthCmd     bool
	SoftReason  string
	SessionLate bool

	Monitor, Watch map[string]string
	Accept         []string             // UIDs
	CalleridAt     map[string]time.Time // by UID
	DCCAllow       []string             // UIDs
	DCCWarned      bool
	AmsgText       string
	AmsgTarget     string
	AmsgAt         time.Time
	NickLocked     bool
	IdentAnswer    string
	Silence        []string
	Knocks         []time.Time
	MdSubs         map[string]bool
	MdSets         []time.Time
	GreetWait      bool
	GreetHeld      []string

	Oper        bool // gets server notices for opers
	NoLag       bool
	OperFlags   string
	OperTitle   string
	OperClass   string
	OperSwhois  []string
	OperVhost   string
	OperAccount int64
	OperReach   string
	Snomask     string

	ConnectedAt, LastRecv, LastMsg time.Time
	PingSent                       time.Time
	Cookie                         string
	CookieOK                       bool

	FloodAhead time.Duration // how far the flood clock ran ahead
	// In: bytes the old server took from the connection and didn't
	// handle: lines held back, whole lines not looked at yet, the start
	// of a line. Out: bytes it meant to send and didn't.
	In  []byte
	Out []byte
}

type dnsblSnap struct {
	List  string
	Reply int
	Until time.Time
}

type mlInSnap struct {
	ID, Target, Verb string
	Tags             map[string]string
	Parts            []mlPartSnap
	Bytes            int
	Label            string
	Bad              string
	BadCtx           []string
}

type mlPartSnap struct {
	Text   string
	Concat bool
}

// encodeHandover writes a snapshot: one line of text, "BEEIRCD-HANDOVER
// <format version> <bytes that follow>", then the data as a gob. (Not
// JSON: topics, real names and away texts are bytes, not always UTF-8,
// and must come out as they went in.)
func encodeHandover(h *handoverSnap, version int) ([]byte, error) {
	var body bytes.Buffer
	if err := gob.NewEncoder(&body).Encode(h); err != nil {
		return nil, err
	}
	b := fmt.Appendf(nil, "%s %d %d\n", handoverMagic, version, body.Len())
	return append(b, body.Bytes()...), nil
}

// decodeHandover reads a snapshot. Another format version gives
// errHandoverVersion, before anything else is looked at.
func decodeHandover(b []byte) (*handoverSnap, error) {
	head, body, ok := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(head))
	if !ok || len(f) != 3 || f[0] != handoverMagic {
		return nil, errors.New("not a hand-over snapshot")
	}
	if v, err := strconv.Atoi(f[1]); err != nil || v != handoverVersion {
		return nil, fmt.Errorf("%w (%s; this server reads %d)", errHandoverVersion, f[1], handoverVersion)
	}
	if n, err := strconv.Atoi(f[2]); err != nil || n != len(body) {
		return nil, fmt.Errorf("hand-over snapshot: %d bytes arrived, the header says %s", len(body), f[2])
	}
	var h handoverSnap
	if err := gob.NewDecoder(bytes.NewReader(body)).Decode(&h); err != nil {
		return nil, fmt.Errorf("hand-over snapshot: %w", err)
	}
	if h.Net == nil {
		return nil, errors.New("hand-over snapshot: no network in it")
	}
	return &h, nil
}

// RequestHandover asks the gate to replace this server process with a new
// one (the program file as it is on disk now) while the clients stay
// connected. It is the entry point for `beeircd update` and RESTART; the
// gate's SIGUSR2 does the same. It only starts the hand-over: Serve
// returns ErrHandover when this server's part is done.
func (s *Server) RequestHandover() error {
	if s.gate == nil {
		return errors.New("a hand-over needs the gate in front of the server (beeircd run on Linux, without -no-gate)")
	}
	return s.gate.RequestHandover()
}

// floodSleep is the reader's flood pause; a hand-over ends it early.
func (s *Server) floodSleep(d time.Duration) {
	t := time.NewTimer(d)
	select {
	case <-t.C:
	case <-s.hand.freeze:
		t.Stop()
	}
}

// startHandover begins a hand-over (on the loop): it stops the readers
// and goes on once they have.
func (s *Server) startHandover() {
	if s.hand.started || s.closing() || s.stopAfter != 0 {
		return
	}
	s.hand.started = true
	s.log.Printf("hand-over: passing %d clients on to the next server process", len(s.clients))
	close(s.hand.freeze)
	clients := make([]*Client, 0, len(s.clients))
	now := time.Now()
	for _, c := range s.clients {
		c.frozen.Store(true)
		c.conn.SetReadDeadline(now) // wakes a reader waiting for bytes
		clients = append(clients, c)
	}
	go func() {
		late := time.After(handoverReadWait)
	wait:
		for _, c := range clients {
			select {
			case <-c.readDone:
			case <-late:
				break wait // whoever still reads doesn't come along
			}
		}
		s.post(func() { s.handoverSettle(time.Now().Add(handoverSettleWait)) })
	}()
}

// handoverBusy reports whether something is under way for c that another
// process can't finish: a password check, a lookup, held lines.
func (s *Server) handoverBusy(c *Client) bool {
	return s.passwordBusy(c) || len(c.heldLines) > 0 || c.saslBusy || c.dnsblBusy || c.identBusy || c.webircBusy || !c.hostDone
}

// handoverSettle waits (without holding the loop) until nothing is under
// way for any client, or until it is time to go on anyway.
func (s *Server) handoverSettle(until time.Time) {
	if s.stopAfter != 0 {
		return // a stop got in first; it says goodbye to everyone
	}
	if time.Now().Before(until) {
		for _, c := range s.clients {
			if s.handoverBusy(c) {
				go func() {
					time.Sleep(handoverSettleStep)
					s.post(func() { s.handoverSettle(until) })
				}()
				return
			}
		}
	}
	s.handoverNow()
}

// handoverNow takes the snapshot and gives everything away. After it this
// server knows no clients any more; Serve stops it.
func (s *Server) handoverNow() {
	const reason = "Server restarting"
	if s.stopAfter != 0 {
		return
	}
	// Links are not handed over: the other side sees a split, the next
	// server links again and both sides sync.
	var links []*linkConn
	for l := range s.links.conns {
		links = append(links, l)
	}
	for _, l := range links {
		s.dropLink(l, "Server upgrade")
	}
	s.svc.Flush()
	s.reap()

	// Who comes along: everyone whose reader stopped and who isn't in the
	// middle of something only this process can finish.
	var keep []*Client
	for _, c := range s.clients {
		stopped := false
		select {
		case <-c.readDone:
			stopped = c.inFrozen
		default:
		}
		switch {
		case !stopped:
			s.quit(c, reason)
		case !c.registered && (s.handoverBusy(c) || c.authRelay != ""):
			s.quit(c, reason) // logging in, with a lookup or check still running
		default:
			keep = append(keep, c)
		}
	}
	keep = s.stopWriters(keep, reason)
	s.svc.Flush()
	if s.hand.beforeSnap != nil {
		s.hand.beforeSnap()
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].uid < keep[j].uid })

	snap := &handoverSnap{
		Server: s.cfg.Server.Name, SID: s.cfg.Server.SID, Casemapping: irc.Casemapping(),
		From: version.String(), Taken: time.Now(),
		MaxLocal: s.maxLocal, MaxGlobal: s.maxGlobal, DayPeak: s.dayPeak, DayPeakAt: s.dayPeakAt,
	}
	for _, b := range s.timedBans {
		snap.TimedBans = append(snap.TimedBans, timedBanSnap{b.channel, b.mode, b.mask, b.until})
	}
	for _, f := range s.floodUnsets {
		snap.FloodUnsets = append(snap.FloodUnsets, floodUnsetSnap{f.channel, f.mode, f.until})
	}
	conns := make(map[string]net.Conn, len(keep))
	for _, c := range keep {
		snap.Clients = append(snap.Clients, s.snapClient(c))
		conns[c.uid] = c.conn
	}
	// The network and services last: snapClient may still answer a
	// client (a line that was too long).
	s.svc.Flush()
	snap.Net = s.net.Snapshot()
	snap.Services = s.svc.HandoverSave()
	if s.cfg.History.Secret == "" && s.hist.store != nil {
		snap.History = s.hist.store.Snapshot()
	}

	ver := handoverVersion
	if s.hand.version != 0 {
		ver = s.hand.version
	}
	give := s.hand.give
	if give == nil && s.gate != nil {
		give = s.gate.Give
	}
	b, err := encodeHandover(snap, ver)
	switch {
	case err != nil:
	case give == nil:
		err = errors.New("no gate to hand over to")
	default:
		err = give(b, conns)
	}
	if err != nil {
		// As at a restart: everyone is told and disconnected, and the
		// next server starts empty.
		s.log.Printf("hand-over failed: %v; disconnecting %d clients as at a restart", err, len(keep))
		handoverDropAll(keep, reason)
	} else {
		s.log.Printf("hand-over: %d clients and %d channels given to the next server process (%d KB)", len(keep), s.net.ChannelCount(), len(b)>>10)
		if !s.hand.keepConns {
			for _, c := range keep {
				c.conn.Close() // this process's copy; the gate holds the socket
			}
		}
	}
	// This server has no clients any more. Nothing is said to anyone and
	// no event runs: for the network these people never left.
	for _, c := range keep {
		c.closed = true
		c.heldLines, c.capture = nil, nil
	}
	clear(s.clients)
	clear(s.held)
	clear(s.opers)
	clear(s.watchers)
	clear(s.perIP)
	s.dirty, s.toKill, s.local = nil, nil, 0
	s.stopAfter = stopHandover
}

// stopWriters stops the writer of each client in cs, keeping what it
// didn't send, and returns the clients whose connection still works; the
// others quit here.
func (s *Server) stopWriters(cs []*Client, reason string) []*Client {
	for _, c := range cs {
		c.mu.Lock()
		c.handoff = true
		c.mu.Unlock()
	}
	late := time.Now().Add(handoverWriteWait)
	keep := cs[:0]
	for _, c := range cs {
		stopped := false
		for !stopped {
			// A writer in the middle of a write to a slow client is cut
			// short by the deadline; one that was about to write may set
			// its own deadline after ours, so ask again until it is out.
			c.conn.SetWriteDeadline(time.Now())
			select {
			case c.notify <- struct{}{}:
			default:
			}
			select {
			case <-c.writeDone:
				stopped = true
			case <-time.After(10 * time.Millisecond):
			}
			if !stopped && time.Now().After(late) {
				break
			}
		}
		c.mu.Lock()
		broken := c.writeErr
		c.mu.Unlock()
		if !stopped || broken {
			// Its writer hangs or its connection broke: it leaves the
			// usual way (the others' QUIT lines stay in their queues and
			// go out from the next server).
			c.mu.Lock()
			c.handoff = false
			c.mu.Unlock()
			s.quit(c, reason)
			continue
		}
		keep = append(keep, c)
	}
	return keep
}

// handoverDrop tells a client that can't come along and closes its
// connection. It writes to the connection itself: the client's writer is
// gone.
// until is the write deadline: callers dropping many clients give them all
// one, so stalled clients hold the loop handoverGoodbye in all, not each.
func handoverDrop(conn net.Conn, host, reason string, until time.Time) {
	conn.SetWriteDeadline(until)
	conn.Write([]byte("ERROR :Closing Link: " + host + " (" + reason + ")" + crlf))
	conn.Close()
}

// handoverDropAll drops cs with one shared write deadline.
func handoverDropAll(cs []*Client, reason string) {
	until := time.Now().Add(handoverGoodbye)
	for _, c := range cs {
		handoverDrop(c.conn, c.host, reason, until)
	}
}

// snapClient copies c for the next server process. c's reader and writer
// have stopped.
func (s *Server) snapClient(c *Client) clientSnap {
	cs := clientSnap{
		UID: c.uid, Registered: c.registered, Nick: c.nick, Ident: c.ident, Realname: c.realname,
		Host: c.host, IP: c.ip, TLS: c.tls, WebSocket: c.websocket, Port: c.port, WebIRC: c.webirc, CertFP: c.certfp,
		UnregCounted: c.unregCounted, CapBusy: c.capBusy, HostDone: c.hostDone,
		TempShun: c.tempShun, ShunLogged: c.shunLogged, PreAway: c.preAway, Marks: c.marks,
		SASLMech: c.saslMech, SASLBuf: c.saslBuf.String(),
		AuthHeld: c.authHeld, AuthUntil: c.authUntil, AuthReason: c.authReason, AuthCmd: c.authCmd,
		SoftReason: c.softReason, SessionLate: c.sessionLate,
		Monitor: c.monitor, Watch: c.watch, CalleridAt: c.calleridAt, DCCWarned: c.dccWarned,
		AmsgText: c.amsgText, AmsgTarget: c.amsgTarget, AmsgAt: c.amsgAt, NickLocked: c.nickLocked,
		IdentAnswer: c.identAnswer, Silence: c.silence, Knocks: c.knocks, MdSubs: c.mdSubs, MdSets: c.mdSets, GreetWait: c.greetWait, GreetHeld: c.greetHeld,
		Oper: s.opers[c], NoLag: c.noLag.Load(), OperFlags: c.operFlags, OperTitle: c.operTitle, OperClass: c.operClass,
		OperSwhois: c.operSwhois, OperVhost: c.operVhost, OperAccount: c.operAccount, OperReach: c.operReach, Snomask: c.snomask,
		ConnectedAt: c.connectedAt, LastRecv: c.lastRecv, LastMsg: c.lastMsg, PingSent: c.pingSent,
		Cookie: c.cookie, CookieOK: c.cookieOK, FloodAhead: c.floodAhead,
	}
	if gc, ok := c.conn.(*gate.Conn); ok {
		info := gc.Info
		cs.Pipe = &info
	}
	if c.registered {
		cs.Nick, cs.Ident, cs.Realname = "", "", "" // the user in the network has them
	}
	for name, on := range c.caps {
		if on {
			cs.Caps = append(cs.Caps, name)
		}
	}
	sort.Strings(cs.Caps)
	for uid, on := range c.accept {
		if on {
			cs.Accept = append(cs.Accept, uid)
		}
	}
	sort.Strings(cs.Accept)
	for uid, on := range c.dccAllow {
		if on {
			cs.DCCAllow = append(cs.DCCAllow, uid)
		}
	}
	sort.Strings(cs.DCCAllow)
	if a := c.saslAccount; a != nil {
		cs.SASLAccount = a.Name
	}
	if r := c.dnsblListed; r != nil {
		cs.DNSBL = &dnsblSnap{r.list, r.reply, r.until}
	}
	if m := c.mlIn; m != nil {
		ml := &mlInSnap{ID: m.id, Target: m.target, Verb: m.verb, Tags: m.tags, Bytes: m.bytes, Label: m.label, Bad: m.bad, BadCtx: m.badCtx}
		for _, p := range m.parts {
			ml.Parts = append(ml.Parts, mlPartSnap{p.text, p.concat})
		}
		cs.Multiline = ml
	}
	// Input, in the order it came: lines held back while a password was
	// checked (that check's answer is lost; the lines run again), then
	// what the reader had taken from the connection.
	for _, l := range c.heldLines {
		cs.In = append(append(cs.In, l...), crlf...)
	}
	rest := c.inRest
	if c.inSkip {
		// The reader was dropping a line that is too long.
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:] // its end is here: answer now
			s.reply(c, irc.ErrInputTooLong, "Input line was too long")
		} else {
			// It goes on: the next reader finds it too long as well.
			rest = bytes.Repeat([]byte{'x'}, maxInput+1)
		}
	}
	cs.In = append(cs.In, rest...)
	c.mu.Lock()
	cs.Out = append(cs.Out, c.outRest...)
	for _, l := range c.queue {
		cs.Out = append(cs.Out, l...)
	}
	c.mu.Unlock()
	return cs
}

// finishHandover stops the server after handoverNow: like shutdown, but
// nobody is told anything and the gate keeps the client ports.
func (s *Server) finishHandover() {
	s.log.Printf("stopping: handed over")
	snap := s.historySnapshot()
	close(s.done)
	s.lmu.Lock()
	for _, l := range s.listeners {
		l.Close()
	}
	s.lmu.Unlock()
	if s.gate != nil {
		s.gate.Close() // no Sync first: the ports stay open for the next server
	}
	s.closeLinks("Server upgrade")
	s.closeControl()
	s.closeRPC()
	s.finishStop(snap)
}

// resume is what a starting server takes over from the last one: the
// gate's hand-over, or a test's stand-in.
type resume struct {
	snapshot []byte
	err      error                                              // it didn't arrive whole
	has      func(id string) bool                               // that client's connection came along
	conn     func(id string, info *gate.Info) (net.Conn, error) // takes it
	close    func()                                             // disconnects whoever wasn't taken
	done     func(kept int, problem string)                     // reports how it went
}

func resumeFromGate(child *gate.Child, h *gate.Handover) *resume {
	return &resume{snapshot: h.Snapshot, err: h.Err, has: h.Has, conn: h.Conn, close: h.Close,
		done: func(kept int, problem string) { child.Resumed(kept, problem) }}
}

// takeOver loads the last server's hand-over into this freshly started
// one, before its loop runs. A hand-over it can't use is dropped (those
// clients are disconnected) and the server starts empty: nil. An error
// (ErrHandoverBroken) means it broke half-way and this server is in no
// state to run.
func (s *Server) takeOver(r *resume) (err error) {
	start := time.Now()
	empty := func(why string) {
		s.log.Printf("hand-over: %s; starting without the last server's clients", why)
		r.close()
		s.hand.took = &control.Handover{Problem: why}
		r.done(0, why)
	}
	if r.err != nil {
		empty(r.err.Error())
		return nil
	}
	snap, derr := decodeHandover(r.snapshot)
	if derr != nil {
		empty(derr.Error())
		return nil
	}
	if snap.SID != s.cfg.Server.SID || !strings.EqualFold(snap.Server, s.cfg.Server.Name) || snap.Casemapping != irc.Casemapping() {
		empty(fmt.Sprintf("the snapshot is from a server set up differently (%s, SID %s, casemapping %s)", snap.Server, snap.SID, snap.Casemapping))
		return nil
	}
	var taken []*Client
	defer func() {
		if p := recover(); p != nil {
			s.log.Printf("PANIC while taking over the last server's clients: %v\n%s", p, debug.Stack())
			for _, c := range taken {
				c.conn.Close()
			}
			r.close()
			s.hand.took = &control.Handover{Problem: "internal error"}
			r.done(0, "internal error")
			err = fmt.Errorf("%w: %v", ErrHandoverBroken, p)
		}
	}()

	// UIDs. Our own service bots are on the network already, maybe under
	// other UIDs than in the last process (a bot added while it ran, a
	// new service in this version): the snapshot's bots become ours, by
	// nick, and a person whose UID one of our bots has now gets a new
	// one. Users without a connection here (another server's, lost in a
	// split that wasn't finished) are left out.
	records := make(map[string]*clientSnap, len(snap.Clients))
	for i := range snap.Clients {
		records[snap.Clients[i].UID] = &snap.Clients[i]
	}
	s.net.Reserve(snap.Net.Next)
	to := make(map[string]string)
	for _, u := range snap.Net.Users {
		rec := records[u.UID]
		switch {
		case u.Service:
			if ours := s.net.UserByNick(u.Nick); ours != nil && ours.Service {
				to[u.UID] = ours.UID
			} else {
				to[u.UID] = ""
			}
		case rec == nil || !rec.Registered || !r.has(u.UID):
			to[u.UID] = ""
		case s.net.UserByUID(u.UID) != nil:
			to[u.UID] = s.net.NextUID()
		}
	}
	uidNow := func(old string) string {
		if id, ok := to[old]; ok {
			return id
		}
		return old
	}
	snap.Net.Remap(to)
	s.net.Load(snap.Net)

	var lost []string    // users whose connection couldn't be taken after all
	people, kept := 0, 0 // registered clients: in the snapshot, taken over
	for i := range snap.Clients {
		cs := &snap.Clients[i]
		if cs.Registered {
			people++
		}
		var u *state.User
		uid := ""
		if cs.Registered {
			if uid = uidNow(cs.UID); uid != "" {
				u = s.net.UserByUID(uid)
			}
			if u == nil || u.Service || s.clients[uid] != nil {
				// Not on the network: one of our bots has the nick now.
				if conn, err := r.conn(cs.UID, cs.Pipe); err == nil {
					handoverDrop(conn, cs.Host, "Server restarting", time.Now().Add(handoverGoodbye))
				}
				continue
			}
		} else {
			uid = s.net.NextUID() // nobody else knows an unregistered client's UID
		}
		conn, err := r.conn(cs.UID, cs.Pipe)
		if err != nil {
			s.log.Printf("hand-over: %s (%s): %v", cs.UID, cs.IP, err)
			if u != nil {
				lost = append(lost, uid)
			}
			continue
		}
		taken = append(taken, s.restoreClient(cs, uid, u, conn, uidNow))
		if cs.Registered {
			kept++
		}
	}
	r.close() // sockets nobody asked for

	s.maxLocal, s.maxGlobal = max(s.maxLocal, snap.MaxLocal), max(s.maxGlobal, snap.MaxGlobal)
	if snap.DayPeakAt.After(s.dayPeakAt) {
		s.dayPeak, s.dayPeakAt = snap.DayPeak, snap.DayPeakAt
	}
	for _, b := range snap.TimedBans {
		s.timedBans = append(s.timedBans, timedBan{b.Channel, b.Mode, b.Mask, b.Until})
	}
	for _, f := range snap.FloodUnsets {
		s.floodUnsets = append(s.floodUnsets, floodUnset{f.Channel, f.Mode, f.Until})
	}
	if len(snap.History) > 0 && s.cfg.History.Secret == "" && s.hist.store != nil {
		s.hist.store.Restore(snap.History, time.Now())
	}
	s.svc.HandoverLoad(snap.Services, uidNow)
	for _, uid := range lost {
		s.emit(&state.UserQuit{UID: uid, Reason: "Connection closed"})
	}
	s.svc.Flush()
	// Start reading and writing only now that everything is in place.
	for _, c := range taken {
		s.wg.Add(1)
		go c.writeLoop()
		go c.readLoop()
	}
	s.hand.took = &control.Handover{Kept: kept, Of: people}
	r.done(len(taken), "")
	s.log.Printf("hand-over: took over %d clients and %d channels from %s (%d ms after its snapshot, %d ms to load)",
		len(taken), s.net.ChannelCount(), snap.From, time.Since(snap.Taken).Milliseconds(), time.Since(start).Milliseconds())
	return nil
}

// restoreClient makes the Client for one connection of the hand-over. u is
// its user on the network, nil before registration. Its reader and writer
// aren't started yet.
func (s *Server) restoreClient(cs *clientSnap, uid string, u *state.User, conn net.Conn, uidNow func(string) string) *Client {
	conn.SetDeadline(time.Time{}) // the same connection object, in tests: the old server's deadlines are still on it
	c := &Client{
		s: s, conn: conn, uid: uid, user: u, registered: cs.Registered,
		nick: cs.Nick, ident: cs.Ident, realname: cs.Realname, host: cs.Host, ip: cs.IP,
		tls: cs.TLS, websocket: cs.WebSocket, port: cs.Port, webirc: cs.WebIRC, certfp: cs.CertFP,
		capBusy: cs.CapBusy, hostDone: cs.HostDone, tempShun: cs.TempShun, shunLogged: cs.ShunLogged,
		caps: make(map[string]bool, len(cs.Caps)), preAway: cs.PreAway, marks: cs.Marks,
		saslMech: cs.SASLMech,
		authHeld: cs.AuthHeld, authUntil: cs.AuthUntil, authReason: cs.AuthReason, authCmd: cs.AuthCmd,
		softReason: cs.SoftReason, sessionLate: cs.SessionLate,
		monitor: cs.Monitor, watch: cs.Watch, dccWarned: cs.DCCWarned,
		amsgText: cs.AmsgText, amsgTarget: cs.AmsgTarget, amsgAt: cs.AmsgAt, nickLocked: cs.NickLocked,
		identAnswer: cs.IdentAnswer, silence: cs.Silence, knocks: cs.Knocks, mdSubs: cs.MdSubs, mdSets: cs.MdSets, greetWait: cs.GreetWait, greetHeld: cs.GreetHeld,
		operFlags: cs.OperFlags, operTitle: cs.OperTitle, operClass: cs.OperClass, operSwhois: cs.OperSwhois,
		operVhost: cs.OperVhost, operAccount: cs.OperAccount, operReach: cs.OperReach, snomask: cs.Snomask,
		floodBurst: s.cfg.Limits.FloodBurst, floodRate: s.cfg.Limits.FloodRate,
		connectedAt: cs.ConnectedAt, lastRecv: cs.LastRecv, lastMsg: cs.LastMsg, pingSent: cs.PingSent,
		cookie: cs.Cookie, cookieOK: cs.CookieOK,
		notify: make(chan struct{}, 1), readDone: make(chan struct{}), writeDone: make(chan struct{}),
		in0: cs.In, ahead0: cs.FloodAhead,
	}
	for _, name := range cs.Caps {
		c.caps[name] = true
	}
	c.saslBuf.WriteString(cs.SASLBuf)
	c.noLag.Store(cs.NoLag)
	if cs.SASLAccount != "" {
		if a, err := s.store.AccountByNick(cs.SASLAccount); err == nil && a != nil {
			c.saslAccount = a
		}
	}
	if d := cs.DNSBL; d != nil {
		c.dnsblListed = &dnsblResult{d.List, d.Reply, d.Until}
	}
	if m := cs.Multiline; m != nil {
		c.mlIn = &mlIn{id: m.ID, target: m.Target, verb: m.Verb, tags: m.Tags, bytes: m.Bytes, label: m.Label, bad: m.Bad, badCtx: m.BadCtx}
		for _, p := range m.Parts {
			c.mlIn.parts = append(c.mlIn.parts, mlPart{p.Text, p.Concat})
		}
	}
	// Lists of other people, by UID.
	for _, old := range cs.Accept {
		if id := uidNow(old); id != "" {
			if c.accept == nil {
				c.accept = make(map[string]bool)
			}
			c.accept[id] = true
		}
	}
	for _, old := range cs.DCCAllow {
		if id := uidNow(old); id != "" {
			if c.dccAllow == nil {
				c.dccAllow = make(map[string]bool)
			}
			c.dccAllow[id] = true
		}
	}
	for old, at := range cs.CalleridAt {
		if id := uidNow(old); id != "" {
			if c.calleridAt == nil {
				c.calleridAt = make(map[string]time.Time)
			}
			c.calleridAt[id] = at
		}
	}
	// The server's own books, as newClient, tryRegister and the commands
	// keep them.
	s.clients[uid] = c
	s.perIP[c.ip]++
	switch {
	case c.registered:
		s.local++
	case cs.UnregCounted:
		c.unregCounted = true
		s.throttle.unreg[c.ip]++
		s.throttle.unregAll++
	}
	if cs.Oper {
		s.opers[c] = true
	}
	for key := range c.monitor {
		s.indexWatch(c, key)
	}
	for key := range c.watch {
		s.indexWatch(c, key)
	}
	if len(cs.Out) > 0 {
		c.queue, c.queued = []string{string(cs.Out)}, len(cs.Out)
		c.dirty = true
		s.dirty = append(s.dirty, c)
	}
	return c
}
