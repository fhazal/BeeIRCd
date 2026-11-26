package server

// Logins over server links. On the server that runs the services, the
// services are the only ones who log people in: an account that arrives
// from a link (the account field of a UID line, SVSLOGIN, InspIRCd's
// METADATA accountname, UnrealIRCd's svid) is taken only when our services
// gave that person that account. Anything else is a link trying to log its
// users in to someone else's account (QA, 1.7.1): we keep them logged out,
// tell the link so, and tell the opers once.
//
// So we keep a record, by UID, of the logins our services gave to people
// on other servers. It lives through a netsplit, so a user who comes back
// in the next burst keeps their login, and goes when the user really quits
// or logs out. A server with services off keeps trusting what arrives.
//
// The records are kept in the services database too (table link_logins),
// so they live through a restart of this server as well: the other servers
// keep their users' UIDs, so after a cold restart the next burst matches.
// Changes are only noted in memory (loginsDirty) and written, all of them
// in one transaction, from the link tick (flushLogins) and when the server
// stops, so a netsplit of thousands is one write and no per-message
// database work is added. At a cold start nobody is on the network yet, so
// every loaded record counts as gone from that moment: the 24 hour wait
// and the expiry work as after a netsplit. After a hand-over the snapshot
// of the last server is used and the database is rewritten from it.

import (
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// loginGrant is one login our services gave to someone on another server.
type loginGrant struct {
	account string
	// id is the account's ID, so an account dropped and registered again
	// under the same name (by someone else) does not match. 0: not known.
	id int64
	// gone is when the user left in a netsplit, or when services logged
	// them in before they were on the network (SASL); zero while they are
	// on the network.
	gone time.Time
}

const (
	// loginGrantKeep is how long a login waits for its user to come back
	// (or to show up after SASL).
	loginGrantKeep = 24 * time.Hour
	// loginGrantCap: past this many records, the ones of people who are
	// not on the network go first.
	loginGrantCap = 100000
	// linkLoginNoticeEvery: at most one oper notice per link this often
	// about logins we ignored; the log has every one.
	linkLoginNoticeEvery = time.Minute
)

// grantLogin records that our services logged uid in to account. away: the
// user is not on the network (yet).
func (s *Server) grantLogin(uid, account string, away bool) {
	if s.links.logins == nil {
		s.links.logins = map[string]loginGrant{}
	}
	if _, ok := s.links.logins[uid]; !ok && len(s.links.logins) >= loginGrantCap {
		// Only people who are not on the network can fill it up: drop
		// their records.
		for k, g := range s.links.logins {
			if !g.gone.IsZero() {
				s.dropLogin(k)
			}
		}
	}
	g := loginGrant{account: account}
	if a, err := s.store.AccountByNick(account); err == nil && a != nil {
		g.id = a.ID
	}
	if away {
		g.gone = time.Now()
	}
	s.setLogin(uid, g)
}

// setLogin stores g as uid's record and notes it for the database.
func (s *Server) setLogin(uid string, g loginGrant) {
	if s.links.logins == nil {
		s.links.logins = map[string]loginGrant{}
	}
	s.links.logins[uid] = g
	s.noteLoginChange(uid)
}

// dropLogin deletes uid's record (if any) and notes it for the database.
func (s *Server) dropLogin(uid string) {
	if _, ok := s.links.logins[uid]; ok {
		delete(s.links.logins, uid)
		s.noteLoginChange(uid)
	}
}

// noteLoginChange says uid's record changed since the last flushLogins.
func (s *Server) noteLoginChange(uid string) {
	if s.links.loginsDirty == nil {
		s.links.loginsDirty = map[string]struct{}{}
	}
	s.links.loginsDirty[uid] = struct{}{}
}

// linkLoginOK says whether to take account for uid (nick) from l. When not,
// it tells l the account we know instead (truth, "" for none), logs it and
// tells the opers (once a minute per link).
func (s *Server) linkLoginOK(l *linkConn, uid, nick, account, truth string) bool {
	if s.svc.Off() || account == "" {
		return true // no services here: whoever runs them decides
	}
	if g, ok := s.links.logins[uid]; ok && irc.EqualFold(g.account, account) &&
		(g.gone.IsZero() || time.Since(g.gone) < loginGrantKeep) {
		// The account must still be there by that name (not dropped or
		// renamed since), and be the same account.
		if a, err := s.store.AccountByNick(account); err == nil && a != nil && irc.EqualFold(a.Name, account) &&
			(g.id == 0 || a.ID == g.id) {
			return true
		}
	}
	s.log.Printf("link %s: ignored a login to %s for %s (%s): our services did not log them in", l.peer.Name, account, nick, uid)
	key, now := "loginspoof:"+strings.ToLower(l.peer.Name), time.Now()
	if last, ok := s.noticeAt[key]; !ok || now.Sub(last) >= linkLoginNoticeEvery {
		s.noticeAt[key] = now
		s.snoOnly('l', fmt.Sprintf("Ignored a login to %s for %s from %s", account, nick, l.peer.Name))
	}
	for _, line := range l.codecOf().Encode(&link.Login{Source: s.cfg.Server.SID, UID: uid, Account: truth}) {
		l.send(line)
	}
	return false
}

// noteLogins keeps the login records up to date with ev (from emit; org is
// where ev came from, nil for this server).
func (s *Server) noteLogins(ev state.Event, org *linkOrigin) {
	if s.svc.Off() {
		return
	}
	switch e := ev.(type) {
	case *state.Login:
		switch {
		case s.ours(e.UID):
		case e.Account == "":
			s.dropLogin(e.UID)
		case org == nil || org.l == nil:
			s.grantLogin(e.UID, e.Account, false) // our services
		default:
			s.loginBack(e.UID, e.Account) // a link passing on our login
		}
	case *state.UserAdd:
		if e.User.Account != "" {
			s.loginBack(e.User.UID, e.User.Account)
		}
	case *state.UserQuit:
		g, ok := s.links.logins[e.UID]
		switch {
		case !ok:
		case s.netsplit != nil:
			// Gone in a split: kept for when they come back.
			g.gone = time.Now()
			s.setLogin(e.UID, g)
		default:
			s.dropLogin(e.UID)
		}
	}
}

// loginBack marks uid's login as on the network again (they came back
// after a split, or showed up after SASL). account was checked already.
func (s *Server) loginBack(uid, account string) {
	if g, ok := s.links.logins[uid]; ok && irc.EqualFold(g.account, account) && !g.gone.IsZero() {
		g.gone = time.Time{}
		s.setLogin(uid, g)
	}
}

// expireLogins drops, once a minute at most, the records of people who
// left long ago.
func (s *Server) expireLogins(now time.Time) {
	if len(s.links.logins) == 0 || now.Sub(s.links.loginsSwept) < time.Minute {
		return
	}
	s.links.loginsSwept = now
	for uid, g := range s.links.logins {
		switch {
		case g.gone.IsZero():
			if s.net.UserByUID(uid) == nil {
				// We missed how they left: give them the usual wait.
				g.gone = now
				s.setLogin(uid, g)
			}
		case now.Sub(g.gone) >= loginGrantKeep:
			s.dropLogin(uid)
		}
	}
}

// loginsRetryAfter: how long to wait before trying the database again after
// a failed write (the changes stay noted).
const loginsRetryAfter = time.Minute

// loadLogins reads the saved records at start (a cold start; a hand-over
// replaces them, see takeOver). Nobody is on the network yet, so a record
// that was on the network counts as gone from now on; ones past the wait
// are dropped.
func (s *Server) loadLogins(now time.Time) {
	if s.svc.Off() {
		return
	}
	all, err := s.store.LinkLogins()
	if err != nil {
		s.log.Printf("services database: link logins: %v", err)
		return
	}
	if s.links.logins == nil {
		s.links.logins = map[string]loginGrant{}
	}
	for _, l := range all {
		g := loginGrant{account: l.Account, id: l.AccountID, gone: l.Gone}
		switch {
		case !l.Gone.IsZero() && now.Sub(l.Gone) >= loginGrantKeep, len(s.links.logins) >= loginGrantCap:
			s.noteLoginChange(l.UID) // not in the map: deleted at the next flush
		case l.Gone.IsZero() || l.Gone.After(now):
			g.gone = now
			s.setLogin(l.UID, g) // changed: written back
		default:
			s.links.logins[l.UID] = g
		}
	}
}

// flushLogins writes the noted changes to the database in one transaction.
// It runs from the link tick (now) and at stop (force: no waiting after an
// earlier failure).
func (s *Server) flushLogins(now time.Time, force bool) {
	l := &s.links
	if len(l.loginsDirty) == 0 && !l.loginsAll {
		return
	}
	if s.svc.Off() {
		l.loginsDirty, l.loginsAll = nil, false
		return
	}
	if !force && now.Before(l.loginsRetry) {
		return
	}
	var save []store.LinkLogin
	var drop []string
	add := func(uid string, g loginGrant) {
		save = append(save, store.LinkLogin{UID: uid, Account: g.account, AccountID: g.id, Gone: g.gone})
	}
	if l.loginsAll {
		for uid, g := range l.logins {
			add(uid, g)
		}
	} else {
		for uid := range l.loginsDirty {
			if g, ok := l.logins[uid]; ok {
				add(uid, g)
			} else {
				drop = append(drop, uid)
			}
		}
	}
	if err := s.store.SaveLinkLogins(save, drop, l.loginsAll); err != nil {
		s.log.Printf("services database: link logins: %v", err)
		l.loginsRetry = now.Add(loginsRetryAfter)
		return
	}
	l.loginsDirty, l.loginsAll = nil, false
}
