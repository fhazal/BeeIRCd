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
const allFlags = "abcghks"

var flagNames = map[byte]string{
	'a': "admin",
	'b': "bots",
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
		o := &store.Oper{AccountID: a.ID, Account: a.Name, Class: "netadmin", Flags: allFlags,
			Vhost: s.cfg.Services.AdminVhost, Hosts: s.adminHosts(), PassHash: s.cfg.Services.AdminPassword}
		if !s.svc.Off() {
			// The services server's admin owns the network.
			o.Reach = store.ReachNetwork
		}
		// Their own entry, when they have one, still says how they look:
		// title, WHOIS lines, and the vhost when beeircd.conf names none.
		if own, err := s.store.OperByAccount(a.ID); err == nil && own != nil {
			o.Title, o.Swhois = own.Title, own.Swhois
			if o.Vhost == "" {
				o.Vhost = own.Vhost
			}
		}
		return o, nil
	}
	return s.store.OperByAccount(a.ID)
}

// operLockKey keeps wrong oper passwords apart from wrong NickServ ones.
func operLockKey(id int64) int64 { return -id }

func (s *Server) operUp(c *Client, o *store.Oper) {
	s.operUpView(c, o, s.viewOf(o))
	if s.svc.Off() && o.AccountID == 0 {
		// The admin from beeircd.conf on a leaf: their own entry (title,
		// WHOIS lines, vhost) is on the services server, so ask for it.
		s.askOperLook(c, o.Account)
	}
}

// operUpView opers c up with v; o names the entry (its account). On a
// leaf v comes from the services server (o.AccountID 0: no account here).
func (s *Server) operUpView(c *Client, o *store.Oper, v operView) {
	u := c.user
	was, wasReach := c.operFlags, c.operReach
	c.operFlags, c.operTitle, c.operAccount, c.operSwhois, c.operClass = v.flags, v.title, o.AccountID, v.swhois, v.class
	c.operReach = v.reach
	u.OperClass, u.OperName = v.class, o.Account // ~O: and ~oper: bans
	s.opers[c] = true
	c.noLag.Store(true)
	s.showOperVhost(c, v.vhost)
	if u.Modes['o'] {
		if was != v.flags {
			s.notice(c, "*** Your oper flags are now "+describeFlags(v.flags))
		}
		if wasReach != v.reach {
			s.notice(c, "*** Your admin reach is now "+s.reachWords(v.reach))
		}
		// Also when only the title, the WHOIS lines or the reach changed.
		s.linkOperInfo(nil, u.UID, v.class, v.flags, s.ownLook(v.title, v.swhois, v.reach))
		return
	}
	c.snomask = snomaskDefault
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(true, "os")})
	s.linkOperInfo(nil, u.UID, v.class, v.flags, s.ownLook(v.title, v.swhois, v.reach))
	s.reply(c, irc.RplYoureOper, "You are now an IRC operator")
	s.replySnomask(c)
	if v.class != "" {
		s.notice(c, "*** Oper class "+v.class+": "+describeFlags(v.flags))
	} else {
		s.notice(c, "*** Oper flags: "+describeFlags(v.flags))
	}
	s.sno('o', "%s", snoOperUp(snoWho(u), s.operRank(v.class, v.title), o.Account))
	s.svc.SendOperNews(c.uid)
	s.operExtras(c)
}

// deoper takes oper status away. why "" means quietly.
func (s *Server) deoper(c *Client, why string) {
	u := c.user
	if u == nil || !c.isOper() && !u.Modes['o'] {
		return
	}
	c.operFlags, c.operTitle, c.operAccount, c.operSwhois, c.operClass = "", "", 0, nil, ""
	c.operReach = ""
	u.OperClass, u.OperName = "", ""
	c.operVia = nil
	c.snomask = ""
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
	s.linkOperInfo(nil, u.UID, "", "", "")
	if why != "" {
		s.notice(c, "*** "+why)
	}
	s.sno('o', "%s logged out of their server operator account.", snoWho(u))
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
	s.recheckRemoteOpers(id, "")
}

// sno sends a server notice with letter to the opers whose snomask has
// it, and writes it to the log.
func (s *Server) sno(letter byte, format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	s.log.Print(text)
	s.snoOnly(letter, text)
}

// snoOnly sends a server notice with letter without logging it (for
// things the log already has).
func (s *Server) snoOnly(letter byte, text string) {
	s.rpcLog(text, snoSubsystem(letter))
	s.snoticeOpers(letter, text)
}

// lnotice is a server notice about a server link (letter l), worded like
// InspIRCd 4's, with server names in bold (\x02). The log and the admin
// API get it without the bold, the admin API under its link subsystem.
func (s *Server) lnotice(format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	plain := strings.ReplaceAll(text, "\x02", "")
	s.log.Print(plain)
	s.rpcLog(plain, "link")
	s.snoticeOpers('l', text)
}

// snoticeOpers sends text to the opers with +s whose snomask has letter.
func (s *Server) snoticeOpers(letter byte, text string) {
	for c := range s.opers {
		if c.wantsSno(letter) {
			s.notice(c, "*** "+snoCategory(letter)+": "+text)
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
		s.stdReply(c, "FAIL", "OPER", "ALREADY_OPER", "You are already an IRC operator"+as+". To oper again, de-oper first: /mode "+u.Nick+" -o", "")
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
	if a == nil && s.svc.Off() {
		s.operRelayTo(c, name, pw) // the entries are on the services server
		return
	}
	if a != nil && o == nil {
		if o, err = s.operEntry(a); err != nil {
			s.log.Printf("services database: %v", err)
			return
		}
	}
	if a == nil && s.cfg.Services.Admin != "" && irc.EqualFold(name, s.cfg.Services.Admin) {
		// A fresh install: the admin from beeircd.conf has no account yet,
		// and "from this host" sent people to check their hosts.
		s.reply(c, irc.ErrNoOperHost, "No account "+s.cfg.Services.Admin+" yet: register it with NickServ (and log in), then /oper again")
		s.operFail('o', u, name, "no account with that name exists")
		return
	}
	if o == nil || !operHostOK(o, u) {
		s.reply(c, irc.ErrNoOperHost, "No oper entry for you from this host")
		if o == nil {
			s.operFail('o', u, name, "no account with that name exists")
		} else {
			s.operFail('o', u, o.Account, "they are connecting from the wrong user@host")
		}
		return
	}
	if o.Disabled != "" {
		s.reply(c, irc.ErrNoOperHost, "Your oper entry is disabled: "+o.Disabled)
		s.operFail('o', u, o.Account, "their oper account is disabled")
		return
	}
	if len(o.CertFPs) > 0 {
		if !certMatches(o.CertFPs, u.CertFP) {
			s.reply(c, irc.ErrNoOperHost, "No oper entry for you from this host")
			s.operFail('o', u, o.Account, "they are using the wrong TLS client certificate")
			return
		}
		if o.PassHash == "" { // the certificate alone, like UnrealIRCd's certfp blocks
			s.operUp(c, o)
			return
		}
	}
	if o.PassHash == "" && !s.outsideOpers() {
		s.firstOperPassword(c, a, o, pw)
		return
	}
	s.svc.CheckOper(&c.pwBusy, func() bool { return c.closed }, operLockKey(a.ID), c.ip, a.Name, o.PassHash, pw, func(r services.PwResult) {
		switch r {
		case services.PwOK:
			s.operUp(c, o)
		case services.PwLocked:
			s.reply(c, irc.ErrPasswdMismatch, "Too many wrong passwords. Try again in "+services.RoundUp(s.lockedFor(operLockKey(a.ID), c.ip)))
		case services.PwBusy:
			s.stdReply(c, "FAIL", "OPER", "TRY_AGAIN", "Still checking your last password. Try again in a moment.", "")
		case services.PwDown:
			s.reply(c, irc.ErrPasswdMismatch, "The login server can't be reached right now. Try again in a moment.")
			s.operFail('o', u, o.Account, "the login server can't be reached")
		default:
			s.reply(c, irc.ErrPasswdMismatch, s.cfg.Messages.PasswordText())
			s.operFail('o', u, o.Account, "they specified the wrong password")
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
	// Without admin_hosts anyone could register the admin's nick first and
	// set the password: then it must be in the config.
	if len(s.cfg.Services.AdminHosts) == 0 {
		s.reply(c, irc.ErrNoOperHost, "No oper password is set for "+o.Account+" yet")
		s.notice(c, "*** Put the admin's oper password in beeircd.conf: [services] admin_password from `beeircd mkpasswd`, then rehash. "+
			"(With admin_hosts set, a first /oper from those hosts may set it instead.)")
		s.operFail('o', c.user, o.Account, "admin_password is empty and admin_hosts is not set")
		return
	}
	if s.accountID(c) != a.ID {
		s.reply(c, irc.ErrNoOperHost, "No oper password is set for "+o.Account+" yet")
		s.notice(c, "*** Identify to "+a.Name+" first; your first /oper then sets the admin oper password.")
		return
	}
	if len(pw) < minOperPassword {
		s.stdReply(c, "FAIL", "OPER", "WEAK_PASSWORD", fmt.Sprintf("Pick an oper password of at least %d characters, different from your NickServ one.", minOperPassword), "")
		return
	}
	s.hashPassword(c, pw, func(hash string) {
		if err := s.saveAdminPassword(hash); err != nil {
			s.log.Printf("saving the admin oper password: %v", err)
			s.stdReply(c, "FAIL", "OPER", "INTERNAL_ERROR", "Couldn't save the oper password to beeircd.conf. The server log says why.", "")
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
	if s.outranked(c, t) {
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
	reason = irc.Truncate(unwrapKill(reason), s.cfg.Limits.KickLen)
	if tc == nil {
		if s.linkOf(u.UID) != nil {
			s.svc.LogKill(u, by, reason)
			s.killRemote(u, by, reason)
		}
		return
	}
	s.svc.LogKill(u, by, reason)
	src, shown := by, s.killedBy(by)
	if shown != by {
		src = s.cfg.Server.Name // [messages] hide_killed_by: not the oper
	}
	tc.send(irc.Message{Source: src, Command: "KILL", Params: []string{u.Nick, reason}, Trailing: true}.String())
	s.sno('k', "Local kill by %s: %s (%s)", by, snoRealMask(u), reason)
	s.quit(tc, "Killed ("+shown+" ("+reason+"))")
}

// unwrapKill strips any "Killed (<by> (" ... "))" wrappers a KILL reason
// already carries (InspIRCd sends them on), so it is wrapped only once.
func unwrapKill(reason string) string {
	for strings.HasPrefix(reason, "Killed (") && strings.HasSuffix(reason, "))") {
		inner := reason[len("Killed (") : len(reason)-2]
		i := strings.Index(inner, " (")
		if i < 0 {
			break
		}
		reason = inner[i+2:]
	}
	return reason
}

// /WALLOPS <text> goes to everyone with +w, here and on every other
// server (":<uid> WALLOPS :<text>" on the links, as UnrealIRCd, InspIRCd
// and Solanum send it).
func (s *Server) cmdWallops(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'g') {
		return
	}
	s.localWallops(c.user.Prefix(), m.Params[0])
	s.linkBroadcast(nil, irc.Message{Source: c.uid, Command: "WALLOPS", Params: []string{m.Params[0]}, Trailing: true}.String())
}

// localWallops shows a WALLOPS from src to everyone here with +w.
func (s *Server) localWallops(src, text string) {
	line := irc.Message{Source: src, Command: "WALLOPS", Params: []string{text}, Trailing: true}.String()
	for _, oc := range s.clients {
		if oc.user != nil && oc.user.Modes['w'] {
			oc.send(line)
		}
	}
}

// /GLOBOPS <text> is oper chat: everyone with the g notice letter here gets
// "*** GLOBOPS: From <nick>: <text>", and the other servers get it as
// letter G.
func (s *Server) cmdGlobops(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	text := strings.Join(m.Params, " ")
	s.sno('g', "From %s: %s", c.user.Nick, text)
	s.linkBroadcast(nil, irc.Message{Source: c.uid, Command: "GLOBOPS", Params: []string{text}, Trailing: true}.String())
}

// sessions counts the connections from ip.
func (s *Server) sessions(ip string) int { return s.perIP[ip] }

// outsideOpers reports whether oper passwords are asked from the outside
// login source ([auth] opers).
func (s *Server) outsideOpers() bool { return s.cfg.Auth.On() && s.cfg.Auth.Opers }

// hashPassword hashes pw off the loop for c and calls done with the hash.
func (s *Server) hashPassword(c *Client, pw string, done func(hash string)) {
	if c.pwBusy {
		s.stdReply(c, "FAIL", "*", "TRY_AGAIN", "Still busy with your last password. Try again in a moment.", "")
		return
	}
	s.svc.HashSecret(&c.pwBusy, func() bool { return c.closed }, pw, func(h string, err error) {
		if err != nil {
			s.log.Printf("hashing a password: %v", err)
			s.stdReply(c, "FAIL", "*", "INTERNAL_ERROR", "Something went wrong on the server. Please try again in a moment.", "")
			return
		}
		done(h)
	})
}
