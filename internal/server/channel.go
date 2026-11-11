package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	maxTargets     = 4
	maxModeParams  = 4
	maxListEntries = 100
)

func (s *Server) cmdJoin(c *Client, m irc.Message) {
	u := c.user
	if m.Params[0] == "0" {
		for _, ch := range s.net.Channels() {
			if ch.Member(u) != nil {
				s.partOne(u, ch, "Left all channels")
			}
		}
		return
	}
	names := strings.Split(m.Params[0], ",")
	keys := strings.Split(m.Param(1), ",")
	for i, name := range names {
		key := ""
		if i < len(keys) {
			key = keys[i]
		}
		s.joinOne(c, name, key)
	}
}

func (s *Server) joinOne(c *Client, name, key string) {
	u := c.user
	if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
		s.reply(c, irc.ErrBadChanName, name, "Illegal channel name")
		return
	}
	if f, ok := s.svc.Forbidden(store.ForbidChan, name); ok && !c.isOper() {
		s.reply(c, irc.ErrBadChanName, name, "Cannot join channel (forbidden by the network: "+f.Reason+")")
		return
	}
	ch := s.net.Channel(name)
	if ch != nil && ch.Member(u) != nil {
		return
	}
	if len(u.Channels) >= s.cfg.Limits.MaxChannels {
		s.reply(c, irc.ErrTooManyChannels, name, "You have joined too many channels")
		return
	}
	if ch != nil {
		invited := ch.Invited[u.UID]
		uop := s.svc.JoinLevel(c.uid, ch.Name) >= store.LevelUOP // ChanServ UOP and up get past +i and +k
		switch {
		case ch.HasMode('O') && !c.isOper():
			s.reply(c, irc.ErrOperOnly, ch.Name, "Cannot join channel (+O)")
			return
		case ch.HasMode('i') && !invited && !uop && !ch.Matches('I', u):
			s.reply(c, irc.ErrInviteOnlyChan, ch.Name, "Cannot join channel (+i)")
			return
		case ch.HasMode('k') && key != ch.Key && !invited && !uop:
			s.reply(c, irc.ErrBadChannelKey, ch.Name, "Cannot join channel (+k)")
			return
		case ch.HasMode('l') && len(ch.Members) >= ch.Limit && !invited:
			s.reply(c, irc.ErrChannelIsFull, ch.Name, "Cannot join channel (+l)")
			return
		case ch.IsBanned(u) && !invited:
			s.reply(c, irc.ErrBannedFromChan, ch.Name, "Cannot join channel (+b)")
			return
		case ch.HasMode('R') && u.Account == "":
			s.reply(c, irc.ErrNeedReggedNick, ch.Name, "You need to identify to a registered nick to join that channel")
			return
		}
	}
	ev := &state.Join{UID: u.UID, Channel: name, TS: time.Now().Unix()}
	if s.emit(ev) != nil {
		return
	}
	ch = s.net.Channel(name)
	if ch.Topic != "" {
		s.sendTopic(c, ch)
	}
	s.sendNames(c, ch)
}

func (s *Server) cmdPart(c *Client, m irc.Message) {
	reason := irc.Truncate(m.Param(1), s.cfg.Limits.KickLen)
	for _, name := range strings.Split(m.Params[0], ",") {
		ch := s.net.Channel(name)
		switch {
		case ch == nil:
			s.reply(c, irc.ErrNoSuchChannel, name, "No such channel")
		case ch.Member(c.user) == nil:
			s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
		default:
			s.partOne(c.user, ch, reason)
		}
	}
}

func (s *Server) partOne(u *state.User, ch *state.Channel, reason string) {
	s.emit(&state.Part{UID: u.UID, Channel: ch.Name, Reason: reason})
}

func (s *Server) cmdTopic(c *Client, m irc.Message) {
	u := c.user
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	mem := ch.Member(u)
	if len(m.Params) < 2 {
		if mem == nil && ch.HasMode('s') {
			s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
			return
		}
		if ch.Topic == "" {
			s.reply(c, irc.RplNoTopic, ch.Name, "No topic is set")
			return
		}
		s.sendTopic(c, ch)
		return
	}
	switch {
	case mem == nil:
		s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
		return
	case ch.HasMode('t') && !mem.AtLeast(state.RankHalfop):
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
		return
	case s.svc.TopicLocked(c.uid, ch):
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "The topic is locked by ChanServ")
		return
	}
	topic := irc.Truncate(m.Params[1], s.cfg.Limits.TopicLen)
	if topic == ch.Topic {
		return
	}
	s.emit(&state.Topic{Source: u.UID, Channel: ch.Name, Text: topic, By: u.Nick, At: time.Now()})
}

func (s *Server) sendTopic(c *Client, ch *state.Channel) {
	s.reply(c, irc.RplTopic, ch.Name, ch.Topic)
	s.replyBare(c, irc.RplTopicWhoTime, ch.Name, ch.TopicBy, strconv.FormatInt(ch.TopicAt.Unix(), 10))
}

func (s *Server) cmdNames(c *Client, m irc.Message) {
	if len(m.Params) == 0 {
		s.reply(c, irc.RplEndOfNames, "*", "End of /NAMES list.")
		return
	}
	for _, name := range strings.Split(m.Params[0], ",") {
		ch := s.net.Channel(name)
		if ch == nil || (ch.Member(c.user) == nil && (ch.HasMode('s') || ch.HasMode('p'))) {
			s.reply(c, irc.RplEndOfNames, name, "End of /NAMES list.")
			continue
		}
		s.sendNames(c, ch)
	}
}

// sendNames lists ch's members, as many per line as fit.
func (s *Server) sendNames(c *Client, ch *state.Channel) {
	kind := "="
	switch {
	case ch.HasMode('s'):
		kind = "@"
	case ch.HasMode('p'):
		kind = "*"
	}
	inside := ch.Member(c.user) != nil
	head := len(s.cfg.Server.Name) + len(c.target()) + len(ch.Name) + 16
	var names []string
	size := head
	flush := func() {
		if len(names) > 0 {
			s.reply(c, irc.RplNamReply, kind, ch.Name, strings.Join(names, " "))
			names, size = nil, head
		}
	}
	for _, mem := range ch.Members { // unsorted, like InspIRCd: sorting cost a lot on every JOIN
		if !inside && mem.User.HasMode('i') {
			continue
		}
		n := memberPrefix(c, mem) + mem.User.Nick
		if c.caps["userhost-in-names"] {
			n = memberPrefix(c, mem) + mem.User.Prefix()
		}
		if size+len(n)+1 > irc.MaxLine {
			flush()
		}
		names = append(names, n)
		size += len(n) + 1
	}
	flush()
	s.reply(c, irc.RplEndOfNames, ch.Name, "End of /NAMES list.")
}

func (s *Server) cmdList(c *Client, m irc.Message) {
	var chans []*state.Channel
	if len(m.Params) > 0 && m.Params[0] != "" {
		for _, name := range strings.Split(m.Params[0], ",") {
			if ch := s.net.Channel(name); ch != nil {
				chans = append(chans, ch)
			}
		}
	} else {
		chans = s.net.Channels()
	}
	s.reply(c, irc.RplListStart, "Channel", "Users  Name")
	for _, ch := range chans {
		if (ch.HasMode('s') || ch.HasMode('p')) && ch.Member(c.user) == nil && !c.can('h') {
			continue
		}
		modes, _ := ch.ModeString(false)
		s.reply(c, irc.RplList, ch.Name, strconv.Itoa(len(ch.Members)), "["+modes+"] "+ch.Topic)
	}
	s.reply(c, irc.RplListEnd, "End of /LIST")
}

func (s *Server) cmdKick(c *Client, m irc.Message) {
	u := c.user
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	mem := ch.Member(u)
	if mem == nil {
		s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
		return
	}
	if !mem.AtLeast(state.RankHalfop) {
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
		return
	}
	reason := irc.Truncate(m.Param(2), s.cfg.Limits.KickLen)
	if reason == "" {
		reason = u.Nick
	}
	targets := strings.Split(m.Params[1], ",")
	if len(targets) > maxTargets {
		targets = targets[:maxTargets]
	}
	for _, nick := range targets {
		t := s.net.UserByNick(nick)
		if t == nil {
			s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
			continue
		}
		tm := ch.Member(t)
		if tm == nil {
			s.reply(c, irc.ErrUserNotInChannel, t.Nick, ch.Name, "They aren't on that channel")
			continue
		}
		if t.Service {
			s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You can't kick "+t.Nick+": it is a network service")
			continue
		}
		if t != u && !canKick(mem, tm) {
			s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You can't kick "+t.Nick+": their channel status protects them")
			continue
		}
		s.emit(&state.Kick{Source: u.UID, UID: t.UID, Channel: ch.Name, Reason: reason})
		if s.net.Channel(ch.Name) == nil {
			return // the last one left: the channel is gone
		}
	}
}

func (s *Server) cmdInvite(c *Client, m irc.Message) {
	u := c.user
	t := s.net.UserByNick(m.Params[0])
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, m.Params[0], "No such nick/channel")
		return
	}
	name := m.Params[1]
	if ch := s.net.Channel(name); ch != nil {
		mem := ch.Member(u)
		switch {
		case mem == nil:
			s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
			return
		case ch.HasMode('i') && !mem.AtLeast(state.RankHalfop):
			s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
			return
		case ch.Member(t) != nil:
			s.reply(c, irc.ErrUserOnChannel, t.Nick, ch.Name, "is already on channel")
			return
		}
		name = ch.Name
	}
	s.replyBare(c, irc.RplInviting, t.Nick, name)
	if t.Away != "" {
		s.reply(c, irc.RplAway, t.Nick, t.Away)
	}
	s.emit(&state.Invite{Source: u.UID, UID: t.UID, Channel: name})
}

// canKick follows UnrealIRCd: halfops kick only people below halfop, ops
// kick anyone below admin, and only owners kick admins and owners.
func canKick(kicker, target *state.Member) bool {
	switch {
	case target.AtLeast(state.RankAdmin):
		return kicker.AtLeast(state.RankOwner)
	case kicker.AtLeast(state.RankOp):
		return true
	case kicker.AtLeast(state.RankHalfop):
		return !target.AtLeast(state.RankHalfop)
	}
	return false
}

// memberPrefix is "@+" for multi-prefix clients, else the highest only.
func memberPrefix(c *Client, m *state.Member) string {
	if c.caps["multi-prefix"] {
		return m.AllPrefixes()
	}
	return m.Prefix()
}

func accountOrStar(u *state.User) string {
	if u.Account == "" {
		return "*"
	}
	return u.Account
}
