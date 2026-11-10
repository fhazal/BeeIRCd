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
	"sync"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/state"
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
	toKill    []*Client
	msgSeq    uint64
	batchSeq  uint64
	wg        sync.WaitGroup

	// resolve looks up a hostname for an IP; replaceable in tests.
	resolve func(ctx context.Context, ip string) (string, bool)
}

type listener struct {
	net.Listener
	tls bool
}

// New opens the configured ports. Call Serve to start answering.
func New(cfg *config.Config, logger *log.Logger) (*Server, error) {
	s := &Server{
		cfg:     cfg,
		log:     logger,
		net:     state.NewNetwork(cfg.Server.SID),
		clients: make(map[string]*Client),
		events:  make(chan func(), 1024),
		done:    make(chan struct{}),
		created: time.Now(),
		resolve: lookupHost,
	}
	var tlsConf *tls.Config
	if cfg.NeedsTLS() {
		var err error
		tlsConf, err = tlsutil.Load(cfg.TLS.Cert, cfg.TLS.Key, cfg.TLS.SelfSigned, cfg.Server.Name)
		if err != nil {
			return nil, err
		}
	}
	for _, l := range cfg.Listen {
		ln, err := net.Listen("tcp", l.Address)
		if err != nil {
			s.closeListeners()
			return nil, fmt.Errorf("listen on %s: %w", l.Address, err)
		}
		if l.TLS {
			ln = tls.NewListener(ln, tlsConf)
		}
		s.listeners = append(s.listeners, listener{ln, l.TLS})
	}
	return s, nil
}

// Addrs returns the address of each open port, in config order.
func (s *Server) Addrs() []net.Addr {
	out := make([]net.Addr, len(s.listeners))
	for i, l := range s.listeners {
		out[i] = l.Addr()
	}
	return out
}

// Serve runs until ctx is cancelled, then closes every connection.
func (s *Server) Serve(ctx context.Context) error {
	for _, l := range s.listeners {
		s.wg.Add(1)
		go s.acceptLoop(l)
	}
	s.log.Printf("%s ready on %d port(s)", s.cfg.Server.Name, len(s.listeners))
	tick := time.NewTicker(s.tickEvery())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return nil
		case f := <-s.events:
			f()
			s.reap()
		case now := <-tick.C:
			s.checkTimeouts(now)
			s.reap()
		}
	}
}

// tickEvery checks timeouts often enough for the shortest one.
func (s *Server) tickEvery() time.Duration {
	l := s.cfg.Limits
	d := min(l.PingInterval.Duration, l.PingTimeout.Duration, l.RegisterTimeout.Duration)
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
	for _, l := range s.listeners {
		l.Close()
	}
}

func (s *Server) shutdown() {
	close(s.done)
	s.closeListeners()
	for _, c := range s.clients {
		s.quit(c, "Server shutting down")
	}
	finished := make(chan struct{})
	go func() { s.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		s.log.Printf("some connections did not close in time")
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
