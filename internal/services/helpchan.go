package services

import (
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// [services] help_channel (Anope's m_helpchan): op (or more) in the help
// channel gives user mode +h, set by HelpServ so it works for people on
// any server. Losing the op (deop, part, kick) takes the +h away again,
// unless they may have it anyway: opers and the HelpServ helper list.
// Someone who had +h before the op keeps it.

// helpChanCheck looks at c again after something happened in channel.
func (s *Services) helpChanCheck(c *Client, channel string) {
	if c == nil || c.closed || c.user == nil {
		return
	}
	help := s.cfg.Services.HelpChannel
	if !c.helpChan && (help == "" || !irc.EqualFold(channel, help)) {
		return
	}
	want := false
	if ch := s.net.Channel(help); help != "" && ch != nil {
		m := ch.Member(c.user)
		want = m != nil && m.AtLeast(state.RankOp)
	}
	switch {
	case want && !c.user.HasMode('h'):
		c.helpChan = true
		s.helpSetMode(c, true)
	case !want && c.helpChan:
		c.helpChan = false
		if !s.mayHelp(c) {
			s.helpSetMode(c, false)
		}
	}
}

// helpChanModes checks everyone in ch after a mode change, when ch is the
// help channel.
func (s *Services) helpChanModes(ch *state.Channel) {
	help := s.cfg.Services.HelpChannel
	if help == "" || !irc.EqualFold(ch.Name, help) {
		return
	}
	for uid := range ch.Members {
		s.helpChanCheck(s.clients[uid], ch.Name)
	}
}
