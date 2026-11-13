package server

// WEBIRC (IRCv3 WebIRC): a trusted web gateway ([[webirc]] in beeircd.conf,
// like BeeRoom / The Lounge) sends, before registration,
//
//	WEBIRC <password> <gateway> <hostname> <ip> [:flags]
//
// and from then on the connection is the person behind the gateway: their
// IP and host count for Z-lines, bans, session limits, the connect
// throttle, DNSBL, cloaks and WHOIS, and travel over server links. The
// gateway's own IP skips the per-IP limits (it carries many people), but
// only listed gateway IPs may send WEBIRC at all.

import (
	"crypto/subtle"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/passwd"
)

// webircGateway reports whether ip is a [[webirc]] gateway host.
func (s *Server) webircGateway(ip string) bool {
	return len(s.cfg.WebIRCFor(ip)) > 0
}

func (s *Server) cmdWebirc(c *Client, m irc.Message) {
	if c.registered || c.webirc != "" || c.webircBusy {
		return // after registration (or a second time): ignored, as the spec says
	}
	gw := c.ip
	blocks := s.cfg.WebIRCFor(gw)
	if len(blocks) == 0 {
		s.snoticeEvery("webirc-host:"+gw, time.Minute, "WEBIRC from %s refused: not a [[webirc]] gateway host", gw)
		s.quit(c, "WEBIRC: this address is not a trusted gateway")
		return
	}
	pw, name, flags := m.Param(0), m.Param(1), m.Param(4)
	ip, ok := webircIP(m.Param(3))
	if !ok {
		s.snoticeEvery("webirc-ip:"+gw, time.Minute, "WEBIRC from %s (%s) refused: %q is not an IP", gw, name, m.Param(3))
		s.quit(c, "WEBIRC: the IP is not valid")
		return
	}
	host := webircHost(m.Param(2), ip)
	refuse := func() {
		s.snoticeEvery("webirc-pw:"+gw, time.Minute, "WEBIRC from %s (%s) refused: wrong password", gw, name)
		s.quit(c, "WEBIRC: wrong password")
	}
	var hashed []config.WebIRC
	for _, b := range blocks {
		if b.Hashed() {
			hashed = append(hashed, b)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(pw), []byte(b.Password)) == 1 {
			s.webircApply(c, b.Name, ip, host, flags)
			return
		}
	}
	if len(hashed) == 0 {
		refuse()
		return
	}
	// A mkpasswd hash: argon2 off the loop; registration waits for it.
	c.webircBusy = true
	go func() {
		match := ""
		for _, b := range hashed {
			if passwd.Check(pw, b.Password) {
				match = b.Name
				break
			}
		}
		s.post(func() {
			c.webircBusy = false
			if c.closed {
				return
			}
			if match == "" {
				refuse()
				return
			}
			s.webircApply(c, match, ip, host, flags)
		})
	}()
}

// webircApply turns c into the person behind gateway name.
func (s *Server) webircApply(c *Client, name, ip, host, flags string) {
	old := c.ip
	gatewayTLS := c.tls
	if a, err := netip.ParseAddr(old); err == nil && a.Unmap().IsLoopback() {
		gatewayTLS = true // same machine: nothing travels in the clear
	}
	if s.perIP[old]--; s.perIP[old] <= 0 {
		delete(s.perIP, old)
	}
	s.doneUnregistered(c)
	c.ip, c.host = ip, host
	s.perIP[ip]++
	c.webirc = name
	c.certfp = "" // the gateway's certificate is not the person's
	c.tls = gatewayTLS && slices.Contains(strings.Fields(flags), "secure")
	// Forget lookups still running for the gateway's IP.
	c.lookupGen++
	c.hostDone = true
	c.dnsblBusy, c.dnsblListed = false, nil
	s.log.Printf("webirc %s: %s is %s (%s)", name, old, ip, host)

	if z := s.svc.ZlineFor(ip); z != nil {
		s.snotice("ZLINE %s stopped a connection from %s (WEBIRC %s)", z.Mask, ip, name)
		s.quit(c, "Z-lined: "+z.Reason)
		return
	}
	if why := s.admitConn(c, time.Now()); why != "" {
		s.quit(c, why)
		return
	}
	if n, max := s.sessions(ip), s.svc.SessionLimit(ip); n > max {
		s.snotice("Too many connections from %s (%d, the limit is %d, WEBIRC %s)", ip, n, max, name)
		s.quit(c, fmt.Sprintf("Too many connections from your IP (the limit is %d)", max))
		return
	}
	s.dnsblStart(c)
	if c.closed {
		return
	}
	s.tryRegister(c)
}

// webircIP checks the IP a gateway sent.
func webircIP(v string) (string, bool) {
	a, err := netip.ParseAddr(v)
	if err != nil || a.IsUnspecified() {
		return "", false
	}
	return a.Unmap().String(), true
}

// webircHost uses the gateway's hostname when it looks like one, else the IP.
func webircHost(name, ip string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" || name == ip || len(name) > 63 || !strings.Contains(name, ".") {
		return irc.DisplayIP(ip)
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return irc.DisplayIP(ip) // an IP that isn't theirs
	}
	letter := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			letter = true
		case r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return irc.DisplayIP(ip)
		}
	}
	if !letter || strings.HasPrefix(name, ".") || strings.Contains(name, "..") || strings.HasPrefix(name, "-") {
		return irc.DisplayIP(ip)
	}
	return name
}
