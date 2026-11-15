package irc

import (
	"net/netip"
	"strings"
)

// MatchHost matches a host mask against host: a glob like *.example.org,
// or a CIDR range like 10.0.0.0/8 or 2001:db8::/32 (which only matches IPs).
func MatchHost(mask, host string) bool {
	if strings.Contains(mask, "/") {
		p, err := netip.ParsePrefix(mask)
		if err != nil {
			return false
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return false
		}
		return p.Contains(a.Unmap())
	}
	return Match(mask, host)
}

// MatchUserHost matches a user@host mask against ident at any of hosts.
func MatchUserHost(mask, ident string, hosts ...string) bool {
	user, host, ok := strings.Cut(mask, "@")
	if !ok || !Match(user, ident) {
		return false
	}
	for _, h := range hosts {
		if h != "" && MatchHost(host, h) {
			return true
		}
	}
	return false
}

// IsCIDR reports whether mask is a valid CIDR range.
func IsCIDR(mask string) bool {
	_, err := netip.ParsePrefix(mask)
	return err == nil
}
