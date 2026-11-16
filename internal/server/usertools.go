package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// Commands for everyone that other servers have: CYCLE (UnrealIRCd),
// REMOVE/FPART and UNINVITE (InspIRCd), and caller ID: user mode +g with
// ACCEPT (InspIRCd callerid, the numerics every client knows).

const (
	acceptMax      = 30
	calleridEvery  = time.Minute // how often a +g person hears about one sender
	rplAcceptList  = "281"
	rplEndOfAccept = "282"
	errAcceptFull  = "456"
	errAcceptExist = "457"
	errAcceptNot   = "458"
	errTargUModeG  = "716"
	rplTargNotify  = "717"
	rplUModeGMsg   = "718"
	rplUninvited   = "494"
	errNotInvited  = "505"
)

func init() {
	commands["CYCLE"] = command{1, false, (*Server).cmdCycle}
	commands["REMOVE"] = command{2, false, (*Server).cmdRemove}
	commands["FPART"] = command{2, false, (*Server).cmdRemove}
	commands["UNINVITE"] = command{2, false, (*Server).cmdUninvite}
	commands["ACCEPT"] = command{1, false, (*Server).cmdAccept}
	helpTopics["CYCLE"] = []string{"CYCLE <#channel[,#channel]>", "Leaves a channel and joins it again at once. Refused when you would not get back in (a ban, +i)."}
	helpTopics["REMOVE"] = []string{"REMOVE <#channel> <nick> [reason]", "Makes someone leave a channel as a PART instead of a KICK, so their client does not rejoin by itself. Same rules as KICK.",
		"The older order REMOVE <nick> <#channel> works too."}
	helpTopics["FPART"] = []string{"FPART <#channel> <nick> [reason]", "The same as REMOVE."}
	helpTopics["UNINVITE"] = []string{"UNINVITE <nick> <#channel>", "Takes back an INVITE (channel ops)."}
	helpTopics["ACCEPT"] = []string{"ACCEPT <nick>[,-nick][,*]", "With user mode +g only people on your accept list can message you: nick adds someone, -nick removes them, * shows the list (at most 30).",
		"Anyone you message yourself is added for you. The list lasts until you disconnect."}
}

// cmdCycle is CYCLE #chan[,#chan]: part and join again.
func (s *Server) cmdCycle(c *Client, m irc.Message) {
	u := c.user
	for _, name := range strings.Split(m.Params[0], ",") {
		ch := s.net.Channel(name)
		switch {
		case ch == nil:
			s.reply(c, irc.ErrNoSuchChannel, name, "No such channel")
			continue
		case ch.Member(u) == nil:
			s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
			continue
		}
		chName, key := ch.Name, ch.Key
		if len(ch.Members) > 1 {
			// Leaving frees their own place, so a full channel is fine.
			if code, text, _ := s.joinRefusal(c, ch, key, false); code != "" && code != irc.ErrChannelIsFull {
				s.reply(c, code, chName, "Not cycled, you could not get back in: "+text)
				continue
			}
		}
		s.partOne(u, ch, s.partReason(c, ch, "Cycling"))
		s.joinOne(c, chName, key)
	}
}

// cmdRemove is REMOVE #chan nick [reason] (also FPART, and the older
// "nick #chan" order): a kick that looks like a PART.
func (s *Server) cmdRemove(c *Client, m irc.Message) {
	name, nick := m.Params[0], m.Params[1]
	if !irc.IsChannel(name) && irc.IsChannel(nick) {
		name, nick = nick, name
	}
	ch, mem := s.kickFrom(c, name, "REMOVE")
	if ch == nil {
		return
	}
	t := s.kickTarget(c, ch, mem, nick, "REMOVE", "remove")
	if t == nil {
		return
	}
	why := irc.Truncate(m.Param(2), s.cfg.Limits.KickLen)
	reason := "Removed by " + c.user.Nick
	if why != "" {
		reason += ": " + why
	}
	if s.ours(t.UID) {
		s.emit(&state.Part{UID: t.UID, Channel: ch.Name, Reason: reason})
		return
	}
	// Only their own server can make them PART.
	if o := s.linkOf(t.UID); o != nil && o.peer.Bee {
		o.send(":" + c.uid + " REMOVE " + ch.Name + " " + t.UID + " :" + why)
		return
	}
	s.emit(&state.Kick{Source: c.uid, UID: t.UID, Channel: ch.Name, Reason: reason})
}

// cmdUninvite is UNINVITE nick #chan.
func (s *Server) cmdUninvite(c *Client, m irc.Message) {
	u := c.user
	t := s.net.UserByNick(m.Params[0])
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, m.Params[0], "No such nick/channel")
		return
	}
	ch := s.net.Channel(m.Params[1])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[1], "No such channel")
		return
	}
	mem := ch.Member(u)
	switch {
	case mem == nil:
		s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
	case !mem.AtLeast(state.RankHalfop):
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
	case s.ours(t.UID) && !ch.Invited[t.UID]:
		// The invite list of someone elsewhere is on their server.
		s.reply(c, errNotInvited, t.Nick, ch.Name, "Is not invited to channel "+ch.Name)
	default:
		s.emit(&state.Invite{Source: u.UID, UID: t.UID, Channel: ch.Name, Remove: true})
		s.reply(c, rplUninvited, ch.Name, t.Nick, "Uninvited")
	}
}

// remoteUninvite is ":<uid> UNINVITE <uid> <#chan>" from a link: for one
// of ours, or sent on towards their server.
func (s *Server) remoteUninvite(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 {
		return
	}
	t := s.userByUIDOrNick(m.Params[0])
	switch {
	case t == nil:
	case s.ours(t.UID):
		s.emitFrom(l, m.Source, &state.Invite{Source: s.userSource(m.Source), UID: t.UID, Channel: m.Params[1], Remove: true})
	default:
		if o := s.linkOf(t.UID); o != nil && o != l {
			o.send(m.String())
		}
	}
}

// ---- caller ID: user mode +g and ACCEPT ----

// acceptPrune forgets people on c's accept list who left.
func (s *Server) acceptPrune(c *Client) {
	for uid := range c.accept {
		if s.net.UserByUID(uid) == nil {
			delete(c.accept, uid)
		}
	}
}

// acceptAdd puts t on c's list; false when it is full.
func (s *Server) acceptAdd(c *Client, t *state.User) bool {
	if c.accept[t.UID] {
		return true
	}
	if len(c.accept) >= acceptMax {
		s.acceptPrune(c)
		if len(c.accept) >= acceptMax {
			return false
		}
	}
	if c.accept == nil {
		c.accept = make(map[string]bool)
	}
	c.accept[t.UID] = true
	return true
}

// cmdAccept is ACCEPT nick,-nick,*.
func (s *Server) cmdAccept(c *Client, m irc.Message) {
	for _, w := range strings.Split(m.Params[0], ",") {
		switch {
		case w == "":
		case w == "*":
			s.acceptPrune(c)
			var nicks []string
			for uid := range c.accept {
				nicks = append(nicks, s.net.UserByUID(uid).Nick)
			}
			for len(nicks) > 0 {
				n := min(10, len(nicks))
				s.replyBare(c, rplAcceptList, nicks[:n]...)
				nicks = nicks[n:]
			}
			s.reply(c, rplEndOfAccept, "End of /ACCEPT list.")
		case w[0] == '-':
			t := s.net.UserByNick(w[1:])
			switch {
			case t == nil:
				s.reply(c, irc.ErrNoSuchNick, w[1:], "No such nick/channel")
			case !c.accept[t.UID]:
				s.reply(c, errAcceptNot, t.Nick, "is not on your accept list")
			default:
				delete(c.accept, t.UID)
			}
		default:
			nick := strings.TrimPrefix(w, "+")
			t := s.net.UserByNick(nick)
			switch {
			case t == nil:
				s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
			case c.accept[t.UID]:
				s.reply(c, errAcceptExist, t.Nick, "is already on your accept list")
			case !s.acceptAdd(c, t):
				s.reply(c, errAcceptFull, "Accept list is full")
			}
		}
	}
}

// callerBlocked reports whether t (one of ours, with +g) refuses a private
// message from from. With tell, the sender hears why and t hears who tried
// (at most once a minute for each sender). Opers and services get through.
func (s *Server) callerBlocked(t, from *state.User, tell bool) bool {
	if from == nil || !t.HasMode('g') || from == t || from.Service || from.Modes['o'] {
		return false
	}
	tc := s.clientOf(t)
	if tc == nil || tc.accept[from.UID] {
		return false
	}
	if !tell {
		return true
	}
	h := host{s}
	h.Numeric(from.UID, errTargUModeG, t.Nick, "is in +g mode (server-side ignore).")
	now := time.Now()
	if last, ok := tc.calleridAt[from.UID]; ok && now.Sub(last) < calleridEvery {
		return true
	}
	if tc.calleridAt == nil || len(tc.calleridAt) > 200 {
		tc.calleridAt = make(map[string]time.Time)
	}
	tc.calleridAt[from.UID] = now
	h.Numeric(from.UID, rplTargNotify, t.Nick, "has been informed that you messaged them.")
	s.reply(tc, rplUModeGMsg, from.Nick, from.Ident+"@"+from.Host, "is messaging you, and you have user mode +g set. Use /ACCEPT "+from.Nick+" to allow.")
	return true
}
