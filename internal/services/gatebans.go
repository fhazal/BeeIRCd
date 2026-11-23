package services

import (
	"net/netip"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// GateBan is a ZLINE the gate in front of the server can apply itself,
// before any TLS work: Net is refused until Until (zero: no end).
type GateBan struct {
	Net   netip.Prefix
	Until time.Time
}

// GateZlines are the ZLINEs the gate can apply: those on one IP or a CIDR,
// where ZlineFor would refuse every address in it. A ZLINE an ELINE may cut
// into is left out, and all of them while an ELINE is too wide to tell
// (a place, or a host mask that may match an IP); the server still
// refuses those itself.
func (s *Services) GateZlines() []GateBan {
	now := s.banNow() // NOEXPIRE keeps run-out entries in force
	var allow []netip.Prefix
	for _, b := range s.netbans {
		if b.Kind != store.BanEline || !b.Live(now) {
			continue
		}
		if irc.IsPlaceMask(b.Mask) {
			return nil
		}
		_, h, _ := strings.Cut(b.Mask, "@")
		if p, ok := gateNet(h, false); ok {
			allow = append(allow, p)
			continue
		}
		if mayMatchIP(h) {
			return nil
		}
	}
	var out []GateBan
	for _, b := range s.netbans {
		if b.Kind != store.BanZline || !b.Live(now) || b.Soft() || irc.IsPlaceMask(b.Mask) {
			continue
		}
		p, ok := gateNet(b.Mask, true)
		if !ok {
			continue
		}
		cut := false
		for _, a := range allow {
			if a.Overlaps(p) {
				cut = true
				break
			}
		}
		if cut {
			continue
		}
		until := b.Expires
		if s.noexpire {
			until = time.Time{}
		}
		out = append(out, GateBan{p, until})
	}
	return out
}

// BanEnd is when b stops refusing people: zero for never, or while
// NOEXPIRE keeps run-out entries in force.
func (s *Services) BanEnd(b *store.NetBan) time.Time {
	if s.noexpire {
		return time.Time{}
	}
	return b.Expires
}

// gateNet reads a mask as irc.MatchHost matches it against an IP: a CIDR,
// or one IP. exact: an IP must be written the way the server writes a
// client's IP, since MatchHost compares that as text.
func gateNet(m string, exact bool) (netip.Prefix, bool) {
	if strings.Contains(m, "/") {
		p, err := netip.ParsePrefix(m)
		return p, err == nil
	}
	a, err := netip.ParseAddr(m)
	if err != nil || a.Zone() != "" || a.Is4In6() || (exact && !strings.EqualFold(a.String(), m)) {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// mayMatchIP reports whether host mask h might match an IP written out
// (or the empty host): it has nothing but hex digits, dots, colons and
// wildcards.
func mayMatchIP(h string) bool {
	return strings.Trim(strings.ToLower(h), "0123456789abcdef.:*?") == ""
}
