// Package server is the IRC server: it accepts connections, runs every
// command and keeps the network state.
//
// One goroutine (the loop) owns all state. Each connection has a reader
// goroutine that hands lines to the loop as closures, and a writer goroutine
// that drains the connection's send queue. Nothing but the loop touches
// state, so there are no locks around it.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// Server is one running BeeIRCd.
type Server struct {
	cfg       *config.Config
	log       *log.Logger
	net       *state.Network
	clients   map[string]*Client // by UID, registered or not
	events    chan func()
	done      chan struct{}
	listeners []listener
	created   time.Time
	whowas    []whowasEntry
	maxLocal  int
	maxGlobal int // most people on the network at once (LUSERS)
	// Hive Panel Spotlight (spotlight3s.go): when each person last talked
	// in each channel (times only), and today's most people at once.
	talk       map[*state.Channel]map[string]int64
	talkRoom   map[*state.Channel]*roomTalk // lines per minute and order of the last line
	talkSeq    uint64
	talkSwept  time.Time
	dayPeak    int
	dayPeakAt  time.Time
	toKill     []*Client
	local      int              // registered clients here
	perIP      map[string]int   // connections per IP (session limit)
	opers      map[*Client]bool // opered clients here (server notices)
	dirty      []*Client        // clients with output to wake their writer for
	sinceFlush int              // loop jobs since the last flushWrites
	msgSeq     uint64
	batchSeq   uint64
	sinks      []func(state.Event) // get every event after it was applied
	beforeEmit func(state.Event)   // tests: runs before each event
	timedBans  []timedBan          // ~t: channel list entries waiting to lift
	// 1.5.9a channel modes: flood guard counters (by folded channel),
	// parsed +f values, flood modes waiting to lift, +J rejoin blocks.
	floods      map[string]*chanFloodState
	floodCache  map[string]floodSet
	floodUnsets []floodUnset
	floodSwept  time.Time
	floodBoot   time.Duration // join floods aren't counted this long after start
	rejoinBlock map[string]time.Time
	// held are the clients with lines waiting for a password check (O1).
	held map[*Client]struct{}
	hist histState // 1.5.9c channel history (+H, CHATHISTORY)
	// 7a connect throttle and unregistered caps; throttleSkip says which
	// IPs skip them (LAN), replaceable in tests.
	throttle     connThrottle
	throttleSkip func(ip string) bool
	noticeAt     map[string]time.Time // snoticeEvery
	// 7b: control socket, reloadable certificate, today's safety counts.
	ctl         net.Listener
	certs       *tlsutil.Reloader
	certChecked time.Time
	certErr     string
	safe        safetyState
	acme        acmeState                   // 7c Let's Encrypt renewal
	upd         updState                    // 7e daily release check
	quitReason  string                      // set by `beeircd update` before it restarts the server
	watchers    map[string]map[*Client]bool // MONITOR: folded nick -> clients watching it
	loop        loopStats                   // how long each piece of loop work took
	links       linkState                   // Stage 2 server links
	netjoin     *netjoinBatch               // set while a link's burst is handled
	netsplit    *netjoinBatch               // set while the users of a split server quit (2e)
	origin      *linkOrigin                 // where the next emitted event came from (2c)
	banFrom     *linkConn                   // the link a ban change came from, while it is applied
	wg          sync.WaitGroup

	// Services. The store is shared: the server reads oper entries and
	// SASL accounts from it, services own the rest.
	store     *store.Store
	svc       *services.Services
	adminServ *state.User // AdminServ is the server's own bot

	tlsConf    *tls.Config
	lmu        sync.Mutex // guards listeners against Addrs from other goroutines
	stopAfter  int        // stopRestart or stopDie once AdminServ asked
	stopReason string

	// resolve looks up a hostname for an IP; replaceable in tests.
	resolve func(ctx context.Context, ip string) (string, bool)
	// lookupA answers DNSBL queries; replaceable in tests.
	lookupA func(ctx context.Context, name string) ([]string, error)
	// dnsblSkip says which IPs are never looked up; replaceable in tests.
	dnsblSkip func(ip string) bool
	dnsbl     dnsblState

	// panelGone keeps the password hash of oper entries the Hive Panel
	// removed, so its Undo brings the password back too (3d).
	panelGone map[int64]string
}

type listener struct {
	net.Listener // plain TCP; TLS is added per connection
	tls          bool
	addr         string // as written in the config
	proxy        bool   // PROXY protocol header first (7a)
	proxyFrom    []*net.IPNet
	tlsConf      *tls.Config // captured: accept goroutines must not read Server.tlsConf
}

const (
	stopRestart = 1
	stopDie     = 2
)

// ErrRestart is returned by Serve when an admin asked for a restart.
var ErrRestart = errors.New("restart requested")

// New opens the configured ports. Call Serve to start answering.
func New(cfg *config.Config, logger *log.Logger) (*Server, error) {
	s := &Server{
		cfg:          cfg,
		log:          logger,
		net:          state.NewNetwork(cfg.Server.SID),
		clients:      make(map[string]*Client),
		watchers:     make(map[string]map[*Client]bool),
		perIP:        make(map[string]int),
		opers:        make(map[*Client]bool),
		events:       make(chan func(), 1024),
		done:         make(chan struct{}),
		created:      time.Now(),
		floods:       make(map[string]*chanFloodState),
		floodCache:   make(map[string]floodSet),
		floodBoot:    floodBootDelay,
		rejoinBlock:  make(map[string]time.Time),
		held:         make(map[*Client]struct{}),
		resolve:      lookupHost,
		lookupA:      lookupA,
		dnsblSkip:    dnsblSkipped,
		throttle:     newConnThrottle(),
		throttleSkip: dnsblSkipped,
		noticeAt:     make(map[string]time.Time),
		dnsbl:        dnsblState{cache: make(map[string]dnsblResult), hits: make(map[string]int), off: make(map[string]bool)},
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Services.Database), 0o750); err != nil {
		return nil, err
	}
	irc.SetCasemapping(cfg.Server.Casemapping) // before any name is stored
	s.setupHistory()
	db, err := store.Open(cfg.Services.Database)
	if err != nil {
		return nil, err
	}
	s.store = db
	for _, n := range db.Notes() {
		logger.Printf("services database: %s", n)
	}
	svc, err := services.New(host{s}, cfg, logger, s.net, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("services database: %w", err)
	}
	s.svc = svc
	svc.OnSecretDone(s.releaseHeld)
	svc.OnBanChange(s.linkBanChanged)
	svc.OnOperData(s.syncQlines)
	s.sinks = append(s.sinks, svc.Event)
	svc.Start()
	s.adminServ = svc.AddBot("AdminServ", "as", "Admin Services",
		"AdminServ changes the server while it runs; changes are saved to beeircd.conf. Commands:", 'a', s.adminServCommands())
	svc.AddCommands("OperServ", s.operServExtras())
	svc.SyncLogChannel("")
	if cfg.Services.Admin != "" && len(cfg.Services.AdminHosts) == 0 {
		logger.Printf("warning: [services] admin_hosts is empty, so the admin %s may /oper from any host (with the oper password)", cfg.Services.Admin)
		if cfg.Services.AdminPassword == "" {
			logger.Printf("warning: [services] admin_password is empty too: %s can't /oper until it is set (beeircd mkpasswd, then rehash)", cfg.Services.Admin)
		}
	}
	svc.Flush()
	var tlsConf *tls.Config
	if cfg.NeedsTLS() {
		var err error
		// With Let's Encrypt a stand-in self-signed pair lets the TLS
		// ports open at once; the real one replaces it in the background.
		certs, err := tlsutil.NewReloader(cfg.TLS.Cert, cfg.TLS.Key, cfg.TLS.SelfSigned || cfg.TLS.ACME, cfg.TLS.Domains(cfg.Server.Name)[0])
		if err != nil {
			db.Close()
			return nil, err
		}
		s.certs, s.certChecked = certs, time.Now()
		tlsConf = certs.Config()
		s.tlsConf = tlsConf
	}
	for _, l := range cfg.Listen {
		ln, err := net.Listen("tcp", l.Address)
		if err != nil {
			s.closeListeners()
			db.Close()
			return nil, fmt.Errorf("listen on %s: %w", l.Address, err)
		}
		li := listener{Listener: ln, tls: l.TLS, addr: l.Address, proxy: l.Proxy, tlsConf: tlsConf}
		for _, m := range l.ProxyFrom {
			n, _ := config.ParseCIDR(m) // checked by config.Check
			li.proxyFrom = append(li.proxyFrom, n)
		}
		s.listeners = append(s.listeners, li)
	}
	if err := s.linkSetup(); err != nil {
		s.closeListeners()
		db.Close()
		return nil, err
	}
	// Without the socket the server still runs; only the admin commands
	// can't reach it.
	if err := s.openControl(); err != nil {
		logger.Printf("warning: %v; beeircd status, backup and rehash won't reach this server", err)
	}
	return s, nil
}

// Addrs returns the address of each open port, in config order.
func (s *Server) Addrs() []net.Addr {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	out := make([]net.Addr, len(s.listeners))
	for i, l := range s.listeners {
		out[i] = l.Addr()
	}
	return out
}

// Serve runs until ctx is cancelled, then closes every connection.
func (s *Server) Serve(ctx context.Context) error {
	s.lmu.Lock()
	for _, l := range s.listeners {
		s.wg.Add(1)
		go s.acceptLoop(l)
	}
	s.lmu.Unlock()
	if s.links.ln != nil {
		s.wg.Add(1)
		go s.linkAcceptLoop(s.links.ln)
	}
	if s.ctl != nil {
		s.wg.Add(1)
		go s.controlLoop()
	}
	if s.cfg.TLS.ACME && s.certs != nil {
		s.wg.Add(1)
		go s.acmeLoop(s.acmeConfig())
	}
	s.startUpdateCheck()
	s.startPlaceLookup()
	s.log.Printf("%s ready on %d port(s)", s.cfg.Server.Name, len(s.listeners))
	tick := time.NewTicker(s.tickEvery())
	defer tick.Stop()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	// Links get their own 1 s tick: autoconnect and pings shouldn't wait
	// for the client tick, which can be 15 s.
	var linkTick <-chan time.Time
	if s.links.ln != nil {
		lt := time.NewTicker(time.Second)
		defer lt.Stop()
		linkTick = lt.C
		s.linkTick(time.Now())
	}
	s.svc.Expire(time.Now())
	for {
		select {
		case <-ctx.Done():
			reason := "Server shutting down"
			if s.quitReason != "" {
				reason = s.quitReason
			}
			s.shutdown(reason)
			return nil
		case f := <-s.events:
			start := time.Now()
			f()
			s.svc.Flush()
			s.loop.add(time.Since(start))
			s.reap()
			// Wake writers when the batch is done, or every 64 jobs so a
			// busy server never holds output back for long.
			if s.sinceFlush++; s.sinceFlush >= 64 {
				s.flushWrites()
			} else if len(s.events) == 0 {
				// Let readers with lines waiting hand them in first, so
				// more work lands in this batch (InspIRCd handles every
				// ready socket in one pass).
				runtime.Gosched()
				if len(s.events) == 0 {
					s.flushWrites()
				}
			}
			if s.stopAfter != 0 {
				reason := "Server shutting down"
				if s.stopAfter == stopRestart {
					reason = "Server restarting"
				}
				if s.stopReason != "" {
					reason += " (" + s.stopReason + ")"
				}
				s.shutdown(reason)
				if s.stopAfter == stopRestart {
					return ErrRestart
				}
				return nil
			}
		case now := <-tick.C:
			s.checkTimeouts(now)
			s.expireTimedBans(now)
			s.floodTick(now)
			s.historyTick(now)
			s.throttleTick(now)
			s.certTick(now)
			s.svc.Tick(now)
			s.reap()
			s.flushWrites()
		case now := <-linkTick:
			s.notePeakGlobal() // people coming in over links
			s.linkTick(now)
			s.reap()
			s.flushWrites()
		case now := <-hourly.C:
			s.svc.Expire(now)
			s.reap()
			s.flushWrites()
		}
	}
}

// flushWrites wakes the writer of every client that has new output.
func (s *Server) flushWrites() {
	for _, c := range s.dirty {
		c.dirty = false
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
	clear(s.dirty)
	s.dirty = s.dirty[:0]
	s.sinceFlush = 0
}

// tickEvery checks timeouts often enough for the shortest one.
func (s *Server) tickEvery() time.Duration {
	l := s.cfg.Limits
	d := min(l.PingInterval.Duration, l.PingTimeout.Duration, l.RegisterTimeout.Duration, s.cfg.Services.IdentifyWithin.Duration, s.cfg.Services.KillQuick.Duration)
	d /= 4
	switch {
	case d < 20*time.Millisecond:
		d = 20 * time.Millisecond
	case d > 15*time.Second:
		d = 15 * time.Second
	}
	return d
}

func (s *Server) acceptLoop(l listener) {
	defer s.wg.Done()
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Printf("accept: %v", err)
			select {
			case <-time.After(100 * time.Millisecond):
				continue
			case <-s.done:
				return
			}
		}
		// Nagle on, like InspIRCd and UnrealIRCd: the kernel merges the many
		// small lines a busy channel sends into fewer packets. Go turns it
		// off by default, which costs a packet (and a trip through the
		// firewall) per line.
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetNoDelay(false)
		}
		if l.proxy {
			s.wg.Add(1)
			go s.acceptProxied(conn, l)
			continue
		}
		if l.tls {
			conn = tls.Server(conn, l.tlsConf)
		}
		select {
		case s.events <- func() { s.newClient(conn, l.tls) }:
		case <-s.done:
			conn.Close()
			return
		}
	}
}

// post hands f to the loop; it gives up when the server is stopping.
func (s *Server) post(f func()) {
	select {
	case s.events <- f:
	case <-s.done:
	}
}

func (s *Server) closeListeners() {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	for _, l := range s.listeners {
		l.Close()
	}
}

func (s *Server) shutdown(reason string) {
	s.log.Printf("stopping: %s", reason)
	snap := s.historySnapshot() // before everyone quits and channels go
	close(s.done)
	s.closeListeners()
	s.closeLinks(reason)
	s.closeControl()
	for _, c := range s.clients {
		s.quit(c, reason)
	}
	finished := make(chan struct{})
	go func() { s.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		s.log.Printf("some connections did not close in time")
	}
	s.saveHistorySnapshot(snap)
	s.svc.SaveSeen()
	if err := s.store.Close(); err != nil {
		s.log.Printf("closing services database: %v", err)
	}
	s.log.Printf("%s stopped", s.cfg.Server.Name)
}

// reap disconnects clients whose send queue overflowed.
func (s *Server) reap() {
	s.reapLinks()
	for len(s.toKill) > 0 {
		kill := s.toKill
		s.toKill = nil
		for _, c := range kill {
			s.quit(c, "SendQ exceeded")
		}
	}
}

func (s *Server) checkTimeouts(now time.Time) {
	lim := s.cfg.Limits
	for _, c := range s.clients {
		if c.closed {
			continue
		}
		if !c.registered {
			if now.Sub(c.connectedAt) > lim.RegisterTimeout.Duration {
				s.quit(c, "Registration timed out")
			}
			continue
		}
		if c.pingSent.IsZero() {
			if now.Sub(c.lastRecv) > lim.PingInterval.Duration {
				c.pingSent = now
				c.send("PING :" + s.cfg.Server.Name)
			}
		} else if now.Sub(c.pingSent) > lim.PingTimeout.Duration {
			s.quit(c, fmt.Sprintf("Ping timeout: %d seconds", int(now.Sub(c.lastRecv).Seconds())))
		}
	}
}

// lookupHost does a reverse lookup and keeps the name only when it points
// back at the same IP.
func lookupHost(ctx context.Context, ip string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return "", false
	}
	name := trimDot(names[0])
	if !validHostname(name) {
		return "", false
	}
	addrs, err := net.DefaultResolver.LookupHost(ctx, name)
	if err != nil {
		return "", false
	}
	for _, a := range addrs {
		if a == ip {
			return name, true
		}
	}
	return "", false
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}

func validHostname(h string) bool {
	if h == "" || len(h) > 63 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
