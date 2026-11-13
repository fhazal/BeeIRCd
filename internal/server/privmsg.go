package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

func (s *Server) cmdPrivmsg(c *Client, m irc.Message) { s.message(c, m, "PRIVMSG") }
func (s *Server) cmdNotice(c *Client, m irc.Message)  { s.message(c, m, "NOTICE") }

// cmdTagmsg carries only tags, like +typing. It reaches only clients with
// message-tags and never sends errors back.
func (s *Server) cmdTagmsg(c *Client, m irc.Message) { s.message(c, m, "TAGMSG") }

// message delivers PRIVMSG, NOTICE or TAGMSG. Only PRIVMSG sends errors.
func (s *Server) message(c *Client, m irc.Message, verb string) {
	quiet := verb != "PRIVMSG"
	fail := func(code string, params ...string) {
		if !quiet {
			s.reply(c, code, params...)
		}
	}
	if len(m.Params) == 0 || m.Params[0] == "" {
		fail(irc.ErrNoRecipient, "No recipient given ("+verb+")")
		return
	}
	tagOnly := verb == "TAGMSG"
	if !tagOnly && (len(m.Params) < 2 || m.Params[1] == "") {
		fail(irc.ErrNoTextToSend, "No text to send")
		return
	}
	u := c.user
	tags := clientTags(m.Tags)
	if tagOnly && len(tags) == 0 {
		return
	}
	if !tagOnly {
		c.lastMsg = time.Now()
	}
	targets := strings.Split(m.Params[0], ",")
	if len(targets) > maxTargets {
		targets = targets[:maxTargets]
	}
	for _, target := range targets {
		status := ""
		name := target
		if len(name) > 1 && strings.IndexByte(s.rankPrefixes(), name[0]) >= 0 && irc.IsChannel(name[1:]) {
			status, name = name[:1], name[1:]
		}
		text := m.Param(1)
		build := func(to string) *outMsg {
			params := []string{to}
			if !tagOnly {
				params = append(params, text)
			}
			o := s.out(u, verb, params...)
			o.msg.Trailing = !tagOnly
			o.tags = tags
			o.msgid = s.newMsgID()
			if tagOnly {
				o.needCap = "message-tags"
			}
			return o
		}
		if irc.IsChannel(name) {
			ch := s.net.Channel(name)
			if ch == nil {
				fail(irc.ErrNoSuchNick, name, "No such nick/channel")
				continue
			}
			if why := cannotSend(ch, u); why != "" {
				fail(irc.ErrCannotSendToChan, ch.Name, why)
				continue
			}
			if mem := ch.Member(u); mem != nil && mem.Hidden {
				if tagOnly {
					continue // typing in a +D channel would show you're there
				}
				s.reveal(u, ch)
			}
			if !tagOnly {
				t, why, drop := s.channelText(c, ch, verb, text)
				if why != "" {
					fail(irc.ErrCannotSendToChan, ch.Name, why)
					continue
				}
				if drop || s.net.Channel(ch.Name) == nil {
					continue
				}
				text = t
			}
			o := build(status + ch.Name)
			for _, mem := range ch.Members {
				if mem.User == u || mem.User.HasMode('d') { // +d: deaf to channel messages
					continue
				}
				if status != "" && !mem.AtLeast(state.RankForPrefix(status[0])) {
					continue
				}
				s.deliverTo(mem.User, o)
			}
			s.routeChannel(nil, u.UID, ch, status, verb, text, linkTags(tags, o.msgid))
			if c.caps["echo-message"] {
				c.deliver(o)
			}
			if status == "" {
				s.recordHistory(ch, o)
				if verb == "PRIVMSG" {
					s.svc.ChannelText(c.uid, ch, text) // BotServ kickers
				}
			}
			continue
		}
		t := s.net.UserByNick(name)
		if t == nil {
			fail(irc.ErrNoSuchNick, name, "No such nick/channel")
			continue
		}
		if why := s.refusesMessage(c, t, verb, text); why != "" {
			if !tagOnly {
				fail(irc.ErrCantSendToUser, t.Nick, why)
			}
			continue
		}
		o := build(t.Nick)
		if c.caps["echo-message"] {
			c.deliver(o)
		}
		if s.silenced(t, u) {
			continue // SILENCE: dropped without a word, like UnrealIRCd
		}
		if s.svc.IsBot(t) {
			if verb == "PRIVMSG" {
				s.svc.Message(c.uid, t, m.Params[1])
			}
			continue
		}
		if t == u && c.caps["echo-message"] && c.capture != nil {
			// To yourself with echo-message: the echo carries the label,
			// the delivered copy goes out on its own, unlabeled.
			held := c.capture
			c.capture = nil
			c.deliver(o)
			c.capture = held
			continue
		}
		if !s.ours(t.UID) {
			s.routeUser(u.UID, t, verb, text, linkTags(tags, o.msgid))
		} else {
			s.deliverTo(t, o)
		}
		if verb == "PRIVMSG" && t.Away != "" {
			s.reply(c, irc.RplAway, t.Nick, t.Away)
		}
	}
}

// cannotSend returns why u may not talk in ch, or "" when it may.
func cannotSend(ch *state.Channel, u *state.User) string {
	mem := ch.Member(u)
	if mem != nil && mem.AtLeast(state.RankVoice) {
		return ""
	}
	switch {
	case mem == nil && ch.HasMode('n'):
		return "Cannot send to channel (no external messages)"
	case ch.HasMode('m'):
		return "Cannot send to channel (+m)"
	case ch.HasMode('M') && u.Account == "":
		return "You need to identify to a registered nick to talk in that channel"
	case ch.IsMuted(u):
		return "Cannot send to channel (you're banned)"
	}
	return ""
}
