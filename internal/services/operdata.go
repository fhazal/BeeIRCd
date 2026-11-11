package services

import (
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Oper data kept in memory: checked on every connect, join and nick.

func (s *Services) loadOperData() error {
	akills, err := s.store.Akills()
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
	fn, err := s.store.Forbids(store.ForbidNick)
	if err != nil {
		return err
	}
	fc, err := s.store.Forbids(store.ForbidChan)
	if err != nil {
		return err
	}
	s.akills, s.news, s.sessionExcepts = akills, news, ex
	s.forbidNicks = make(map[string]store.Forbid)
	for _, f := range fn {
		s.forbidNicks[irc.Fold(f.Name)] = f
	}
	s.forbidChans = make(map[string]store.Forbid)
	for _, f := range fc {
		s.forbidChans[irc.Fold(f.Name)] = f
	}
	return nil
}

// reloadOperData reloads after a change; failures go to the log.
func (s *Services) reloadOperData() {
	if err := s.loadOperData(); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// akillFor returns the AKILL matching ident at host/ip, or nil.
func (s *Services) akillFor(ident string, hosts ...string) *store.Akill {
	now := time.Now()
	for i := range s.akills {
		k := &s.akills[i]
		if !k.Expires.IsZero() && now.After(k.Expires) {
			continue
		}
		for _, h := range hosts {
			if h != "" && irc.Match(k.Mask, ident+"@"+h) {
				return k
			}
		}
	}
	return nil
}

// sessionLimit is how many connections ip may have.
func (s *Services) sessionLimit(ip string) int {
	for _, e := range s.sessionExcepts {
		if irc.Match(e.Mask, ip) {
			return e.Max
		}
	}
	return s.cfg.Limits.SessionLimit
}

// expireAkills drops AKILLs that ran out.
func (s *Services) expireAkills(now time.Time) {
	gone, err := s.store.PurgeAkills(now)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	if len(gone) > 0 {
		s.reloadOperData()
		for _, m := range gone {
			s.snotice("AKILL on %s ran out", m)
		}
	}
}

func (s *Services) forbidden(kind, name string) (store.Forbid, bool) {
	m := s.forbidNicks
	if kind == store.ForbidChan {
		m = s.forbidChans
	}
	f, ok := m[irc.Fold(name)]
	return f, ok
}
