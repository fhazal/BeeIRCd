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

func (s *Server) joinOne(c *Client, name, key string) { s.joinVia(c, name, key, 0) }

// joinVia joins c to name; hops counts the +L channels it came through.
func (s *Server) joinVia(c *Client, name, key string, hops int) {
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
		uop := s.svc.JoinLevel(c.uid, ch.Name) >= store.LevelUOP // ChanServ UOP and up get past +i and +k
		if code, text, link := s.joinRefusal(c, ch, key, uop); code != "" {
			if to := ch.BanForward(u); code == irc.ErrBannedFromChan && to != "" && hops < maxLinkHops && irc.Fold(to) != irc.Fold(ch.Name) {
				s.reply(c, irc.ErrLinkChannel, ch.Name, to, "[Link] "+ch.Name+" has you banned, so you are automatically being transferred to "+to)
				s.joinVia(c, to, "", hops+1)
				return
			}
			if to := ch.Param('L'); link && to != "" && hops < maxLinkHops {
				s.reply(c, irc.ErrLinkChannel, ch.Name, to, "[Link] "+ch.Name+" "+linkReason(code)+", so you are automatically being transferred to the linked channel "+to)
				s.joinVia(c, to, "", hops+1)
				return
			}
			s.reply(c, code, ch.Name, text)
			return
		}
	}
	if ch == nil {
		if why := s.restrictedCreate(c); why != "" {
			s.reply(c, irc.ErrCannotDoCommand, "JOIN", why)
			return
		}
		if why := s.createRefused(c); why != "" {
			s.reply(c, irc.ErrCannotDoCommand, "JOIN", why)
			return
		}
	}
	ev := &state.Join{UID: u.UID, Channel: name, TS: time.Now().Unix()}
	if s.emit(ev) != nil {
		return
	}
	ch = s.net.Channel(name)
	if !ev.Created {
		s.floodEvent(c, ch, 'j')
		if ch = s.net.Channel(name); ch == nil || ch.Member(u) == nil {
			return
		}
	}
	if ch.Topic != "" {
		s.sendTopic(c, ch)
	}
	s.joinNames(c, ch)
	s.joinPlayback(c, ch)
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
			s.partOne(c.user, ch, s.partReason(c, ch, reason))
		}
	}
}

// partReason drops the part message of someone a ban keeps quiet (a ban,
// ~q, ~p), like UnrealIRCd: a part message is a way to talk.
func (s *Server) partReason(c *Client, ch *state.Channel, reason string) string {
	u := c.user
	if reason == "" || c.isOper() {
		return reason
	}
	if mem := ch.Member(u); mem != nil && mem.AtLeast(state.RankVoice) {
		return reason
	}
	if ch.IsMuted(u) || ch.Stops(u, state.StopPartMsg) {
		return ""
	}
	return reason
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
	s.reveal(u, ch)
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

// joinNames sends the names list a JOIN brings, unless the client asked
// for draft/no-implicit-names (it sends NAMES itself when it wants them).
func (s *Server) joinNames(c *Client, ch *state.Channel) {
	s.joinMark(c, ch.Name)
	if c.caps["draft/no-implicit-names"] {
		return
	}
	s.sendNames(c, ch)
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
		if (!inside && mem.User.HasMode('i')) || !ch.Sees(c.user, mem) {
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

// cmdKick takes "KICK #chan nick[,nick]" or RFC 2812 pairs
// "KICK #a,#b nick1,nick2" (one channel per nick).
func (s *Server) cmdKick(c *Client, m irc.Message) {
	chans := strings.Split(m.Params[0], ",")
	nicks := strings.Split(m.Params[1], ",")
	if len(nicks) > maxTargets {
		nicks = nicks[:maxTargets]
	}
	if len(chans) > 1 && len(chans) != len(nicks) {
		s.reply(c, irc.ErrNeedMoreParams, "KICK", "Not enough parameters")
		return
	}
	reason := irc.Truncate(m.Param(2), s.cfg.Limits.KickLen)
	if reason == "" {
		reason = c.user.Nick
	}
	if len(chans) == 1 {
		s.kick(c, chans[0], nicks, reason)
		return
	}
	for i, name := range chans {
		s.kick(c, name, nicks[i:i+1], reason)
	}
}

// kick removes nicks from the channel name on behalf of c.
func (s *Server) kick(c *Client, name string, nicks []string, reason string) {
	ch, mem := s.kickFrom(c, name, "KICK")
	if ch == nil {
		return
	}
	for _, nick := range nicks {
		t := s.kickTarget(c, ch, mem, nick, "KICK", "kick")
		if t == nil {
			continue
		}
		s.emit(&state.Kick{Source: c.user.UID, UID: t.UID, Channel: ch.Name, Reason: reason})
		if s.net.Channel(ch.Name) == nil {
			return // the last one left: the channel is gone
		}
	}
}

// kickFrom checks that c may kick people from the channel name (KICK and
// REMOVE); nil when not, with the reason sent.
func (s *Server) kickFrom(c *Client, name, cmd string) (*state.Channel, *state.Member) {
	u := c.user
	ch := s.net.Channel(name)
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, name, "No such channel")
		return nil, nil
	}
	mem := ch.Member(u)
	switch {
	case mem == nil:
		s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
	case !mem.AtLeast(state.RankHalfop):
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
	case ch.HasMode('Q') && !c.isOper():
		s.reply(c, irc.ErrCannotDoCommand, cmd, "channel is +Q")
	case !c.isOper() && ch.Stops(u, state.StopKick):
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You may not kick people in this channel (you're banned from it)")
	default:
		return ch, mem
	}
	return nil, nil
}

// kickTarget finds nick in ch and checks that c (member mem) may kick
// them; verb is "kick" or "remove" for the answers.
func (s *Server) kickTarget(c *Client, ch *state.Channel, mem *state.Member, nick, cmd, verb string) *state.User {
	u := c.user
	t := s.userByUIDOrNick(nick)
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
		return nil
	}
	tm := ch.Member(t)
	switch {
	case tm == nil:
		s.reply(c, irc.ErrUserNotInChannel, t.Nick, ch.Name, "They aren't on that channel")
	case t.Service:
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You can't "+verb+" "+t.Nick+": it is a network service")
	case t.HasMode('q') && t != u && !c.can('a'):
		s.reply(c, irc.ErrCannotDoCommand, cmd, "user is unkickable (user mode +q)")
	case t != u && !canKick(mem, tm):
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You can't "+verb+" "+t.Nick+": their channel status protects them")
	default:
		return t
	}
	return nil
}

func (s *Server) cmdInvite(c *Client, m irc.Message) {
	u := c.user
	t := s.net.UserByNick(m.Params[0])
	if t == nil {
		s.reply(c, irc.ErrNoSuchNick, m.Params[0], "No such nick/channel")
		return
	}
	name := m.Params[1]
	if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
		s.reply(c, irc.ErrNoSuchChannel, name, "No such channel")
		return
	}
	if ch := s.net.Channel(name); ch != nil {
		mem := ch.Member(u)
		switch {
		case mem == nil:
			s.reply(c, irc.ErrNotOnChannel, ch.Name, "You're not on that channel")
			return
		case ch.HasMode('V') && !c.isOper():
			s.reply(c, irc.ErrNoInvite, "Cannot invite (+V) at channel "+ch.Name)
			return
		case !c.isOper() && ch.Stops(u, state.StopInvite):
			s.reply(c, irc.ErrNoInvite, "Cannot invite at channel "+ch.Name+" (you're banned from inviting)")
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
	if s.silenced(t, u) {
		return // SILENCE: the invite is dropped quietly
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
