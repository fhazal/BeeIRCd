package server

import (
	"fmt"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
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
		return &store.Oper{AccountID: a.ID, Account: a.Name, Class: "netadmin", Flags: allFlags,
			Vhost: s.cfg.Services.AdminVhost, Hosts: s.adminHosts(), PassHash: s.cfg.Services.AdminPassword}, nil
	}
	return s.store.OperByAccount(a.ID)
}

// operLockKey keeps wrong oper passwords apart from wrong NickServ ones.
func operLockKey(id int64) int64 { return -id }

func (s *Server) operUp(c *Client, o *store.Oper) {
	u := c.user
	v := s.viewOf(o)
	was := c.operFlags
	c.operFlags, c.operTitle, c.operAccount, c.operSwhois, c.operClass = v.flags, v.title, o.AccountID, v.swhois, v.class
	s.opers[c] = true
	c.noLag.Store(true)
	s.showOperVhost(c, v.vhost)
	if u.Modes['o'] {
		if was != v.flags {
			s.notice(c, "*** Your oper flags are now "+describeFlags(v.flags))
			s.linkOperInfo(nil, u.UID, v.class, v.flags)
		}
		return
	}
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(true, "os")})
	s.linkOperInfo(nil, u.UID, v.class, v.flags)
	s.reply(c, irc.RplYoureOper, "You are now an IRC operator")
	if v.class != "" {
		s.notice(c, "*** Oper class "+v.class+": "+describeFlags(v.flags))
	} else {
		s.notice(c, "*** Oper flags: "+describeFlags(v.flags))
	}
	if v.class != "" {
		s.snotice("%s (%s@%s) is now an operator as %s (class %s): %s", u.Nick, u.TrueIdent(), u.RealHost, o.Account, v.class, v.flags)
	} else {
		s.snotice("%s (%s@%s) is now an operator as %s: %s", u.Nick, u.TrueIdent(), u.RealHost, o.Account, v.flags)
	}
	s.svc.SendOperNews(c.uid)
}

// deoper takes oper status away. why "" means quietly.
func (s *Server) deoper(c *Client, why string) {
	u := c.user
	if u == nil || !c.isOper() && !u.Modes['o'] {
		return
	}
	c.operFlags, c.operTitle, c.operAccount, c.operSwhois, c.operClass = "", "", 0, nil, ""
	delete(s.opers, c)
	c.noLag.Store(false)
	s.showOperVhost(c, "")
	gone := "os"
	for _, m := range []byte(operOnlyModes) {
		if u.Modes[m] {
			gone += string(m)
		}
	}
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(false, gone)})
	s.linkOperInfo(nil, u.UID, "", "")
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
		case o.Disabled != "":
			s.deoper(c, "Your oper entry was disabled ("+o.Disabled+"), so you are no longer an operator.")
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
	for c := range s.opers {
		if c.user != nil && c.isOper() && c.user.Modes['s'] {
			s.notice(c, "*** Notice -- "+text)
		}
	}
}

// needFlag checks that c is an oper with flag f, answering as sv (or with a
// numeric when sv is nil).
func (s *Server) needFlag(c *Client, sv *state.User, f byte) bool {
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
	s.botReply(c, sv, fmt.Sprintf("You need oper flag %c (%s) for that.", f, flagNames[f]))
	return false
}

// /OPER <name> <oper password>: name is the oper's registered nick, the
// password is their own oper password (not the NickServ one).
func (s *Server) cmdOper(c *Client, m irc.Message) {
	u := c.user
	name, pw := m.Params[0], m.Params[1]
	// Like UnrealIRCd: already opered is answered before any password work,
	// so it costs nothing and never counts as a failed attempt.
	if u.Modes['o'] {
		as := ""
		if a, _ := s.store.AccountByID(c.operAccount); a != nil {
			as = "as " + a.Name
			if c.operClass != "" {
				as += ", class " + c.operClass
			}
			as = " (" + as + ")"
		}
		s.notice(c, "*** You are already an IRC operator"+as+". To oper again, de-oper first: /mode "+u.Nick+" -o")
		return
	}
	if s.links.noop {
		s.reply(c, irc.ErrNoOperHost, "No IRC operators on this server for now (NOOP)")
		return
	}
	a, err := s.store.AccountByNick(name)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	var o *store.Oper
	if a == nil {
		a, o = s.leafAdmin(u, name)
	}
	if a != nil && o == nil {
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
	if o.Disabled != "" {
		s.reply(c, irc.ErrNoOperHost, "Your oper entry is disabled: "+o.Disabled)
		s.snotice("Failed /oper as %s from %s (%s@%s): entry disabled", o.Account, u.Nick, u.TrueIdent(), u.RealHost)
		return
	}
	if o.PassHash == "" {
		s.firstOperPassword(c, a, o, pw)
		return
	}
	s.checkSecret(c, operLockKey(a.ID), o.PassHash, pw, func(r services.PwResult) {
		switch r {
		case services.PwOK:
			s.operUp(c, o)
		case services.PwLocked:
			s.reply(c, irc.ErrPasswdMismatch, "Too many wrong passwords. Try again in "+services.RoundUp(s.lockedFor(operLockKey(a.ID))))
		case services.PwBusy:
			s.notice(c, "*** Still checking your last password. Try again in a moment.")
		default:
			s.reply(c, irc.ErrPasswdMismatch, "Password incorrect")
			s.snotice("Failed /oper as %s from %s (%s@%s): wrong password", o.Account, u.Nick, u.Ident, u.RealHost)
		}
	})
}

// leafAdmin is the admin's oper entry on a server whose services are off
// (a leaf): accounts live on the services server, so there is none here
// to look up. The admin from beeircd.conf may still /oper with its
// admin_password, once logged in (on the services server) to the account
// of that name.
func (s *Server) leafAdmin(u *state.User, name string) (*store.Account, *store.Oper) {
	admin := s.cfg.Services.Admin
	if !s.svc.Off() || admin == "" || s.cfg.Services.AdminPassword == "" ||
		!strings.EqualFold(name, admin) || !strings.EqualFold(u.Account, admin) {
		return nil, nil
	}
	a := &store.Account{Name: admin}
	return a, &store.Oper{Account: admin, Class: "netadmin", Flags: allFlags,
		Vhost: s.cfg.Services.AdminVhost, Hosts: s.adminHosts(), PassHash: s.cfg.Services.AdminPassword}
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
	if s.accountID(c) != a.ID {
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
	reason = irc.Truncate(reason, s.cfg.Limits.KickLen)
	if tc == nil {
		if s.linkOf(u.UID) != nil {
			s.killRemote(u, by, reason)
		}
		return
	}
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

// sessions counts the connections from ip.
func (s *Server) sessions(ip string) int { return s.perIP[ip] }

// checkSecret checks an oper or SASL password off the loop for c.
func (s *Server) checkSecret(c *Client, key int64, hash, pw string, result func(services.PwResult)) {
	s.svc.CheckSecret(&c.pwBusy, func() bool { return c.closed }, key, hash, pw, result)
}

// hashPassword hashes pw off the loop for c and calls done with the hash.
func (s *Server) hashPassword(c *Client, pw string, done func(hash string)) {
	if c.pwBusy {
		s.notice(c, "*** Still busy with your last password. Try again in a moment.")
		return
	}
	s.svc.HashSecret(&c.pwBusy, func() bool { return c.closed }, pw, func(h string, err error) {
		if err != nil {
			s.log.Printf("hashing a password: %v", err)
			s.notice(c, "*** Something went wrong on the server. Please try again in a moment.")
			return
		}
		done(h)
	})
}
