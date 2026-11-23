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
	"github.com/fhazal/beeircd/internal/gate"
	"github.com/fhazal/beeircd/internal/history"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// Server is one running BeeIRCd.
type Server struct {
	cfg         *config.Config
	mailOK      mailTested // the last test mail that went through (Hive Panel)
	log         *log.Logger
	net         *state.Network
	clients     map[string]*Client // by UID, registered or not
	events      chan func()
	done        chan struct{}
	listeners   []listener
	created     time.Time
	whowas      []whowasEntry
	whowasDirty bool // entries not saved yet (whowasdb.go)
	whowasSaved time.Time
	rpc         rpcState                 // the JSON-RPC admin API (rpc.go)
	jump        *jumpServer              // JUMPSERVER: where people are sent, nil = off
	targetFlood map[string]*[3]targetWin // [targetfloodprot]: messages to each channel and person
	spamRep     spamReports              // [[spamreport]]: recent reports (spamreport.go)
	targetSwept time.Time
	identPort   int // where ident servers listen (113; tests change it)
	maxLocal    int
	maxGlobal   int // most people on the network at once (LUSERS)
	// Hive Panel Spotlight (spotlight.go): when each person last talked
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
	// Channel modes: flood guard counters (by folded channel),
	// parsed +f values, flood modes waiting to lift, +J rejoin blocks.
	floods      map[string]*chanFloodState
	floodCache  map[string]floodSet
	floodUnsets []floodUnset
	floodSwept  time.Time
	floodBoot   time.Duration // join floods aren't counted this long after start
	rejoinBlock map[string]time.Time
	// held are the clients with lines waiting for a password check (O1).
	held map[*Client]struct{}
	hist histState // channel history (+H, CHATHISTORY)
	// marks are the read markers of accounts (draft/read-marker).
	marks map[markKey]time.Time
	ml    *mlOut // the multiline message being delivered
	// recent are the last minutes' messages, for REDACT.
	recent recentMsgs
	// renamedAt is when each channel (folded) last got its name, for the
	// RENAME cooldown.
	renamedAt map[string]time.Time
	// roBy are the servers behind links whose services are read-only.
	roBy map[string]bool
	// 7a connect throttle and unregistered caps; throttleSkip says which
	// IPs skip them (LAN), replaceable in tests.
	throttle     connThrottle
	throttleSkip func(ip string) bool
	noticeAt     map[string]time.Time // snoticeEvery
	// Control socket, reloadable certificate, today's safety counts.
	ctl         net.Listener
	certs       *tlsutil.Reloader
	certChecked time.Time
	certErr     string
	safe        safetyState
	acme        acmeState                   // 7c Let's Encrypt renewal
	upd         updState                    // 7e daily release check
	geo         geoState                    // country and ASN lookups
	quitReason  string                      // set by `beeircd update` before it restarts the server
	chatKeys    map[string]chatKey          // one-time SASL keys for the Hive Panel's Chat tab (chatkey.go)
	watchers    map[string]map[*Client]bool // MONITOR: folded nick -> clients watching it
	loop        loopStats                   // how long each piece of loop work took
	links       linkState                   // server links
	netjoin     *netjoinBatch               // set while a link's burst is handled
	netsplit    *netjoinBatch               // set while the users of a split server quit
	origin      *linkOrigin                 // where the next emitted event came from
	banFrom     *linkConn                   // the link a ban change came from, while it is applied
	wg          sync.WaitGroup
	hand        handState // passing the clients on to the next server process (handover.go)

	// Services. The store is shared: the server reads oper entries and
	// SASL accounts from it, services own the rest.
	store     *store.Store
	svc       *services.Services
	adminServ *state.User // AdminServ is the server's own bot

	tlsConf    *tls.Config
	lmu        sync.Mutex // guards listeners against Addrs from other goroutines
	stopAfter  int        // stopRestart or stopDie once AdminServ asked
	stopReason string

	// acc runs the client ports' accept loops in one process. Behind the
	// gate (internal/gate) the gate holds the client ports instead; gate
	// is nil when this process holds them itself.
	acc       *acceptor
	gate      *gate.Child
	certFiles *gate.TLSFiles // the certificate the gate loads for TLS ports
	certGen   int            // bumped after each certificate reload, for the gate
	gateDeny  gateDenyState  // the IPs the gate refuses before TLS (gatedeny.go)

	// resolve looks up a hostname for an IP; replaceable in tests.
	resolve func(ctx context.Context, ip string) (string, bool)
	// lookupA answers DNSBL queries; replaceable in tests.
	lookupA func(ctx context.Context, name string) ([]string, error)
	// dnsblSkip says which IPs are never looked up; replaceable in tests.
	dnsblSkip func(ip string) bool
	dnsbl     dnsblState

	// panelGone keeps the password hash of oper entries the Hive Panel
	// removed, so its Undo brings the password back too.
	panelGone map[int64]string

	rep        repState        // reputation scores (reputation.go)
	nut        newUserThrottle // [connthrottle] counts (secgroup.go)
	lastGroups time.Time       // when everyone's security groups were last worked out
}

type listener struct {
	net.Listener // plain TCP; TLS is added per connection
	tls          bool
	addr         string // as written in the config
	proxy        bool   // PROXY protocol header first (WebSocket: X-Forwarded-For)
	proxyFrom    []*net.IPNet
	websocket    bool
	origins      []string
	tlsConf      *tls.Config // captured: accept goroutines must not read Server.tlsConf
	conf         config.Listen
}

// newListener sets up a client port as conf says, on ln.
func newListener(conf config.Listen, ln net.Listener, tlsConf *tls.Config) listener {
	l := listener{Listener: ln, tls: conf.TLS, addr: conf.Address, proxy: conf.Proxy, tlsConf: tlsConf,
		websocket: conf.WebSocket, origins: conf.Origins, conf: conf}
	for _, m := range conf.ProxyFrom {
		n, _ := config.ParseCIDR(m) // checked by config.Check
		l.proxyFrom = append(l.proxyFrom, n)
	}
	return l
}

const (
	stopRestart  = 1
	stopDie      = 2
	stopHandover = 3 // the clients went to the next server process (handover.go)
)

// ErrRestart is returned by Serve when an admin asked for a restart.
var ErrRestart = errors.New("restart requested")

// New opens the configured ports. Call Serve to start answering.
func New(cfg *config.Config, logger *log.Logger) (*Server, error) {
	return newServer(cfg, logger, nil, nil)
}

// NewBehindGate is New for the server process the gate runs: the gate
// (child's other end) holds the client ports, this process the rest. When
// the gate passes on the clients of the server process before this one,
// they are taken over (handover.go); ErrHandoverBroken means that broke
// half-way and the process should end.
func NewBehindGate(cfg *config.Config, logger *log.Logger, child *gate.Child) (*Server, error) {
	var r *resume
	if h := child.Handover(); h != nil {
		r = resumeFromGate(child, h)
	}
	return newServer(cfg, logger, child, r)
}

func newServer(cfg *config.Config, logger *log.Logger, child *gate.Child, r *resume) (*Server, error) {
	s := &Server{
		gate:         child,
		hand:         handState{freeze: make(chan struct{})},
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
	s.acc = &acceptor{log: logger, wg: &s.wg, done: s.done, hand: s.handToLoop}
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
	s.loadReadMarkers()
	s.loadReputation(time.Now())
	s.loadWhowas(time.Now())
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
	svc.OnSpamChange(s.linkSpamChanged)
	svc.SetGeoLookup(func(ip string) (string, uint32) {
		if !s.cfg.GeoIP.On() {
			return "", 0
		}
		in := s.geo.set.Lookup(ip)
		return in.Country, in.ASN
	})
	svc.OnOperData(func() { s.syncQlines(); s.syncExcepts(); s.gateDenyChanged() })
	s.sinks = append(s.sinks, svc.Event)
	svc.OnServerHelp(s.helpLines)
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
		files := gate.TLSFiles{Cert: cfg.TLS.Cert, Key: cfg.TLS.Key, SelfSigned: cfg.TLS.SelfSigned || cfg.TLS.ACME, Host: cfg.TLS.Domains(cfg.Server.Name)[0]}
		certs, err := tlsutil.NewReloader(files.Cert, files.Key, files.SelfSigned, files.Host)
		if err != nil {
			db.Close()
			return nil, err
		}
		s.certs, s.certChecked, s.certFiles = certs, time.Now(), &files
		tlsConf = certs.Config()
		s.tlsConf = tlsConf
	}
	if err := s.openPorts(cfg.Listen); err != nil {
		s.closeListeners(false)
		db.Close()
		return nil, err
	}
	if err := s.linkSetup(); err != nil {
		s.closeListeners(false)
		db.Close()
		return nil, err
	}
	// Without the socket the server still runs; only the admin commands
	// can't reach it.
	if err := s.openControl(); err != nil {
		logger.Printf("warning: %v; beeircd status, backup and rehash won't reach this server", err)
	}
	if err := s.openRPC(); err != nil {
		s.closeRPC()
		s.closeControl()
		s.closeListeners(false)
		db.Close()
		return nil, err
	}
	if r != nil {
		// Last, when nothing else can stop this server from starting.
		if err := s.takeOver(r); err != nil {
			s.closeRPC()
			s.closeControl()
			s.closeListeners(false)
			db.Close()
			return nil, err
		}
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
	if s.gate != nil {
		s.wg.Add(1)
		go s.gateLoop()
	} else {
		for _, l := range s.listeners {
			s.wg.Add(1)
			go s.acceptLoop(l)
		}
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
	s.startRPC()
	if s.cfg.TLS.ACME && s.certs != nil {
		s.wg.Add(1)
		go s.acmeLoop(s.acmeConfig())
	}
	s.startUpdateCheck()
	s.startGeoIP()
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
			s.runSafe(f)
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
			if s.stopAfter == stopHandover {
				s.finishHandover()
				return ErrHandover
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
			s.repTick(now)
			s.whowasTick(now)
			s.antiSpamTick(now)
			s.groupTick(now)
			s.certTick(now)
			s.svc.Tick(now)
			s.svc.ExpireDue(now)
			s.reap()
			s.flushWrites()
		case now := <-linkTick:
			s.notePeakGlobal() // people coming in over links
			s.linkTick(now)
			s.svc.ExpireDue(now)
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

// acceptor runs the accept loops of the client ports and the slow first
// steps (PROXY header, TLS wrap, WebSocket upgrade) off the loop. hand gets
// each connection once it is ready for IRC: the server's loop in one
// process, or the server process behind the gate (Front). False from hand
// means it was not taken (stopping); the acceptor then closes it. refuse
// (the gate's Front only) closes a client the server refuses anyway
// before any TLS or WebSocket work: on TLS and WebSocket ports, once the
// client's address is known (after a PROXY header). On a WebSocket port
// behind a proxy the address comes with the HTTP request: refuseIP does it
// there.
type acceptor struct {
	log    *log.Logger
	wg     *sync.WaitGroup
	done   <-chan struct{}
	hand   func(conn net.Conn, isTLS bool) bool
	refuse func(conn net.Conn, l listener) bool
	// refuseIP (the gate only) does the same for the visitor address a
	// proxy gives on a WebSocket port, once the HTTP request is read.
	refuseIP func(ip net.IP, conn net.Conn, l listener) bool
}

// acceptLoop starts the accept loop of one client port.
func (s *Server) acceptLoop(l listener) { s.acc.acceptLoop(l) }

func (a *acceptor) acceptLoop(l listener) {
	defer a.wg.Done()
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			a.log.Printf("accept: %v", err)
			select {
			case <-time.After(100 * time.Millisecond):
				continue
			case <-a.done:
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
		if a.refuse != nil && !l.proxy && (l.tls || l.websocket) && a.refuse(conn, l) {
			continue
		}
		if l.websocket {
			a.wg.Add(1)
			go a.acceptWebSocket(conn, l)
			continue
		}
		if l.proxy {
			a.wg.Add(1)
			go a.acceptProxied(conn, l)
			continue
		}
		if l.tls {
			conn = tls.Server(conn, l.tlsConf)
		}
		if !a.hand(conn, l.tls) {
			conn.Close()
			return
		}
	}
}

// handToLoop gives a ready client connection to the loop; false once the
// server is stopping.
func (s *Server) handToLoop(conn net.Conn, isTLS bool) bool {
	select {
	case s.events <- func() { s.newClient(conn, isTLS) }:
		return true
	case <-s.done:
		return false
	}
}

// post hands f to the loop; it gives up when the server is stopping.
func (s *Server) post(f func()) {
	select {
	case s.events <- f:
	case <-s.done:
	}
}

// closeListeners closes the ports and the gate link. With closeGatePorts
// (a real stop) the gate closes its client ports too; without it (newServer
// failed) the gate keeps them for the next server it starts.
func (s *Server) closeListeners(closeGatePorts bool) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	for _, l := range s.listeners {
		l.Close()
	}
	if s.gate != nil {
		if closeGatePorts {
			s.gate.Sync(gate.Spec{})
		}
		s.gate.Close()
	}
}

func (s *Server) shutdown(reason string) {
	s.log.Printf("stopping: %s", reason)
	snap := s.historySnapshot() // before everyone quits and channels go
	close(s.done)
	s.closeListeners(true)
	s.closeLinks(reason)
	s.closeControl()
	s.closeRPC()
	for _, c := range s.clients {
		s.quit(c, reason)
	}
	s.finishStop(snap)
}

// finishStop is the end of every stop: it waits for the connections to
// close, saves what is kept across restarts and closes the database.
func (s *Server) finishStop(snap history.Snapshot) {
	finished := make(chan struct{})
	go func() { s.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		s.log.Printf("some connections did not close in time")
	}
	s.saveHistorySnapshot(snap)
	s.svc.SaveSeen()
	s.saveReputation(time.Now())
	s.saveWhowas(time.Now())
	s.svc.CloseOutside()
	s.svc.CloseLogFile()
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
			if c.authHeld {
				if now.After(c.authUntil) {
					s.authExpired(c)
				}
				continue
			}
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
