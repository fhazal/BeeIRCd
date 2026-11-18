package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
)

// User modes B d D R T p H W z Z q (UnrealIRCd letters and
// rules), KNOCK, SILENCE, SETNAME/CHGNAME, SETHOST/SETIDENT/CHGHOST/
// CHGIDENT and the SA commands.

const (
	maxSilence  = 32 // SILENCE entries per person (005 SILENCE=32)
	knockLimit  = 3  // KNOCKs per person ...
	knockWindow = time.Minute
	maxRealname = 50
	maxIdentLen = 10
	maxVhostLen = 63
	saFlag      = 'a' // SAJOIN SAPART SANICK SAMODE SATOPIC, and the tools in opertools.go
	chgFlag     = 'h' // SETHOST SETIDENT CHGHOST CHGIDENT CHGNAME
)

// isCTCP is UnrealIRCd's IsACTCP: a CTCP that isn't ACTION or DCC.
func isCTCP(text string) bool {
	return strings.HasPrefix(text, "\x01") && !strings.HasPrefix(text, "\x01ACTION ") && !strings.HasPrefix(text, "\x01DCC ")
}

// refusesMessage says why t's user modes refuse a private message from c
// ("" = allowed), like UnrealIRCd's privdeaf, regonlymsg, noctcp and
// secureonlymsg. Opers get past +D, +T and +R.
func (s *Server) refusesMessage(c *Client, t *state.User, verb, text string) string {
	u := c.user
	if t == u || t.Service || u.Service {
		return ""
	}
	oper := c.isOper()
	switch {
	case t.HasMode('D') && !oper:
		return "User does not accept private messages"
	case t.HasMode('R') && u.Account == "" && !oper:
		return "You must identify to a registered nick to private message this user"
	case t.HasMode('T') && !oper && verb != "TAGMSG" && isCTCP(text):
		return "User does not accept CTCPs"
	case t.HasMode('Z') && !u.TLS && !oper:
		return "You must be connected via TLS to message this user"
	case u.HasMode('Z') && !t.TLS && !oper:
		return "Recipient is not connected via TLS and you are +Z"
	}
	return ""
}

// silenced reports whether t's SILENCE list holds a mask matching u.
func (s *Server) silenced(t *state.User, u *state.User) bool {
	tc := s.clientOf(t)
	if tc == nil || len(tc.silence) == 0 || u.Service {
		return false
	}
	for _, mask := range tc.silence {
		if irc.Match(mask, u.Nick+"!"+u.Ident+"@"+u.Host) || irc.Match(mask, u.Nick+"!"+u.TrueIdent()+"@"+u.RealHost) {
			return true
		}
	}
	return false
}

// silenceMask turns "nick", "nick!user", "user@host" into a full mask, as
// UnrealIRCd's pretty_mask does.
func silenceMask(m string) string {
	switch {
	case strings.Contains(m, "!") && strings.Contains(m, "@"):
	case strings.Contains(m, "!"):
		m += "@*"
	case strings.Contains(m, "@"):
		m = "*!" + m
	case strings.Contains(m, "."):
		m = "*!*@" + m
	default:
		m += "!*@*"
	}
	return irc.NormalizeMask(m)
}

// cmdSilence is SILENCE [+|-mask]: a server-side ignore for private
// messages, notices and invites (UnrealIRCd numerics 271/272/511).
func (s *Server) cmdSilence(c *Client, m irc.Message) {
	arg := m.Param(0)
	if arg == "" {
		for _, mask := range c.silence {
			s.reply(c, irc.RplSileList, mask)
		}
		s.reply(c, irc.RplEndOfSileList, "End of Silence List")
		return
	}
	action := arg[0]
	if action == '+' || action == '-' {
		arg = arg[1:]
	} else if !strings.ContainsAny(arg, "@.!*") && s.net.UserByNick(arg) == nil {
		s.reply(c, irc.ErrNoSuchNick, arg, "No such nick/channel")
		return
	} else {
		action = '+'
	}
	if arg == "" {
		return
	}
	mask := silenceMask(arg)
	if action == '-' {
		for i, e := range c.silence {
			if irc.EqualFold(e, mask) {
				c.silence = append(c.silence[:i:i], c.silence[i+1:]...)
				c.send(":" + c.user.Prefix() + " SILENCE -" + mask)
				return
			}
		}
		return
	}
	for _, e := range c.silence {
		if irc.Match(e, mask) {
			return // already covered
		}
	}
	if len(c.silence) >= maxSilence {
		s.reply(c, irc.ErrSileListFull, mask, "Your silence list is full")
		return
	}
	c.silence = append(c.silence, mask)
	c.send(":" + c.user.Prefix() + " SILENCE +" + mask)
}

// cmdKnock is KNOCK #chan [reason]: asks the ops of a channel you can't
// get into (+i, +k, or full +l) to let you in. +K forbids it, bans stop
// it, and it counts toward +f k.
func (s *Server) cmdKnock(c *Client, m irc.Message) {
	u := c.user
	name := m.Params[0]
	reason := m.Param(1)
	if reason == "" {
		reason = "no reason specified"
	}
	reason = irc.Truncate(reason, s.cfg.Limits.KickLen)
	ch := s.net.Channel(name)
	if ch == nil {
		s.reply(c, irc.ErrCannotKnock, "Cannot knock on "+name+" (Channel does not exist!)")
		return
	}
	refuse := func(why string) { s.reply(c, irc.ErrCannotKnock, "Cannot knock on "+ch.Name+" ("+why+")") }
	full := ch.HasMode('l') && len(ch.Members) >= ch.Limit
	switch {
	case ch.Member(u) != nil:
		refuse("You're already there!")
	case ch.HasMode('K'):
		refuse("No knocks are allowed! (+K)")
	case !ch.HasMode('i') && !ch.HasMode('k') && !full:
		refuse("Channel is not invite only!")
	case ch.IsBanned(u):
		refuse("You're banned!")
	case !c.can('g') && !s.knockAllowed(c):
		refuse("You are KNOCK flooding")
	default:
		text := "[Knock] by " + u.Prefix() + " (" + reason + ")"
		for _, mem := range ch.Members {
			if mem.AtLeast(state.RankOp) && !mem.User.Service {
				if tc := s.clientOf(mem.User); tc != nil {
					tc.send(":" + s.cfg.Server.Name + " NOTICE @" + ch.Name + " :" + text)
				}
			}
		}
		s.notice(c, "Knocked on "+ch.Name)
		s.floodEvent(c, ch, 'k')
	}
}

// knockAllowed keeps the KNOCK flood limit for c.
func (s *Server) knockAllowed(c *Client) bool {
	now := time.Now()
	kept := c.knocks[:0]
	for _, t := range c.knocks {
		if now.Sub(t) < knockWindow {
			kept = append(kept, t)
		}
	}
	c.knocks = kept
	if len(c.knocks) >= knockLimit {
		return false
	}
	c.knocks = append(c.knocks, now)
	return true
}

// cmdSetname is IRCv3 SETNAME: change your own real name.
func (s *Server) cmdSetname(c *Client, m irc.Message) {
	name := m.Params[0]
	if name == "" || len(name) > maxRealname {
		c.send(":" + s.cfg.Server.Name + " FAIL SETNAME INVALID_REALNAME :Real names may be at most 50 characters long")
		return
	}
	if !c.caps["setname"] {
		defer s.notice(c, "Your real name is now "+name)
	}
	s.emit(&state.RealnameChange{UID: c.uid, Realname: name})
}

// opTarget finds the person an oper command is about (not a service).
func (s *Server) opTarget(c *Client, nick string) *Client {
	t := s.net.UserByNick(nick)
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
		return nil
	}
	if t.Service {
		s.reply(c, irc.ErrNoPrivileges, "Cannot use this command on a network service")
		return nil
	}
	tc := s.clientOf(t)
	if tc == nil {
		s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
	}
	return tc
}

// cmdChgname is CHGNAME <nick> <realname> (opers with flag h).
func (s *Server) cmdChgname(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, chgFlag) {
		return
	}
	if len(m.Params) < 2 || m.Params[1] == "" {
		s.reply(c, irc.ErrNeedMoreParams, "CHGNAME", "Not enough parameters")
		return
	}
	if len(m.Params[1]) > maxRealname {
		s.stdReply(c, "FAIL", "CHGNAME", "INVALID_REALNAME", "Real names may be at most 50 characters long", "*** ChgName Error: Requested realname too long -- rejected.")
		return
	}
	t := s.chgTarget(c, m.Params[0])
	if t == nil {
		return
	}
	s.snotice("CHGNAME: %s changed the realname of %s to be %s", c.user.Nick, t.Nick, m.Params[1])
	s.emit(&state.RealnameChange{UID: t.UID, Realname: m.Params[1]})
}

// chgTarget finds the person CHGHOST, CHGIDENT or CHGNAME acts on, here or
// on another server (the change then goes out over the links); nil after
// replying when there is nobody to change.
func (s *Server) chgTarget(c *Client, nick string) *state.User {
	t := s.userByUIDOrNick(nick)
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
		return nil
	}
	if t.Service {
		s.reply(c, irc.ErrNoPrivileges, "Cannot use this command on a network service")
		return nil
	}
	return t
}

// chgHost sets someone's host or ident: through changeHost for people here
// (they get 396), or as a change the links carry to their own server.
func (s *Server) chgHost(t *state.User, host, ident string) {
	if tc := s.clientOf(t); tc != nil {
		s.changeHost(tc, host, ident)
		return
	}
	if host == "" {
		host = t.Host
	}
	if host == t.Host && (ident == "" || ident == t.Ident) {
		return
	}
	s.emit(&state.HostChange{UID: t.UID, Host: host, Ident: ident})
}

// validVhost is UnrealIRCd's rule for SETHOST/CHGHOST: a-z, A-Z, 0-9, '-'
// and '.', at most 63 characters.
func validVhost(h string) bool {
	return validHostname(h) && len(h) <= maxVhostLen && h[0] != '.' && h[0] != '-'
}

// validSetIdent is UnrealIRCd's rule for SETIDENT/CHGIDENT.
func validSetIdent(id string) bool {
	if id == "" || len(id) > maxIdentLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '.' || ch == '~' || ch == '_') {
			return false
		}
	}
	return true
}

// changeHost gives t a new displayed host and/or ident (chghost, 396).
func (s *Server) changeHost(t *Client, host, ident string) {
	u := t.user
	if host == "" {
		host = u.Host
	}
	if host == u.Host && (ident == "" || ident == u.Ident) {
		return
	}
	s.emit(&state.HostChange{UID: u.UID, Host: host, Ident: ident})
	s.reply(t, irc.RplHostHidden, host, "is now your displayed host")
}

func (s *Server) cmdSethost(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, chgFlag) {
		return
	}
	host := m.Params[0]
	if !validVhost(host) {
		s.stdReply(c, "FAIL", "SETHOST", "INVALID_HOST", "A host may only contain a-z, A-Z, 0-9, '-' and '.' (at most 63)", "*** /SetHost Error: A hostname may only contain a-z, A-Z, 0-9, '-' & '.' (at most 63).", host)
		return
	}
	if host == c.user.Host {
		s.stdReply(c, "FAIL", "SETHOST", "HOST_UNCHANGED", "That is already your host", "*** /SetHost Error: requested host is same as current host.", host)
		return
	}
	s.changeHost(c, host, "")
	s.notice(c, "Your nick!user@host-mask is now ("+c.user.Prefix()+")")
	s.snotice("%s used SETHOST to change their displayed host to %s", c.user.Nick, host)
}

func (s *Server) cmdSetident(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, chgFlag) {
		return
	}
	id := m.Params[0]
	if !validSetIdent(id) {
		s.stdReply(c, "FAIL", "SETIDENT", "INVALID_IDENT", "A username may only contain a-z, A-Z, 0-9, '-', '_', '~' and '.' (at most 10)", "*** /SETIDENT Error: A username may contain a-z, A-Z, 0-9, '-', '_', '~' & '.' (at most 10).", id)
		return
	}
	s.changeHost(c, "", id)
	s.notice(c, "Your nick!user@host-mask is now ("+c.user.Prefix()+")")
	s.snotice("%s used SETIDENT to change their username to %s", c.user.Nick, id)
}

func (s *Server) cmdChghost(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, chgFlag) {
		return
	}
	host := m.Params[1]
	if !validVhost(host) {
		s.stdReply(c, "FAIL", "CHGHOST", "INVALID_HOST", "A host may only contain a-z, A-Z, 0-9, '-' and '.' (at most 63)", "*** /ChgHost Error: A hostname may contain a-z, A-Z, 0-9, '-' & '.' - Please only use them", host)
		return
	}
	t := s.chgTarget(c, m.Params[0])
	if t == nil {
		return
	}
	s.chgHost(t, host, "")
	s.snotice("CHGHOST: %s changed the virtual hostname of %s (%s@%s) to be %s", c.user.Nick, t.Nick, t.TrueIdent(), t.RealHost, host)
}

func (s *Server) cmdChgident(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, chgFlag) {
		return
	}
	id := m.Params[1]
	if !validSetIdent(id) {
		s.stdReply(c, "FAIL", "CHGIDENT", "INVALID_IDENT", "A username may only contain a-z, A-Z, 0-9, '-', '_', '~' and '.' (at most 10)", "*** /ChgIdent Error: A ident may contain a-z, A-Z, 0-9, '-', '_', '~' & '.' - Please only use them", id)
		return
	}
	t := s.chgTarget(c, m.Params[0])
	if t == nil {
		return
	}
	s.chgHost(t, "", id)
	s.snotice("CHGIDENT: %s changed the username of %s (%s@%s) to be %s", c.user.Nick, t.Nick, t.TrueIdent(), t.RealHost, id)
}

// ---- SA commands (oper flag a) ----

func (s *Server) cmdSajoin(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) || s.relayOper(c, m, false) {
		return
	}
	t := s.opTarget(c, m.Params[0])
	if t == nil {
		return
	}
	var joined []string
	for _, name := range strings.Split(m.Params[1], ",") {
		if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
			s.reply(c, irc.ErrNoSuchChannel, name, "No such channel")
			continue
		}
		if ch := s.net.Channel(name); ch != nil && ch.Member(t.user) != nil {
			s.reply(c, irc.ErrUserOnChannel, t.user.Nick, ch.Name, "is already on channel")
			continue
		}
		host{s}.ForceJoin(t.uid, name)
		if ch := s.net.Channel(name); ch != nil {
			joined = append(joined, ch.Name)
		}
	}
	if len(joined) > 0 {
		list := strings.Join(joined, ",")
		s.notice(t, "*** You were forced to join "+list)
		s.snotice("SAJOIN: %s used SAJOIN to make %s join %s", c.user.Nick, t.user.Nick, list)
	}
}

func (s *Server) cmdSapart(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) || s.relayOper(c, m, false) {
		return
	}
	t := s.opTarget(c, m.Params[0])
	if t == nil {
		return
	}
	reason := irc.Truncate(m.Param(2), s.cfg.Limits.KickLen)
	var parted []string
	for _, name := range strings.Split(m.Params[1], ",") {
		ch := s.net.Channel(name)
		switch {
		case ch == nil:
			s.reply(c, irc.ErrNoSuchChannel, name, "No such channel")
			continue
		case ch.Member(t.user) == nil:
			s.reply(c, irc.ErrUserNotInChannel, t.user.Nick, ch.Name, "They aren't on that channel")
			continue
		}
		parted = append(parted, ch.Name)
		s.emit(&state.Part{UID: t.uid, Channel: ch.Name, Reason: reason})
	}
	if len(parted) == 0 {
		return
	}
	list := strings.Join(parted, ",")
	if reason != "" {
		s.notice(t, "*** You were forced to part "+list+" ("+reason+")")
		s.snotice("SAPART: %s used SAPART to make %s part %s (%s)", c.user.Nick, t.user.Nick, list, reason)
	} else {
		s.notice(t, "*** You were forced to part "+list)
		s.snotice("SAPART: %s used SAPART to make %s part %s", c.user.Nick, t.user.Nick, list)
	}
}

func (s *Server) cmdSanick(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) || s.relayOper(c, m, false) {
		return
	}
	t := s.opTarget(c, m.Params[0])
	if t == nil {
		return
	}
	nick := m.Params[1]
	if !irc.ValidNick(nick, s.cfg.Limits.NickLen) || services.IsReserved(nick) {
		s.stdReply(c, "FAIL", "SANICK", "INVALID_NICK", "Invalid nickname: '"+nick+"'", "", nick)
		return
	}
	if !irc.EqualFold(nick, t.user.Nick) && s.net.NickInUse(nick) {
		s.reply(c, irc.ErrNicknameInUse, nick, "Nickname is already in use")
		return
	}
	old := t.user.Nick
	if err := s.emit(&state.NickChange{UID: t.uid, Nick: nick, TS: time.Now().Unix()}); err != nil {
		s.snotice("%s failed SANICK (from %s to %s)", c.user.Nick, old, nick)
		return
	}
	s.snotice("%s used SANICK to change %s to %s", c.user.Nick, old, nick)
}

func (s *Server) cmdSamode(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	if len(m.Params) < 2 {
		s.reply(c, irc.ErrNeedMoreParams, "SAMODE", "Not enough parameters")
		return
	}
	text := strings.Join(m.Params[1:], " ")
	if !irc.IsChannel(m.Params[0]) {
		t := s.opTarget(c, m.Params[0])
		if t == nil {
			return
		}
		s.setUserModes(t, c.uid, m.Params[1])
		s.snotice("%s used SAMODE: %s %s", c.user.Nick, t.user.Nick, text)
		return
	}
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	s.applyChannelModes(c, c.user, ch, servicesRank, m.Params[1], m.Params[2:])
	s.snotice("%s used SAMODE: %s %s", c.user.Nick, ch.Name, text)
}

func (s *Server) cmdSatopic(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	topic := irc.Truncate(m.Params[1], s.cfg.Limits.TopicLen)
	if topic == ch.Topic {
		s.stdReply(c, "WARN", "SATOPIC", "TOPIC_UNCHANGED", "The topic on "+ch.Name+" is already what you are trying to change it to.",
			"The topic on "+ch.Name+" is already what you are trying to change it to.", ch.Name)
		return
	}
	s.emit(&state.Topic{Source: c.uid, Channel: ch.Name, Text: topic, By: c.user.Nick, At: time.Now()})
	s.snotice("%s used SATOPIC on %s, new topic: %s", c.user.Nick, ch.Name, topic)
}
