package server

// Front: the client ports inside the gate process (internal/gate). It uses
// the same accept code as the server in one process (acceptor), so PROXY,
// TLS and WebSocket behave the same; it only hands clients on instead of
// giving them to the loop. It never touches server state.

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// Front owns the client ports in the gate process.
type Front struct {
	deliver gate.Deliver
	acc     *acceptor
	wg      sync.WaitGroup // accept goroutines; nobody waits: the gate exits
	done    chan struct{}
	wait    atomic.Int64 // how long a TLS handshake may take, in nanoseconds

	mu      sync.Mutex // guards the rest; Apply runs one at a time
	closed  bool
	ports   []frontPort
	files   *gate.TLSFiles
	certs   *tlsutil.Reloader
	tlsConf *tls.Config
	certGen int
}

type frontPort struct {
	key string
	l   listener
}

// NewFront makes a Front with no ports; Apply opens them.
func NewFront(logger *log.Logger, deliver gate.Deliver) *Front {
	f := &Front{deliver: deliver, done: make(chan struct{})}
	f.wait.Store(int64(30 * time.Second))
	f.acc = &acceptor{log: logger, wg: &f.wg, done: f.done, hand: f.hand}
	return f
}

// Apply makes the open ports match spec: ports in it are opened or kept,
// the others closed. It never waits for a client.
func (f *Front) Apply(spec gate.Spec) gate.Result {
	res := gate.Result{Addrs: make([]string, len(spec.Ports)), Errors: make([]string, len(spec.Ports))}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		for i := range res.Errors {
			res.Errors[i] = "the gate is stopping"
		}
		return res
	}
	if spec.Wait > 0 {
		f.wait.Store(int64(spec.Wait))
	}
	if err := f.setTLS(spec); err != nil {
		res.Err = err.Error()
		return res
	}
	// Keep a port whose settings didn't change (a TLS one only with the
	// same certificate setup); close the rest before opening new ones, so
	// a port can move between settings.
	old := make(map[string]listener, len(f.ports))
	want := make(map[string]bool, len(spec.Ports))
	for _, c := range spec.Ports {
		want[portKey(c)] = true
	}
	for _, p := range f.ports {
		if !want[p.key] || (p.l.tls && p.l.tlsConf != f.tlsConf) {
			p.l.Close()
			continue
		}
		old[p.key] = p.l
	}
	f.ports = f.ports[:0]
	for i, c := range spec.Ports {
		k := portKey(c)
		l, ok := old[k]
		if ok {
			delete(old, k)
		} else {
			if c.TLS && f.tlsConf == nil {
				res.Errors[i] = "no certificate for a TLS port"
				continue
			}
			ln, err := net.Listen("tcp", c.Address)
			if err != nil {
				res.Errors[i] = err.Error()
				continue
			}
			l = newListener(c, ln, f.tlsConf)
			f.wg.Add(1)
			go f.acc.acceptLoop(l)
		}
		f.ports = append(f.ports, frontPort{k, l})
		res.Addrs[i] = l.Addr().String()
	}
	for _, l := range old { // the same port twice in spec: never happens
		l.Close()
	}
	return res
}

// setTLS loads the certificate spec names, or reads it again when the
// server did (CertGen).
func (f *Front) setTLS(spec gate.Spec) error {
	if spec.TLS == nil {
		return nil
	}
	if f.certs == nil || *f.files != *spec.TLS {
		certs, err := tlsutil.NewReloader(spec.TLS.Cert, spec.TLS.Key, spec.TLS.SelfSigned, spec.TLS.Host)
		if err != nil {
			return err
		}
		files := *spec.TLS
		f.certs, f.files, f.tlsConf, f.certGen = certs, &files, certs.Config(), spec.CertGen
		return nil
	}
	if spec.CertGen != f.certGen {
		if err := f.certs.Reload(); err != nil {
			return err
		}
		f.certGen = spec.CertGen
	}
	return nil
}

// Close stops accepting. Connected clients stay.
func (f *Front) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	close(f.done)
	for _, p := range f.ports {
		p.l.Close()
	}
	f.ports = nil
}

// hand takes a client the acceptor made ready. A TLS client shakes hands
// first (in its own goroutine), so the server learns its certificate.
func (f *Front) hand(conn net.Conn, isTLS bool) bool {
	if tc, ok := conn.(*tls.Conn); ok {
		go f.handshake(tc)
		return true
	}
	f.send(conn, isTLS)
	return true
}

func (f *Front) handshake(tc *tls.Conn) {
	tc.SetDeadline(time.Now().Add(time.Duration(f.wait.Load())))
	if err := tc.Handshake(); err != nil {
		tc.Close()
		return
	}
	tc.SetDeadline(time.Time{})
	f.send(tc, true)
}

// send hands one ready client to the server process with what the gate
// knows about it.
func (f *Front) send(conn net.Conn, isTLS bool) {
	info := gate.Info{Remote: conn.RemoteAddr().String(), Local: conn.LocalAddr().String(), TLS: isTLS}
	switch c := conn.(type) {
	case *tls.Conn:
		st := c.ConnectionState()
		info.Cipher = tls.CipherSuiteName(st.CipherSuite)
		info.CertFP = certFP(st)
	case *wsConn:
		info.WebSocket = true // no certificate, as in one process
	}
	if !f.deliver(conn, info) {
		conn.Close()
	}
}

// portKey tells two port settings apart.
func portKey(c config.Listen) string { return fmt.Sprintf("%#v", c) }

// certFP is the SHA-256 of the client's certificate in hex, or "".
func certFP(st tls.ConnectionState) string {
	if len(st.PeerCertificates) == 0 {
		return ""
	}
	sum := sha256.Sum256(st.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}
