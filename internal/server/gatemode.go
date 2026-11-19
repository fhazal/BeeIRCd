package server

// The server process behind the gate (internal/gate): the gate holds the
// client ports and hands each client over; this file is the server's side
// of that. In one-process mode (s.gate == nil) none of it runs.

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
)

// gatePort stands for a client port the gate holds open. It knows the
// address; its clients arrive through gateLoop, so Accept never returns one.
type gatePort struct{ addr net.Addr }

func (p gatePort) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (p gatePort) Close() error              { return nil } // syncGate closes it in the gate
func (p gatePort) Addr() net.Addr            { return p.addr }

// openPorts opens the configured client ports, here or in the gate.
func (s *Server) openPorts(confs []config.Listen) error {
	if s.gate == nil {
		for _, c := range confs {
			ln, err := net.Listen("tcp", c.Address)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", c.Address, err)
			}
			s.listeners = append(s.listeners, newListener(c, ln, s.tlsConf))
		}
		return nil
	}
	ls, errs, err := s.gateSync(confs)
	if err != nil {
		return err
	}
	for i, e := range errs {
		if e != "" {
			return fmt.Errorf("listen on %s: %s", confs[i].Address, e)
		}
	}
	s.listeners = ls
	return nil
}

// openPort opens one more client port (AdminServ PORT ADD). The error is
// the one net.Listen gave, here or in the gate.
func (s *Server) openPort(conf config.Listen) (listener, error) {
	if s.gate == nil {
		ln, err := net.Listen("tcp", conf.Address)
		if err != nil {
			return listener{}, err
		}
		return newListener(conf, ln, s.tlsConf), nil
	}
	ls, errs, err := s.gateSync(append(s.listenConfs(), conf))
	if err != nil {
		return listener{}, err
	}
	if e := errs[len(errs)-1]; e != "" {
		return listener{}, errors.New(e)
	}
	return ls[len(ls)-1], nil
}

// listenConfs returns the settings of the open client ports.
func (s *Server) listenConfs() []config.Listen {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	confs := make([]config.Listen, len(s.listeners))
	for i, l := range s.listeners {
		confs[i] = l.conf
	}
	return confs
}

// gateSync asks the gate to have exactly these client ports open, with the
// current certificate. Per port it returns a listener (the bound address
// only) and the error text, "" when it is open; err is for a gate that
// didn't answer or a certificate it couldn't load (no port changed then).
func (s *Server) gateSync(confs []config.Listen) ([]listener, []string, error) {
	spec := gate.Spec{Ports: confs, CertGen: s.certGen, Wait: s.cfg.Limits.RegisterTimeout.Duration}
	if s.certs != nil {
		spec.TLS = s.certFiles
	}
	res, err := s.gate.Sync(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("gate: %w", err)
	}
	if res.Err != "" {
		return nil, nil, fmt.Errorf("gate: the certificate didn't load: %s", res.Err)
	}
	ls := make([]listener, len(confs))
	for i, c := range confs {
		var addr net.Addr = &net.TCPAddr{}
		if ap, err := netip.ParseAddrPort(res.Addrs[i]); err == nil {
			addr = net.TCPAddrFromAddrPort(ap)
		}
		ls[i] = newListener(c, gatePort{addr}, s.tlsConf)
	}
	return ls, res.Errors, nil
}

// syncGate tells the gate the client ports and certificate as they are
// now: after PORT DEL, a failed PORT ADD, a rehash or a new certificate.
func (s *Server) syncGate() {
	if s.gate == nil {
		return
	}
	confs := s.listenConfs()
	_, errs, err := s.gateSync(confs)
	if err != nil {
		s.log.Print(err)
		return
	}
	for i, e := range errs {
		if e != "" {
			s.log.Printf("gate: listen on %s: %s", confs[i].Address, e)
		}
	}
}

// certsChanged runs on the loop after the certificate was read again, so
// the gate's TLS ports read it too.
func (s *Server) certsChanged() {
	s.certGen++
	s.syncGate()
}

// gateLoop takes the clients the gate hands over, like acceptLoop.
func (s *Server) gateLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.gate.Accept()
		if err != nil {
			return // closed, or the gate is gone (main stops the server)
		}
		isTLS := false
		if gc, ok := conn.(*gate.Conn); ok {
			isTLS = gc.Info.TLS
		}
		if !s.handToLoop(conn, isTLS) {
			conn.Close()
			return
		}
	}
}
