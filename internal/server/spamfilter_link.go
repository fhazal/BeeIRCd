package server

import (
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/spamfilter"
)

// Spam filter rules over links: UnrealIRCd's TKL F, the same to BeeIRCd
// and UnrealIRCd 6 servers. Every change goes to every link, the burst
// sends the whole list, and each server checks its own people.

// spamItem writes rule f for the wire.
func (s *Server) spamItem(f spamfilter.Filter, add bool) *link.SpamFilter {
	it := &link.SpamFilter{Source: s.cfg.Server.SID, Add: add, Targets: f.Targets, Action: spamfilter.ActionLetter(f.Action),
		SetBy: orStr(f.SetBy, s.cfg.Server.Name), SetAt: f.SetAt.Unix(), BanTime: int64(f.BanTime / time.Second),
		Reason: f.Reason, MatchType: f.Type, Match: f.Match}
	if !f.Expires.IsZero() {
		it.Expires = f.Expires.Unix()
	}
	return it
}

// linkSpamChanged sends a rule change of ours to every link (services
// call it, see OnSpamChange).
func (s *Server) linkSpamChanged(f spamfilter.Filter, add bool) {
	if spamfilter.ActionLetter(f.Action) == 0 {
		return
	}
	it := s.spamItem(f, add)
	for _, o := range s.upLinks(nil) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
}

// spamBurst is the rule list for a new link.
func (s *Server) spamBurst(l *linkConn) []string {
	var out []string
	cd := l.codecOf()
	for _, f := range s.svc.SpamFilters() {
		if spamfilter.ActionLetter(f.Action) != 0 {
			out = append(out, cd.Encode(s.spamItem(f, true))...)
		}
	}
	return out
}

// remoteSpam is a TKL F from l: kept, used here and passed on.
func (s *Server) remoteSpam(l *linkConn, raw irc.Message, it *link.SpamFilter) {
	action := spamfilter.ActionOf(it.Action)
	if action == "" || (it.MatchType != spamfilter.Simple && it.MatchType != spamfilter.Regex) {
		for _, o := range s.upLinks(l) {
			o.send(raw.String()) // not ours to keep; servers further on may know it
		}
		return
	}
	f := spamfilter.Filter{Type: it.MatchType, Match: it.Match, Targets: it.Targets, Action: action,
		BanTime: time.Duration(it.BanTime) * time.Second, Reason: irc.Truncate(it.Reason, 4*spamfilter.MaxReason),
		SetBy: it.SetBy, SetAt: time.Unix(it.SetAt, 0)}
	if it.SetAt == 0 {
		f.SetAt = time.Now()
	}
	if it.Expires > 0 {
		f.Expires = time.Unix(it.Expires, 0)
	}
	if !s.svc.RemoteSpamFilter(f, it.Add) {
		return
	}
	for _, o := range s.upLinks(l) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
	if l.eos {
		how := "added"
		if !it.Add {
			how = "removed"
		}
		s.snotice("Spam filter %s %s by %s (from %s)", it.Match, how, it.SetBy, l.peer.Name)
	}
}
