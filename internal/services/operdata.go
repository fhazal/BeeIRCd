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
	forbids := make(map[string][]store.Forbid)
	for _, kind := range store.ForbidKinds {
		list, err := s.store.Forbids(kind)
		if err != nil {
			return err
		}
		forbids[kind] = list
	}
	s.akills, s.news, s.sessionExcepts, s.forbids = akills, news, ex, forbids
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

// forbidden returns the first forbid of kind that matches name (names may
// be patterns with * and ?).
func (s *Services) forbidden(kind, name string) (store.Forbid, bool) {
	for _, f := range s.forbids[kind] {
		if irc.Match(f.Name, name) {
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
