package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// allFlags are the oper flags, in the order they are shown.
const allFlags = "acghks"

var flagNames = map[byte]string{
	'a': "admin",
	'c': "links",
	'g': "global",
	'h': "hosts",
	'k': "kill",
	's': "services",
}

// can reports whether c is an oper with flag f.
func (c *Client) can(f byte) bool { return strings.IndexByte(c.operFlags, f) >= 0 }

// isOper reports whether c is an oper.
func (c *Client) isOper() bool { return c.operFlags != "" }

// cleanFlags keeps known flags once each, in the usual order. bad is the
// first unknown letter, or 0.
func cleanFlags(in string) (string, byte) {
	var b strings.Builder
	for i := 0; i < len(allFlags); i++ {
		if strings.IndexByte(in, allFlags[i]) >= 0 {
			b.WriteByte(allFlags[i])
		}
	}
	for i := 0; i < len(in); i++ {
		if strings.IndexByte(allFlags, in[i]) < 0 {
			return b.String(), in[i]
		}
	}
	return b.String(), 0
}

func describeFlags(flags string) string {
	var parts []string
	for i := 0; i < len(flags); i++ {
		parts = append(parts, flagNames[flags[i]])
	}
	return flags + " (" + strings.Join(parts, ", ") + ")"
}

// isAdminAccount reports whether a is the admin named in beeircd.conf.
func (s *Server) isAdminAccount(a *store.Account) bool {
	name := s.cfg.Services.Admin
	if name == "" || a == nil {
		return false
	}
	if irc.EqualFold(a.Name, name) {
		return true
	}
	owner, err := s.store.AccountByNick(name)
	return err == nil && owner != nil && owner.ID == a.ID
}

// operEntry returns a's oper entry. The admin from beeircd.conf always has
// every flag, from any host, with the password from beeircd.conf.
func (s *Server) operEntry(a *store.Account) (*store.Oper, error) {
	if s.isAdminAccount(a) {
		return &store.Oper{AccountID: a.ID, Account: a.Name, Flags: allFlags, Title: "Server Admin",
			Hosts: []string{"*@*"}, PassHash: s.cfg.Services.AdminPassword}, nil
	}
	return s.store.OperByAccount(a.ID)
}

// operHostOK reports whether u connects from a host on the oper entry.
func operHostOK(o *store.Oper, u *state.User) bool {
	for _, m := range o.Hosts {
		for _, h := range []string{u.RealHost, u.IP, u.Host} {
			if h != "" && irc.Match(m, u.Ident+"@"+h) {
				return true
			}
		}
	}
	return false
}

// operLockKey keeps wrong oper passwords apart from wrong NickServ ones.
func operLockKey(id int64) int64 { return -id }

func (s *Server) operUp(c *Client, o *store.Oper) {
	u := c.user
	was := c.operFlags
	c.operFlags, c.operTitle, c.operAccount = o.Flags, o.Title, o.AccountID
	if u.Modes['o'] {
		if was != o.Flags {
			s.notice(c, "*** Your oper flags are now "+describeFlags(o.Flags))
		}
		return
	}
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(true, "os")})
	s.reply(c, irc.RplYoureOper, "You are now an IRC operator")
	s.notice(c, "*** Oper flags: "+describeFlags(o.Flags))
	s.snotice("%s (%s@%s) is now an operator as %s: %s", u.Nick, u.Ident, u.RealHost, o.Account, o.Flags)
}

// deoper takes oper status away. why "" means quietly.
func (s *Server) deoper(c *Client, why string) {
	u := c.user
	if u == nil || !c.isOper() && !u.Modes['o'] {
		return
	}
	c.operFlags, c.operTitle, c.operAccount = "", "", 0
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(false, "os")})
	if why != "" {
		s.notice(c, "*** "+why)
	}
	s.snotice("%s is no longer an operator", u.Nick)
}

// recheckOpers runs after the oper entry of account id changed: everyone
// opered with it gets the new flags, or loses oper when the entry is gone
// or no longer covers their host.
func (s *Server) recheckOpers(id int64) {
	var o *store.Oper
	if a, err := s.store.AccountByID(id); err == nil && a != nil {
		o, _ = s.operEntry(a)
	}
	for _, c := range s.clients {
		if c.user == nil || c.operAccount != id {
			continue
		}
		switch {
		case o == nil:
			s.deoper(c, "Your oper entry was removed, so you are no longer an operator.")
		case !operHostOK(o, c.user):
			s.deoper(c, "Your oper entry no longer covers your host, so you are no longer an operator.")
		default:
			s.operUp(c, o)
		}
	}
}

// snotice sends a server notice to opers with +s and writes it to the log.
func (s *Server) snotice(format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	s.log.Print(text)
	s.snoticeOnly(text)
}

// snoticeOnly sends a server notice without logging it (for things the
// log already has).
func (s *Server) snoticeOnly(text string) {
	for _, c := range s.clients {
		if c.user != nil && c.isOper() && c.user.Modes['s'] {
			s.notice(c, "*** Notice -- "+text)
		}
	}
}

// needFlag checks that c is an oper with flag f, answering as sv (or with a
// numeric when sv is nil).
func (s *Server) needFlag(c *Client, sv *service, f byte) bool {
	if c.can(f) {
		return true
	}
	if sv == nil {
		if c.isOper() {
			s.reply(c, irc.ErrNoPrivileges, fmt.Sprintf("Permission denied: you need oper flag %c", f))
		} else {
			s.reply(c, irc.ErrNoPrivileges, "Permission denied: you're not an IRC operator")
		}
		return false
	}
	s.svcReply(c, sv, fmt.Sprintf("You need oper flag %c (%s) for that.", f, flagNames[f]))
	return false
}

// /OPER <name> <oper password>: name is the oper's registered nick, the
// password is their own oper password (not the NickServ one).
func (s *Server) cmdOper(c *Client, m irc.Message) {
	u := c.user
	name, pw := m.Params[0], m.Params[1]
	a, err := s.store.AccountByNick(name)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	var o *store.Oper
	if a != nil {
		if o, err = s.operEntry(a); err != nil {
			s.log.Printf("services database: %v", err)
			return
		}
	}
	if o == nil || !operHostOK(o, u) {
		s.reply(c, irc.ErrNoOperHost, "No oper entry for you from this host")
		s.snotice("Failed /oper as %s from %s (%s@%s): no matching oper entry", name, u.Nick, u.Ident, u.RealHost)
		return
	}
	if o.PassHash == "" {
		s.firstOperPassword(c, a, o, pw)
		return
	}
	s.checkSecret(c, operLockKey(a.ID), o.PassHash, pw, func(r pwResult) {
		switch r {
		case pwOK:
			s.operUp(c, o)
		case pwLocked:
			s.reply(c, irc.ErrPasswdMismatch, "Too many wrong passwords. Try again in "+roundUp(s.lockedFor(operLockKey(a.ID))))
		case pwBusy:
			s.notice(c, "*** Still checking your last password. Try again in a moment.")
		default:
			s.reply(c, irc.ErrPasswdMismatch, "Password incorrect")
			s.snotice("Failed /oper as %s from %s (%s@%s): wrong password", o.Account, u.Nick, u.Ident, u.RealHost)
		}
	})
}

// firstOperPassword handles /oper for an entry without a password. Only
// the admin from beeircd.conf, identified to their account, may set it this
// way; everyone else gets theirs from AdminServ.
func (s *Server) firstOperPassword(c *Client, a *store.Account, o *store.Oper, pw string) {
	if !s.isAdminAccount(a) {
		s.reply(c, irc.ErrNoOperHost, "No oper password is set for "+o.Account+" yet")
		s.notice(c, "*** An admin sets it: /as oper "+o.Account+" set password <password>")
		return
	}
	if c.accountID != a.ID {
		s.reply(c, irc.ErrNoOperHost, "No oper password is set for "+o.Account+" yet")
		s.notice(c, "*** Identify to "+a.Name+" first; your first /oper then sets the admin oper password.")
		return
	}
	if len(pw) < minOperPassword {
		s.notice(c, fmt.Sprintf("*** Pick an oper password of at least %d characters, different from your NickServ one.", minOperPassword))
		return
	}
	s.hashPassword(c, pw, func(hash string) {
		if err := s.saveAdminPassword(hash); err != nil {
			s.log.Printf("saving the admin oper password: %v", err)
			s.notice(c, "*** Couldn't save the oper password to beeircd.conf: "+err.Error())
			return
		}
		o.PassHash = hash
		s.notice(c, "*** Your oper password is set and saved to beeircd.conf. Use it with /oper from now on.")
		s.operUp(c, o)
	})
}

const minOperPassword = 8

// saveAdminPassword writes the admin's oper password hash to beeircd.conf.
func (s *Server) saveAdminPassword(hash string) error {
	if s.cfg.Path == "" {
		return fmt.Errorf("the server wasn't started from a config file")
	}
	fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		d.Set("services", "admin_password", config.Quote(hash))
		return nil
	})
	if err != nil {
		return err
	}
	s.cfg.Services.AdminPassword = fresh.Services.AdminPassword
	return nil
}

// /KILL <nick> [reason]
func (s *Server) cmdKill(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'k') {
		return
	}
	t := s.net.UserByNick(m.Params[0])
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, m.Params[0], "No such nick/channel")
		return
	}
	if t.Service {
		s.reply(c, irc.ErrCantKillServer, "You can't kill a service")
		return
	}
	reason := m.Param(1)
	if reason == "" {
		reason = "No reason"
	}
	s.killUser(t, c.user.Nick, reason)
}

// killUser disconnects u, as by is the oper or service doing it.
func (s *Server) killUser(u *state.User, by, reason string) {
	tc := s.clientOf(u)
	if tc == nil {
		return
	}
	reason = irc.Truncate(reason, s.cfg.Limits.KickLen)
	tc.send(irc.Message{Source: by, Command: "KILL", Params: []string{u.Nick, reason}, Trailing: true}.String())
	s.snotice("%s killed %s (%s@%s): %s", by, u.Nick, u.Ident, u.RealHost, reason)
	s.quit(tc, "Killed ("+by+" ("+reason+"))")
}

// /WALLOPS <text> goes to everyone with +w.
func (s *Server) cmdWallops(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'g') {
		return
	}
	line := irc.Message{Source: c.user.Prefix(), Command: "WALLOPS", Params: []string{m.Params[0]}, Trailing: true}.String()
	for _, oc := range s.clients {
		if oc.user != nil && oc.user.Modes['w'] {
			oc.send(line)
		}
	}
}

// Oper data kept in memory: checked on every connect, join and nick.

func (s *Server) loadOperData() error {
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
func (s *Server) reloadOperData() {
	if err := s.loadOperData(); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// akillFor returns the AKILL matching ident at host/ip, or nil.
func (s *Server) akillFor(ident string, hosts ...string) *store.Akill {
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
func (s *Server) sessionLimit(ip string) int {
	for _, e := range s.sessionExcepts {
		if irc.Match(e.Mask, ip) {
			return e.Max
		}
	}
	return s.cfg.Limits.SessionLimit
}

// sessions counts the connections from ip.
func (s *Server) sessions(ip string) int {
	n := 0
	for _, c := range s.clients {
		if c.ip == ip && !c.closed {
			n++
		}
	}
	return n
}

// expireAkills drops AKILLs that ran out.
func (s *Server) expireAkills(now time.Time) {
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

// sendNews shows the news lines after the MOTD.
func (s *Server) sendNews(c *Client) {
	for _, n := range s.news {
		s.svcReply(c, s.global, "[News "+n.SetAt.UTC().Format("2 Jan")+"] "+n.Text)
	}
}

func (s *Server) forbidden(kind, name string) (store.Forbid, bool) {
	m := s.forbidNicks
	if kind == store.ForbidChan {
		m = s.forbidChans
	}
	f, ok := m[irc.Fold(name)]
	return f, ok
}
