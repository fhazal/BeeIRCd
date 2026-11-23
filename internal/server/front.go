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
	"net/netip"
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
	deny    atomic.Pointer[denyList]

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
	f.acc = &acceptor{log: logger, wg: &f.wg, done: f.done, hand: f.hand, refuse: f.refuse, refuseIP: f.refuseIP}
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
	f.deny.Store(newDenyList(spec.Deny))
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

// refuse closes a client the server refuses anyway (Spec.Deny) before
// any TLS or WebSocket work, and tells the server, which counts and logs
// it as its own refusal. True: it was refused.
func (f *Front) refuse(conn net.Conn, l listener) bool {
	d := f.deny.Load()
	if d == nil {
		return false
	}
	ip, ok := addrOf(conn.RemoteAddr())
	if !ok || !d.has(ip, time.Now()) {
		return false
	}
	info := gate.Info{Remote: conn.RemoteAddr().String(), Local: conn.LocalAddr().String(), TLS: l.tls, WebSocket: l.websocket}
	conn.Close()
	f.deliver(nil, info)
	return true
}

// refuseIP is refuse for a visitor behind a proxy on a WebSocket port: ip
// is the address from the proxy's headers, read before the upgrade is
// answered. The server is told the visitor's address, not the proxy's.
func (f *Front) refuseIP(ip net.IP, conn net.Conn, l listener) bool {
	d := f.deny.Load()
	a, ok := netip.AddrFromSlice(ip)
	if d == nil || !ok || !d.has(a.Unmap(), time.Now()) {
		return false
	}
	f.deliver(nil, gate.Info{Remote: (&net.TCPAddr{IP: ip}).String(), Local: conn.LocalAddr().String(), TLS: l.tls, WebSocket: true})
	return true
}

// denyList is Spec.Deny, ready for lookups: single addresses in a map,
// wider prefixes in a list.
type denyList struct {
	ips  map[netip.Addr]int64 // until, Unix ms; 0: no end
	nets []denyNet
}

type denyNet struct {
	p     netip.Prefix
	until int64
}

// newDenyList reads Spec.Deny; nil when it is empty. Entries that don't
// parse are skipped.
func newDenyList(in []gate.Deny) *denyList {
	if len(in) == 0 {
		return nil
	}
	d := &denyList{ips: make(map[netip.Addr]int64)}
	for _, e := range in {
		p, err := netip.ParsePrefix(e.Net)
		if err != nil {
			continue
		}
		if p.IsSingleIP() {
			d.ips[p.Addr()] = e.Until
		} else {
			d.nets = append(d.nets, denyNet{p, e.Until})
		}
	}
	return d
}

// has reports whether ip is refused at now. ip is unmapped, as the server
// sees it.
func (d *denyList) has(ip netip.Addr, now time.Time) bool {
	ms := now.UnixMilli()
	if until, ok := d.ips[ip]; ok && (until == 0 || ms < until) {
		return true
	}
	for _, n := range d.nets {
		if (n.until == 0 || ms < n.until) && n.p.Contains(ip) {
			return true
		}
	}
	return false
}

// addrOf is the IP of a client address, unmapped (an IPv4 client on an
// IPv6 socket is its IPv4 address, as the server sees it).
func addrOf(a net.Addr) (netip.Addr, bool) {
	if ta, ok := a.(*net.TCPAddr); ok {
		ip, ok := netip.AddrFromSlice(ta.IP)
		return ip.Unmap(), ok
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
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
