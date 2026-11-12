package services

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// Network bans over server links (Stage 2c). Every change to the list,
// whatever made it (OperServ, /GLINE, DNSBL, the throttle, a ban from a
// link), reaches the server through OnBanChange; bans from links come in
// through RemoteBan. SNLINE stays on this server (UnrealIRCd has no such
// network ban).

// LinkedBanKinds are the kinds that travel between servers.
var LinkedBanKinds = []string{store.BanAkill, store.BanZline, store.BanShun, store.BanEline, store.BanSNLine}

func linked(kind string) bool {
	for _, k := range LinkedBanKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// OnBanChange sets what runs after a linked network ban went on (add) or
// off. Expiry is not a change: every server drops a ban at its time.
func (s *Services) OnBanChange(f func(b store.NetBan, add bool)) { s.banHook = f }

// OnOperData sets what runs after the bans, forbids and the rest were
// (re)loaded: the server sends nick bans (SQLINE) to its links.
func (s *Services) OnOperData(f func()) { s.operHook = f }

// NickForbids are the live FORBID NICK entries (= SQLINE).
func (s *Services) NickForbids() []store.Forbid {
	now := time.Now()
	var out []store.Forbid
	for _, f := range s.forbids[store.ForbidNick] {
		if f.Expires.IsZero() || now.Before(f.Expires) {
			out = append(out, f)
		}
	}
	return out
}

// LinkedBans are the bans in force that travel between servers.
func (s *Services) LinkedBans() []store.NetBan {
	now := time.Now()
	var out []store.NetBan
	for _, b := range s.netbans {
		if linked(b.Kind) && b.Live(now) {
			out = append(out, b)
		}
	}
	return out
}

// RemoteBan applies a ban change from another server. It reports whether
// anything changed.
func (s *Services) RemoteBan(b store.NetBan, add bool) bool {
	if !linked(b.Kind) {
		return false
	}
	b.Mask = strings.ToLower(b.Mask)
	if add {
		if !b.Live(time.Now()) {
			return false
		}
		for _, have := range s.netbans {
			if have.Kind == b.Kind && have.Mask == b.Mask && have.Reason == b.Reason && have.Expires.Equal(b.Expires) {
				return false
			}
			if have.Kind == b.Kind && have.Mask == b.Mask && have.SetAt.After(b.SetAt) {
				return false // after a split both sides send theirs: the newest wins (2e)
			}
		}
		if err := s.store.AddNetBan(b); err != nil {
			s.log.Printf("services database: %v", err)
			return false
		}
	} else {
		gone, err := s.store.DelNetBan(b.Kind, b.Mask)
		if err != nil {
			s.log.Printf("services database: %v", err)
		}
		if !gone {
			return false
		}
	}
	s.reloadOperData()
	return true
}

// banChanges calls the hook for what differs between two ban lists.
func (s *Services) banChanges(old, now []store.NetBan) {
	if s.banHook == nil {
		return
	}
	key := func(b store.NetBan) string { return b.Kind + " " + b.Mask }
	before := make(map[string]store.NetBan, len(old))
	for _, b := range old {
		before[key(b)] = b
	}
	t := time.Now()
	for _, b := range now {
		if !linked(b.Kind) {
			continue
		}
		o, had := before[key(b)]
		delete(before, key(b))
		if !had || o.Reason != b.Reason || !o.Expires.Equal(b.Expires) {
			if b.Live(t) {
				s.banHook(b, true)
			}
		}
	}
	for _, b := range before {
		if linked(b.Kind) && b.Live(t) {
			s.banHook(b, false)
		}
	}
}
