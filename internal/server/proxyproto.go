package server

// PROXY protocol v1 and v2 (HAProxy's spec) on [[listen]] ports with
// proxy = true: a load balancer or a VPS tunnel sends the real client
// address first, so cloaks, bans, DNSBL and the throttle see the person,
// not the proxy. Only proxy_from addresses may connect to such a port.

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	proxyWait     = 5 * time.Second // the header must arrive within this
	proxyV2Max    = 2048            // most address + TLV bytes accepted
	proxyV1MaxLen = 107             // the spec's longest v1 line
)

var proxyV2Sig = []byte("\r\n\r\n\x00\r\nQUIT\n")

var errProxyHeader = errors.New("bad PROXY protocol header")

// proxyConn is a connection whose first bytes were a PROXY header: reads
// start with what was buffered past it, and RemoteAddr is the real client.
type proxyConn struct {
	net.Conn
	r      *bufio.Reader
	remote net.Addr
}

func (p *proxyConn) Read(b []byte) (int, error) {
	if p.r != nil {
		if p.r.Buffered() > 0 {
			return p.r.Read(b)
		}
		p.r = nil
	}
	return p.Conn.Read(b)
}

func (p *proxyConn) RemoteAddr() net.Addr { return p.remote }

// readProxyHeader reads a v1 or v2 header from r. A LOCAL (v2) or UNKNOWN
// (v1) header, or an address family other than TCP over IPv4/IPv6, keeps
// real (the proxy's own address).
func readProxyHeader(r *bufio.Reader, real net.Addr) (net.Addr, error) {
	head, err := r.Peek(12)
	if err != nil {
		return nil, errProxyHeader
	}
	if bytes.Equal(head, proxyV2Sig) {
		return readProxyV2(r, real)
	}
	if !bytes.HasPrefix(head, []byte("PROXY ")) {
		return nil, errProxyHeader
	}
	var line []byte
	for len(line) <= proxyV1MaxLen {
		b, err := r.ReadByte()
		if err != nil {
			return nil, errProxyHeader
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
	}
	s, ok := strings.CutSuffix(string(line), "\r\n")
	if !ok {
		return nil, errProxyHeader
	}
	f := strings.Split(s, " ")
	if len(f) >= 2 && f[1] == "UNKNOWN" {
		return real, nil
	}
	if len(f) != 6 || (f[1] != "TCP4" && f[1] != "TCP6") {
		return nil, errProxyHeader
	}
	ip := net.ParseIP(f[2])
	port, err := strconv.Atoi(f[4])
	if ip == nil || err != nil || port < 0 || port > 65535 || (f[1] == "TCP4") != (ip.To4() != nil) {
		return nil, errProxyHeader
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}

func readProxyV2(r *bufio.Reader, real net.Addr) (net.Addr, error) {
	var h [16]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, errProxyHeader
	}
	verCmd, fam := h[12], h[13]
	n := int(binary.BigEndian.Uint16(h[14:16]))
	if verCmd>>4 != 2 || n > proxyV2Max {
		return nil, errProxyHeader
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, errProxyHeader
	}
	switch verCmd & 0x0f {
	case 0: // LOCAL: the proxy talking for itself (health checks)
		return real, nil
	case 1: // PROXY
	default:
		return nil, errProxyHeader
	}
	switch fam {
	case 0x11: // TCP over IPv4
		if n < 12 {
			return nil, errProxyHeader
		}
		return &net.TCPAddr{IP: net.IP(append([]byte(nil), body[0:4]...)), Port: int(binary.BigEndian.Uint16(body[8:10]))}, nil
	case 0x21: // TCP over IPv6
		if n < 36 {
			return nil, errProxyHeader
		}
		return &net.TCPAddr{IP: net.IP(append([]byte(nil), body[0:16]...)), Port: int(binary.BigEndian.Uint16(body[32:34]))}, nil
	}
	return real, nil
}

// proxyAllowed reports whether addr may speak PROXY on l.
func (l listener) proxyAllowed(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, n := range l.proxyFrom {
		if ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// acceptProxied reads the PROXY header off the loop, then hands the
// connection on like any other.
func (a *acceptor) acceptProxied(conn net.Conn, l listener) {
	defer a.wg.Done()
	if !l.proxyAllowed(conn.RemoteAddr()) {
		conn.Close() // not our proxy: nobody else may claim an address
		return
	}
	conn.SetReadDeadline(time.Now().Add(proxyWait))
	r := bufio.NewReaderSize(conn, 512)
	remote, err := readProxyHeader(r, conn.RemoteAddr())
	if err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	var c net.Conn = &proxyConn{Conn: conn, r: r, remote: remote}
	if l.tls && a.refuse != nil && a.refuse(c, l) {
		return
	}
	if l.tls {
		c = tls.Server(c, l.tlsConf)
	}
	if !a.hand(c, l.tls) {
		conn.Close()
	}
}
