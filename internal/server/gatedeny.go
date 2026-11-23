package server

// The gate refuses Z-lined and throttled IPs itself, before any TLS
// handshake or WebSocket upgrade. This server tells it which: the ZLINEs
// on an IP or a CIDR that no ELINE cuts into (services GateZlines), and
// each IP this server refused (a ZLINE with a wider mask, or the connect
// throttle) for as long as it stays refused. The gate reports each client
// it refused (gateRefused), so logs, notices and throttle strikes come out
// as they do without the gate.

import (
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/fhazal/beeircd/internal/gate"
)

const (
	// maxGateDeny bounds the list sent to the gate (a Sync message has to
	// stay under 64 KB); the server refuses the rest itself, after TLS.
	maxGateDeny = 500
	// maxGateLearned bounds the IPs remembered for it.
	maxGateLearned = 1000
	// gateDenyDelay gathers changes into one Sync.
	gateDenyDelay = 100 * time.Millisecond
)

type gateDenyState struct {
	learned map[string]time.Time // refused IPs, when learned
	sent    []gate.Deny          // the list the gate has
	pending bool                 // a syncGateDeny is on its way
}

// gateLearn notes that this server refused ip, so the gate refuses it next
// time.
func (s *Server) gateLearn(ip string) {
	d := &s.gateDeny
	if s.gate == nil {
		return
	}
	if _, ok := d.learned[ip]; ok || len(d.learned) >= maxGateLearned {
		return
	}
	if d.learned == nil {
		d.learned = make(map[string]time.Time)
	}
	d.learned[ip] = time.Now()
	s.gateDenyChanged()
}

// gateDenyChanged sends the gate a new list soon (bans changed).
func (s *Server) gateDenyChanged() {
	if s.gate == nil || s.gateDeny.pending {
		return
	}
	s.gateDeny.pending = true
	time.AfterFunc(gateDenyDelay, func() { s.post(s.syncGateDeny) })
}

// syncGateDeny sends the gate the list when it changed.
func (s *Server) syncGateDeny() {
	d := &s.gateDeny
	d.pending = false
	if s.gate == nil || s.hand.started {
		return
	}
	list := s.gateDenyList(time.Now())
	if slices.Equal(list, d.sent) {
		return
	}
	old := d.sent
	d.sent = list
	if _, _, err := s.gateSync(s.listenConfs()); err != nil {
		d.sent = old
		s.log.Print(err)
	}
}

// gateDenyList is the list for the gate: the ZLINEs it can apply, then the
// learned IPs that are still refused, newest first. Learned IPs that are
// no longer refused are forgotten.
func (s *Server) gateDenyList(now time.Time) []gate.Deny {
	var out []gate.Deny
	for _, b := range s.svc.GateZlines() {
		if len(out) == maxGateDeny {
			return out
		}
		out = append(out, gate.Deny{Net: b.Net.String(), Until: unixMs(b.Until)})
	}
	type entry struct {
		ip    netip.Addr
		at    time.Time
		until time.Time
	}
	var es []entry
	for ip, at := range s.gateDeny.learned {
		a, err := netip.ParseAddr(ip)
		until, ok := s.refusedUntil(ip, now)
		if err != nil || a.Zone() != "" || !ok {
			delete(s.gateDeny.learned, ip)
			continue
		}
		es = append(es, entry{a.Unmap(), at, until})
	}
	slices.SortFunc(es, func(x, y entry) int {
		if c := y.at.Compare(x.at); c != 0 {
			return c
		}
		return x.ip.Compare(y.ip)
	})
	for _, e := range es {
		if len(out) == maxGateDeny {
			break
		}
		out = append(out, gate.Deny{Net: netip.PrefixFrom(e.ip, e.ip.BitLen()).String(), Until: unixMs(e.until)})
	}
	return out
}

// refusedUntil reports whether a new connection from ip is refused now
// (a ZLINE, or the connect throttle), and until when (zero: no end).
func (s *Server) refusedUntil(ip string, now time.Time) (time.Time, bool) {
	if z := s.svc.ZlineFor(ip); z != nil {
		return s.svc.BanEnd(z), true
	}
	if until, ok := s.throttleUntil(ip, now); ok {
		return until, true
	}
	return time.Time{}, false
}

// throttleUntil reports whether the connect throttle refuses the next
// connection from ip, and until when.
func (s *Server) throttleUntil(ip string, now time.Time) (time.Time, bool) {
	n, per, ok := s.cfg.Limits.ConnectLimit()
	if !ok || s.throttleExempt(ip) {
		return time.Time{}, false
	}
	h := s.throttle.hits[ip]
	if h == nil || h.n < n || now.Sub(h.start) >= per {
		return time.Time{}, false
	}
	return h.start.Add(per), true
}

// gateRefused runs on the loop for a client the gate refused (closed
// before TLS): it does what newClient does for a Z-lined or throttled
// client, so the logs, notices and throttle strikes are the same.
func (s *Server) gateRefused(ip string) {
	now := time.Now()
	if z := s.svc.ZlineFor(ip); z != nil {
		s.svc.LogBanHit(z.Kind, z.Mask, ip, z.SetBy, z.Reason)
		s.sno('x', "Z-line %s stopped a connection from %s", z.Mask, ip)
		return
	}
	if _, ok := s.throttleUntil(ip, now); ok {
		s.throttleCount(ip, now)
		return
	}
	s.gateDenyChanged() // the gate's list is out of date
}

// gateRefusedIP is the IP of a client the gate refused, written as
// newClient writes it.
func gateRefusedIP(r *gate.Refused) string {
	host, _, err := net.SplitHostPort(r.Info.Remote)
	if err != nil {
		return r.Info.Remote
	}
	return host
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return max(t.UnixMilli(), 1)
}
