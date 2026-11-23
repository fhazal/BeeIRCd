package services

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Network bans over server links. Every change to the list,
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

// matchesEveryone says whether a ban mask of kind would hit every user
// (the masks OperServ refuses with "That mask matches everyone").
func matchesEveryone(kind, mask string) bool {
	switch kind {
	case store.BanZline:
		return strings.Trim(mask, "*?.:") == "" || strings.HasSuffix(mask, "/0")
	case store.BanSNLine:
		return strings.Trim(mask, "*?") == ""
	}
	if irc.IsPlaceMask(mask) {
		_, v, _ := strings.Cut(mask[1:], ":")
		return strings.Trim(v, "*?") == ""
	}
	user, host, ok := strings.Cut(mask, "@")
	if !ok {
		return strings.Trim(mask, "*?.:") == ""
	}
	return (strings.Trim(host, "*?.:") == "" || strings.HasSuffix(host, "/0")) && strings.Trim(user, "*?~") == ""
}

// RemoteBan applies a ban change from another server. It reports whether
// anything changed and, for an added ban, whether it is stale: this very
// ban (or an older one) was removed here while the other server was split
// off, so the removal stands and the other server should drop it too.
func (s *Services) RemoteBan(b store.NetBan, add bool) (changed, stale bool) {
	if !linked(b.Kind) {
		return false, false
	}
	if m, ok := irc.ServerExtban(b.Mask); ok {
		if b.Kind == store.BanZline || b.Kind == store.BanSNLine {
			return false, false
		}
		b.Mask = m
	} else {
		b.Mask = strings.ToLower(b.Mask)
	}
	if add {
		if !b.Live(time.Now()) {
			return false, false
		}
		if matchesEveryone(b.Kind, b.Mask) {
			// Never right, from anyone: here OperServ refuses it too.
			s.log.Printf("refused a network ban from another server that matches everyone: %s %s (by %s)", b.Kind, b.Mask, b.SetBy)
			return false, false
		}
		have := false
		for _, o := range s.netbans {
			if o.Kind != b.Kind || o.Mask != b.Mask {
				continue
			}
			if o.Reason == b.Reason && o.Expires.Equal(b.Expires) {
				return false, false
			}
			if o.SetAt.After(b.SetAt) {
				return false, false // after a split both sides send theirs: the newest wins
			}
			have = true
		}
		if !have {
			// Removed here after it was set (the other server was split
			// off then): the removal is the newer change. A ban set again
			// later is newer still and comes through.
			set, removed, err := s.store.RemovedNetBan(b.Kind, b.Mask, time.Now())
			if err != nil {
				s.log.Printf("services database: %v", err)
			}
			if removed && !b.SetAt.After(set) {
				return false, true
			}
		}
		if err := s.store.AddNetBan(b); err != nil {
			s.log.Printf("services database: %v", err)
			return false, false
		}
	} else {
		gone, err := s.store.DelNetBan(b.Kind, b.Mask)
		if err != nil {
			s.log.Printf("services database: %v", err)
		}
		if !gone {
			return false, false
		}
	}
	s.reloadOperData()
	return true, false
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
