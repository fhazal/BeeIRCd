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
		s.snoticeEvery("webirc-host:"+gw, time.Minute, 'a', "WEBIRC from %s refused: not a [[webirc]] gateway host", gw)
		s.quit(c, "WEBIRC: this address is not a trusted gateway")
		return
	}
	pw, name, flags := m.Param(0), m.Param(1), m.Param(4)
	ip, ok := webircIP(m.Param(3))
	if !ok {
		s.snoticeEvery("webirc-ip:"+gw, time.Minute, 'a', "WEBIRC from %s (%s) refused: %q is not an IP", gw, name, m.Param(3))
		s.quit(c, "WEBIRC: the IP is not valid")
		return
	}
	host := webircHost(m.Param(2), ip)
	refuse := func() {
		s.snoticeEvery("webirc-pw:"+gw, time.Minute, 'a', "WEBIRC from %s (%s) refused: wrong password", gw, name)
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
	c.identBusy, c.identAnswer = false, "" // an ident answer was the gateway's
	c.hostDone = true
	c.dnsblBusy, c.dnsblListed = false, nil
	s.log.Printf("webirc %s: %s is %s (%s)", name, old, ip, host)

	if z := s.svc.ZlineFor(ip); z != nil {
		s.svc.LogBanHit(z.Kind, z.Mask, ip, z.SetBy, z.Reason)
		s.sno('x', "Z-line %s stopped a connection from %s (WEBIRC %s)", z.Mask, ip, name)
		s.quit(c, "Z-lined: "+z.Reason)
		return
	}
	if why := s.admitConn(c, time.Now()); why != "" {
		s.quit(c, why)
		return
	}
	max := s.sessionMax(ip)
	if n, ok := s.svc.SessionExcept(ip, host); ok {
		max = n // the gateway gave the hostname: an exception on it counts too
	}
	if n := s.sessions(ip); n > max {
		s.svc.LogSessionLimit(ip, max, c.logMask(c.nick))
		s.sno('a', "Client %s with IP %s rejected: maxperip limit exceeded (%d local, max %d) (WEBIRC %s)", orStr(c.nick, "[Unknown user]"), ip, n, max, name)
		s.quit(c, s.cfg.Messages.TooManyText())
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
	// A zone names the gateway's own network interface: nothing to keep,
	// and it may hold ! @ * that would break the person's prefix.
	return a.Unmap().WithZone("").String(), true
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
