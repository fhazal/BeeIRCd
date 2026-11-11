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
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// Server is one running BeeIRCd.
type Server struct {
	cfg        *config.Config
	log        *log.Logger
	net        *state.Network
	clients    map[string]*Client // by UID, registered or not
	events     chan func()
	done       chan struct{}
	listeners  []listener
	created    time.Time
	whowas     []whowasEntry
	maxLocal   int
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
	loop       loopStats           // how long each piece of loop work took
	wg         sync.WaitGroup

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
}

type listener struct {
	net.Listener
	tls  bool
	addr string // as written in the config
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
		cfg:     cfg,
		log:     logger,
		net:     state.NewNetwork(cfg.Server.SID),
		clients: make(map[string]*Client),
		perIP:   make(map[string]int),
		opers:   make(map[*Client]bool),
		events:  make(chan func(), 1024),
		done:    make(chan struct{}),
		created: time.Now(),
		resolve: lookupHost,
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Services.Database), 0o750); err != nil {
		return nil, err
	}
	db, err := store.Open(cfg.Services.Database)
	if err != nil {
		return nil, err
	}
	s.store = db
	svc, err := services.New(host{s}, cfg, logger, s.net, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("services database: %w", err)
	}
	s.svc = svc
	s.sinks = append(s.sinks, svc.Event)
	svc.Start()
	s.adminServ = svc.AddBot("AdminServ", "as", "Admin Services",
		"AdminServ changes the server while it runs; changes are saved to beeircd.conf. Commands:", 'a', s.adminServCommands())
	svc.SyncLogChannel("")
	svc.Flush()
	var tlsConf *tls.Config
	if cfg.NeedsTLS() {
		var err error
		tlsConf, err = tlsutil.Load(cfg.TLS.Cert, cfg.TLS.Key, cfg.TLS.SelfSigned, cfg.Server.Name)
		if err != nil {
			db.Close()
			return nil, err
		}
		s.tlsConf = tlsConf
	}
	for _, l := range cfg.Listen {
		ln, err := net.Listen("tcp", l.Address)
		if err != nil {
			s.closeListeners()
			db.Close()
			return nil, fmt.Errorf("listen on %s: %w", l.Address, err)
		}
		if l.TLS {
			ln = tls.NewListener(ln, tlsConf)
		}
		s.listeners = append(s.listeners, listener{ln, l.TLS, l.Address})
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
	s.log.Printf("%s ready on %d port(s)", s.cfg.Server.Name, len(s.listeners))
	tick := time.NewTicker(s.tickEvery())
	defer tick.Stop()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	s.svc.Expire(time.Now())
	for {
		select {
		case <-ctx.Done():
			s.shutdown("Server shutting down")
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
			s.svc.Tick(now)
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
	d := min(l.PingInterval.Duration, l.PingTimeout.Duration, l.RegisterTimeout.Duration, s.cfg.Services.IdentifyWithin.Duration)
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
	close(s.done)
	s.closeListeners()
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
	if err := s.store.Close(); err != nil {
		s.log.Printf("closing services database: %v", err)
	}
	s.log.Printf("%s stopped", s.cfg.Server.Name)
}

// reap disconnects clients whose send queue overflowed.
func (s *Server) reap() {
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
