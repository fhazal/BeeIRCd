package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

func (s *Server) cmdPrivmsg(c *Client, m irc.Message) { s.message(c, m, "PRIVMSG") }
func (s *Server) cmdNotice(c *Client, m irc.Message)  { s.message(c, m, "NOTICE") }

// message delivers PRIVMSG or NOTICE. NOTICE never sends errors back.
func (s *Server) message(c *Client, m irc.Message, verb string) {
	notice := verb == "NOTICE"
	fail := func(code string, params ...string) {
		if !notice {
			s.reply(c, code, params...)
		}
	}
	if len(m.Params) == 0 || m.Params[0] == "" {
		fail(irc.ErrNoRecipient, "No recipient given ("+verb+")")
		return
	}
	if len(m.Params) < 2 || m.Params[1] == "" {
		fail(irc.ErrNoTextToSend, "No text to send")
		return
	}
	u := c.user
	text := m.Params[1]
	c.lastMsg = time.Now()
	targets := strings.Split(m.Params[0], ",")
	if len(targets) > maxTargets {
		targets = targets[:maxTargets]
	}
	for _, target := range targets {
		status := ""
		name := target
		if len(name) > 1 && (name[0] == '@' || name[0] == '+') && irc.IsChannel(name[1:]) {
			status, name = name[:1], name[1:]
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
			line := irc.Message{Source: u.Prefix(), Command: verb, Params: []string{status + ch.Name, text}, Trailing: true}.String()
			for _, mem := range ch.Members {
				if mem.User == u {
					continue
				}
				if status == "@" && !mem.Op || status == "+" && !mem.Op && !mem.Voice {
					continue
				}
				s.sendTo(mem.User, line)
			}
			continue
		}
		t := s.net.UserByNick(name)
		if t == nil {
			fail(irc.ErrNoSuchNick, name, "No such nick/channel")
			continue
		}
		s.sendTo(t, irc.Message{Source: u.Prefix(), Command: verb, Params: []string{t.Nick, text}, Trailing: true}.String())
		if !notice && t.Away != "" {
			s.reply(c, irc.RplAway, t.Nick, t.Away)
		}
	}
}

// cannotSend returns why u may not talk in ch, or "" when it may.
func cannotSend(ch *state.Channel, u *state.User) string {
	mem := ch.Member(u)
	if mem != nil && (mem.Op || mem.Voice) {
		return ""
	}
	switch {
	case mem == nil && ch.HasMode('n'):
		return "Cannot send to channel (no external messages)"
	case ch.HasMode('m'):
		return "Cannot send to channel (+m)"
	case ch.HasMode('M') && u.Account == "":
		return "You need to identify to a registered nick to talk in that channel"
	case ch.IsBanned(u):
		return "Cannot send to channel (you're banned)"
	}
	return ""
}
