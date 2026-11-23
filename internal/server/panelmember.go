package server

import (
	"sort"
	"strings"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

// Hive Chat h1: members. With [web] members on, anyone with a registered
// nick signs in to the Hive Panel with their NickServ password and gets
// the member pages only (My nick, My channels; Chat comes in h2). Opers
// still sign in with their oper password for the admin pages.

// memberSeal is a member session's seal: it changes with the NickServ
// password, so a new password ends old sessions (like passSeal for opers).
func memberSeal(a *store.Account) string { return passSeal("member:" + a.PassHash) }

// panelMember finds the member behind req (As + Seal of a member session).
// ok is false when req isn't a member session; deny is set when it is one
// that may not go on (members turned off, password changed, suspended).
func (s *Server) panelMember(req control.Request) (po *control.PanelOper, deny *control.Response, ok bool) {
	if req.As == "" || req.Seal == "" || s.svc.Off() {
		return nil, nil, false
	}
	a, err := s.store.AccountByNick(req.As)
	if err != nil || a == nil || req.Seal != memberSeal(a) {
		return nil, nil, false
	}
	signin := &control.Response{Error: "Please sign in again.", Denied: "signin"}
	switch {
	case !s.cfg.Web.Members:
		signin.Error = "Member sign-in is off on this server."
		return nil, signin, true
	case a.Suspended(s.svc.KeepNow()):
		signin.Error = a.Name + " is suspended."
		return nil, signin, true
	}
	return s.memberOper(a, req.IP), nil, true
}

// memberOper is a member session for a. When a is an oper who could
// sign in from ip but has no oper password yet, NoOperPass says so: the
// owner's first /oper on IRC sets one (firstOperPassword), otherwise
// beeircd.conf or an admin does. Not for opers who use
// a TLS certificate alone or an outside login server, where it doesn't.
func (s *Server) memberOper(a *store.Account, ip string) *control.PanelOper {
	po := &control.PanelOper{Account: a.Name, Member: true, Seal: memberSeal(a), Chat: s.cfg.Web.ChatURL}
	if s.outsideOpers() {
		return po
	}
	if op, _, o, _ := s.panelOper(a.Name, ip); op != nil && o.PassHash == "" && len(o.CertFPs) == 0 {
		switch { // how they get one: see firstOperPassword
		case !op.Owner:
			po.NoOperPass = "oper" // an admin sets it (/as oper <nick> set password)
		case len(s.cfg.Services.AdminHosts) == 0:
			po.NoOperPass = "owner-conf" // admin_password in beeircd.conf
		default:
			po.NoOperPass = "owner" // their first /oper from admin_hosts
		}
	}
	return po
}

// memberOffMsg answers a right NickServ password while Members is off.
const memberOffMsg = "Member sign-in is off on this server. Opers: sign in with your oper password."

// memberLogin checks a NickServ password for the Hive Panel. It counts
// wrong tries on the same lock as IDENTIFY on IRC.
func (s *Server) memberLogin(a *store.Account, nick, pw, ip string, done func(control.Response)) {
	wrong := control.Response{Error: "Wrong nick or password.", Denied: "signin"}
	if s.cfg.Web.Members && a.Suspended(s.svc.KeepNow()) { // with Members off nothing is told without the password
		done(control.Response{Error: a.Name + " is suspended.", Denied: "signin"})
		return
	}
	s.svc.CheckLogin(new(bool), func() bool { return false }, a, ip, pw, func(res services.PwResult) {
		switch res {
		case services.PwOK:
			if !s.cfg.Web.Members {
				s.panelEvent(a.Name, ip, "could not sign in as a member: member sign-in is off")
				done(control.Response{Error: memberOffMsg, Denied: "signin"})
				return
			}
			s.panelEvent(a.Name, ip, "signed in as a member")
			done(control.Response{Oper: s.memberOper(a, ip)})
		case services.PwLocked:
			done(control.Response{Error: "Too many wrong passwords for this nick. Try again in " + services.RoundUp(s.lockedFor(a.ID, ip)) + ".", Denied: "signin"})
		default:
			if !s.cfg.Web.Members { // as before Members: a wrong panel password, told to opers
				s.panelEvent(nick, ip, "could not sign in: wrong password")
				s.sno('a', "Hive Panel: wrong password for %s from %s", nick, ip)
			} else {
				s.panelEvent(nick, ip, "could not sign in as a member: wrong password")
			}
			done(wrong)
		}
	})
}

// panelMyNick is My nick and My channels for po's account.
func (s *Server) panelMyNick(po *control.PanelOper) *control.Response {
	a, err := s.store.AccountByNick(po.Account)
	if err != nil || a == nil {
		return &control.Response{Error: "Your account no longer exists.", Denied: "signin"}
	}
	nicks, _ := s.store.Nicks(a)
	kill := "off"
	if a.Enforce {
		switch a.Protect {
		case store.KillQuick:
			kill = "quick"
		case store.KillImmed:
			kill = "immed"
		default:
			kill = "on"
		}
	}
	my := &control.MyNick{Name: a.Name, Registered: a.Registered, Email: maskEmail(a.Email), Nicks: nicks, Kill: kill,
		HideEmail: a.HideEmail, HideStatus: a.HideStatus, AutoOp: a.AutoOp, Private: a.Private, Greet: s.store.Greet(a.ID),
		Channels: []control.MyChannel{}, Metadata: s.store.AccountMetadata(a.ID), MetaOn: s.cfg.Server.IRCv3Drafts}
	chans, err := s.store.Channels()
	if err != nil {
		s.log.Printf("services database: %v", err)
		return &control.Response{Error: "Something went wrong on the server."}
	}
	for _, reg := range chans {
		lvl, flags := s.store.ChanAccess(reg, a.ID)
		if lvl == store.LevelNone {
			continue
		}
		mc := control.MyChannel{Name: reg.Name, Level: store.LevelName(lvl)}
		if flags != "" {
			mc.Level = "+" + flags
		}
		if ch := s.net.Channel(reg.Name); ch != nil {
			mc.Users = len(ch.Members)
		}
		my.Channels = append(my.Channels, mc)
	}
	sort.Slice(my.Channels, func(i, j int) bool {
		return strings.ToLower(my.Channels[i].Name) < strings.ToLower(my.Channels[j].Name)
	})
	return &control.Response{MyNick: my}
}

// panelMySet runs NickServ SET for po's own account.
func (s *Server) panelMySet(po *control.PanelOper, ip, option, value string) *control.Response {
	a, err := s.store.AccountByNick(po.Account)
	if err != nil || a == nil {
		return &control.Response{Error: "Your account no longer exists.", Denied: "signin"}
	}
	args := strings.Fields(value)
	if strings.EqualFold(option, "GREET") {
		args = []string{strings.TrimSpace(value)} // the whole text, spaces and all
	}
	changed, lines := s.svc.WebSet(a.ID, ip, option, args)
	text := strings.Join(lines, " ")
	if !changed {
		return &control.Response{Error: text}
	}
	return &control.Response{Done: &control.Done{Text: text}}
}

// maskEmail shows the first letter and the domain: b•••@example.org.
func maskEmail(e string) string {
	user, domain, ok := strings.Cut(e, "@")
	if !ok || user == "" {
		return ""
	}
	return user[:1] + "•••@" + domain
}
