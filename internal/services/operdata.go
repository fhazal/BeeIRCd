package services

import (
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Oper data kept in memory: checked on every connect, join and nick.

func (s *Services) loadOperData() error {
	bans, err := s.store.NetBans("")
	if err != nil {
		return err
	}
	news, err := s.store.AllNews()
	if err != nil {
		return err
	}
	ex, err := s.store.SessionExcepts()
	if err != nil {
		return err
	}
	forbids := make(map[string][]store.Forbid)
	for _, kind := range store.ForbidKinds {
		list, err := s.store.Forbids(kind)
		if err != nil {
			return err
		}
		forbids[kind] = list
	}
	s.netbans, s.news, s.sessionExcepts, s.forbids = bans, news, ex, forbids
	return nil
}

// reloadOperData reloads after a change; failures go to the log.
func (s *Services) reloadOperData() {
	if err := s.loadOperData(); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// sessionLimit is how many connections ip may have.
func (s *Services) sessionLimit(ip string) int {
	now := time.Now()
	for _, e := range s.sessionExcepts {
		if (e.Expires.IsZero() || now.Before(e.Expires)) && irc.MatchHost(e.Mask, ip) {
			return e.Max
		}
	}
	return s.cfg.Limits.SessionLimit
}

// expireBans drops network bans, forbids and session exceptions that ran
// out.
func (s *Services) expireBans(now time.Time) {
	gone, err := s.store.PurgeExpired(now)
	if err != nil {
		s.log.Printf("services database: %v", err)
	}
	if len(gone) > 0 {
		s.reloadOperData()
		for _, e := range gone {
			s.snotice("%s ran out", expiredWords(e))
			s.logTo(s.operServ, anopeExpired(e))
		}
	}
}

// forbidden returns the first forbid of kind that matches name (names may
// be patterns with * and ?).
func (s *Services) forbidden(kind, name string) (store.Forbid, bool) {
	now := time.Now()
	for _, f := range s.forbids[kind] {
		if (f.Expires.IsZero() || now.Before(f.Expires)) && irc.Match(f.Name, name) {
			return f, true
		}
	}
	return store.Forbid{}, false
}

// registerForbidden reports why name (a nick or channel) can't be
// registered, or "".
func (s *Services) registerForbidden(name string) string {
	kind := store.ForbidNick
	if irc.IsChannel(name) {
		kind = store.ForbidChan
	}
	if f, ok := s.forbidden(kind, name); ok {
		return name + " is forbidden by the network (" + f.Reason + ") and can't be registered."
	}
	if f, ok := s.forbidden(store.ForbidRegister, name); ok {
		return name + " can't be registered here (" + f.Reason + ")."
	}
	return ""
}

// emailForbidden reports why email can't be used, or "".
func (s *Services) emailForbidden(email string) string {
	if f, ok := s.forbidden(store.ForbidEmail, email); ok {
		return "That email can't be used here (" + f.Reason + ")."
	}
	return ""
}
