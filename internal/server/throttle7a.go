package server

// Step 7a connection safety: a per-IP connect throttle (UnrealIRCd
// anti-flood connect-flood, InspIRCd connectban) and caps on connections
// that haven't finished logging in. Both run before any DNS or DNSBL work.

import (
	"time"
)

const (
	// throttleStrikes refusals within throttleStrikeWindow earn a ZLINE
	// for throttleBanTime.
	throttleStrikes      = 3
	throttleStrikeWindow = 10 * time.Minute
	throttleBanTime      = 10 * time.Minute
	// throttleMaxIPs bounds the table; a flood from more IPs than this
	// just starts it over.
	throttleMaxIPs = 50000
)

type ipHits struct {
	start   time.Time   // the current counting window began
	n       int         // connections in it
	refused []time.Time // recent refusals, for the ZLINE
}

type connThrottle struct {
	hits      map[string]*ipHits
	unreg     map[string]int // unregistered connections per IP
	unregAll  int
	lastSweep time.Time
}

func newConnThrottle() connThrottle {
	return connThrottle{hits: make(map[string]*ipHits), unreg: make(map[string]int), lastSweep: time.Now()}
}

// throttleExempt: LAN and loopback IPs, ELINEs and session exceptions skip
// the throttle and the unregistered caps (the same rules as DNSBL).
func (s *Server) throttleExempt(ip string) bool {
	return s.throttleSkip(ip) || s.svc.DnsblExempt(ip)
}

// admitConn decides whether a new connection may go on; "" means yes, else
// the reason it is closed with.
func (s *Server) admitConn(c *Client, now time.Time) string {
	lim := s.cfg.Limits
	if !s.throttleExempt(c.ip) {
		if n, per, ok := lim.ConnectLimit(); ok {
			t := &s.throttle
			h := t.hits[c.ip]
			if h == nil {
				if len(t.hits) >= throttleMaxIPs {
					t.hits = make(map[string]*ipHits)
				}
				h = &ipHits{start: now}
				t.hits[c.ip] = h
			}
			if now.Sub(h.start) >= per {
				h.start, h.n = now, 0
			}
			h.n++
			if h.n > n {
				s.throttled(c.ip, h, n, per, now)
				return "Throttled: reconnecting too fast, try again later"
			}
		}
		if s.throttle.unreg[c.ip] >= lim.MaxUnregisteredPerIP {
			return "Too many connections from your IP still logging in"
		}
		if s.throttle.unregAll >= lim.MaxUnregistered {
			s.snoticeEvery("unreg-full", time.Minute, "%d connections are logging in at once (max_unregistered); new ones are refused", s.throttle.unregAll)
			return "The server is busy, try again in a moment"
		}
	}
	c.unregCounted = true
	s.throttle.unreg[c.ip]++
	s.throttle.unregAll++
	return ""
}

// throttled notes a refusal and bans an IP that keeps at it.
func (s *Server) throttled(ip string, h *ipHits, n int, per time.Duration, now time.Time) {
	keep := h.refused[:0]
	for _, t := range h.refused {
		if now.Sub(t) < throttleStrikeWindow {
			keep = append(keep, t)
		}
	}
	h.refused = append(keep, now)
	s.safety().Throttled++
	if len(h.refused) == 1 {
		s.snotice("Throttled %s: more than %d connections in %d seconds", ip, n, int(per/time.Second))
	}
	if len(h.refused) >= throttleStrikes {
		h.refused = nil
		s.svc.ThrottleBan(ip, "Reconnecting too fast", throttleBanTime)
		s.safety().Zlined++
		s.svc.LogThrottle(ip, throttleStrikes, throttleStrikeWindow, throttleBanTime)
		s.snotice("ZLINE %s for %d minutes: throttled %d times in %d minutes", ip, int(throttleBanTime/time.Minute), throttleStrikes, int(throttleStrikeWindow/time.Minute))
	}
}

// doneUnregistered takes c off the unregistered count (it logged in or
// left).
func (s *Server) doneUnregistered(c *Client) {
	if !c.unregCounted {
		return
	}
	c.unregCounted = false
	t := &s.throttle
	if t.unreg[c.ip]--; t.unreg[c.ip] <= 0 {
		delete(t.unreg, c.ip)
	}
	t.unregAll--
}

// throttleTick forgets IPs that have been quiet for a while.
func (s *Server) throttleTick(now time.Time) {
	t := &s.throttle
	if now.Sub(t.lastSweep) < time.Minute {
		return
	}
	t.lastSweep = now
	_, per, _ := s.cfg.Limits.ConnectLimit()
	idle := max(per, throttleStrikeWindow)
	for ip, h := range t.hits {
		if now.Sub(h.start) >= idle {
			delete(t.hits, ip)
		}
	}
}

// snoticeEvery sends a server notice at most once per every for key, so a
// flood can't turn into a notice flood.
func (s *Server) snoticeEvery(key string, every time.Duration, format string, args ...any) {
	now := time.Now()
	if last, ok := s.noticeAt[key]; ok && now.Sub(last) < every {
		return
	}
	s.noticeAt[key] = now
	s.snotice(format, args...)
}
