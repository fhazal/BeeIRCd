package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/spamfilter"
	"github.com/fhazal/beeircd/internal/store"
)

// The spam filter (after 1.0): every PRIVMSG, NOTICE,
// PART, QUIT, TOPIC and AWAY from a local person goes past the rules
// before the command runs, and so do nick!user@host:realname at connect
// and on a nick change. Opers and Exceptions (ELINE) are never filtered;
// [spamfilter] except channels neither.

// spamNoticeEvery is how often one rule may tell the opers.
const spamNoticeEvery = 10 * time.Second

// spamSkip reports whether c is never filtered.
func (s *Server) spamSkip(c *Client) bool {
	return c.isOper() || c.user == nil || c.user.Service || s.svc.SpamExempt(c.user)
}

// spamCheck runs the rules on m from c. It returns false when the command
// must not run at all; it may take targets or a reason out of m.
func (s *Server) spamCheck(c *Client, m *irc.Message) bool {
	set := s.svc.SpamSet()
	if set == nil || s.spamSkip(c) {
		return true
	}
	switch m.Command {
	case "PRIVMSG", "NOTICE":
		if len(m.Params) < 2 || m.Params[1] == "" {
			return true
		}
		var keep []string
		for _, target := range strings.Split(m.Params[0], ",") {
			name := strings.TrimLeft(target, s.rankPrefixes())
			t := byte(spamfilter.Private)
			switch {
			case irc.IsChannel(name) && m.Command == "NOTICE":
				t = spamfilter.ChannelNotice
			case irc.IsChannel(name):
				t = spamfilter.Channel
			case m.Command == "NOTICE":
				t = spamfilter.PrivateNotice
			}
			if irc.IsChannel(name) && s.cfg.SpamExcept(name) {
				keep = append(keep, target)
				continue
			}
			switch s.spamAct(c, t, target, m.Params[1]) {
			case spamPass:
				keep = append(keep, target)
			case spamGone:
				return false
			}
		}
		if len(keep) == 0 {
			return false
		}
		m.Params[0] = strings.Join(keep, ",")
	case "PART":
		if len(m.Params) < 2 || m.Params[1] == "" || s.allExcept(m.Params[0]) {
			return true
		}
		switch s.spamAct(c, spamfilter.Part, m.Params[0], m.Params[1]) {
		case spamStop:
			m.Params = m.Params[:1] // they still leave, without the text
		case spamGone:
			return false
		}
	case "QUIT":
		if len(m.Params) < 1 || m.Params[0] == "" {
			return true
		}
		switch s.spamAct(c, spamfilter.Quit, "", m.Params[0]) {
		case spamStop:
			m.Params = nil
		case spamGone:
			return false
		}
	case "TOPIC":
		if len(m.Params) < 2 || m.Params[1] == "" || s.cfg.SpamExcept(m.Params[0]) {
			return true
		}
		return s.spamAct(c, spamfilter.Topic, m.Params[0], m.Params[1]) == spamPass
	case "AWAY":
		if len(m.Params) < 1 || m.Params[0] == "" {
			return true
		}
		return s.spamAct(c, spamfilter.Away, "", m.Params[0]) == spamPass
	}
	return true
}

// allExcept reports whether every channel in list is on the except list.
func (s *Server) allExcept(list string) bool {
	for _, ch := range strings.Split(list, ",") {
		if !s.cfg.SpamExcept(ch) {
			return false
		}
	}
	return true
}

type spamOutcome int

const (
	spamPass spamOutcome = iota // nothing caught it, or only warned
	spamStop                    // this text is dropped
	spamGone                    // the person is gone or shunned: drop everything
)

// spamAct checks text for target letter t (to: the target, for notices)
// and carries out the first rule that catches it.
func (s *Server) spamAct(c *Client, t byte, to, text string) spamOutcome {
	r := s.svc.SpamSet().First(t, text)
	if r == nil || !spamfilter.Known(r.Action) {
		return spamPass
	}
	return s.spamDo(c, r, t, to, text)
}

// spamDo carries out rule r on c.
func (s *Server) spamDo(c *Client, r *spamfilter.Rule, t byte, to, text string) spamOutcome {
	now := time.Now()
	s.svc.SpamHit(r.ID, now)
	who := c.nick
	if c.user != nil {
		who = c.user.Nick + "!" + c.user.TrueIdent() + "@" + c.user.RealHost
	}
	where := spamfilter.TargetWords[t]
	if to != "" {
		where += " to " + to
	}
	s.snoticeEvery("spamfilter:"+r.Key(), spamNoticeEvery, "Spam filter %d caught %s (%s, %s): %s",
		r.ID, who, where, actionName(r.Filter), irc.Truncate(text, 120))
	reason := r.Reason
	switch r.Action {
	case spamfilter.Warn:
		return spamPass
	case spamfilter.Block:
		if t == spamfilter.Quit || t == spamfilter.Part {
			return spamStop // the quit or part still happens
		}
		s.stdReply(c, "FAIL", spamCommand(t), "SPAMFILTER_BLOCKED", "Your message to "+orStr(to, "the server")+" was blocked: "+reason, "")
		return spamStop
	case spamfilter.Kill:
		s.quit(c, "Spam filter: "+reason)
		return spamGone
	case spamfilter.TempShun:
		c.tempShun = true
		return spamGone
	case spamfilter.Shun:
		s.svc.SpamBan(store.BanShun, c.ip, reason, r.BanTime)
		return spamGone
	case spamfilter.Gline, spamfilter.Kline:
		s.svc.SpamBan(store.BanAkill, c.ip, reason, r.BanTime)
		s.quit(c, "Banned: "+reason)
		return spamGone
	case spamfilter.Gzline, spamfilter.Zline:
		s.svc.SpamBan(store.BanZline, c.ip, reason, r.BanTime)
		s.quit(c, "Z-lined: "+reason)
		return spamGone
	}
	return spamPass
}

// spamUser checks nick!user@host:realname (connect or nick change) and
// reports whether c may go on with nick.
func (s *Server) spamUser(c *Client, nick string) bool {
	set := s.svc.SpamSet()
	if set.Empty(spamfilter.User) || c.isOper() || s.svc.SpamExemptMask(c.ident, c.host, c.ip) {
		return true
	}
	ident, host, realname := c.ident, c.host, c.realname
	if c.user != nil {
		ident, host, realname = c.user.TrueIdent(), c.user.RealHost, c.user.Realname
	}
	r := set.First(spamfilter.User, nick+"!"+ident+"@"+host+":"+realname)
	if r == nil || !spamfilter.Known(r.Action) {
		return true
	}
	switch out := s.spamDo(c, r, spamfilter.User, "", nick); {
	case out == spamPass:
		return true
	case c.closed:
		return false
	case !c.registered && r.Action == spamfilter.Block:
		s.quit(c, "Spam filter: "+r.Reason) // a blocked nick can't connect
		return false
	case !c.registered && (r.Action == spamfilter.TempShun || r.Action == spamfilter.Shun):
		return true // in, but shunned
	case c.registered && r.Action == spamfilter.Block:
		s.reply(c, irc.ErrErroneusNickname, nick, "Nick refused by the spam filter: "+r.Reason)
	}
	return false
}

// actionName is a rule's action for oper notices: "AKILL 1d", "Block".
func actionName(f spamfilter.Filter) string {
	return services.ActionWords(f)
}

// spamCommand is the command a spam filter target letter comes from.
func spamCommand(t byte) string {
	switch t {
	case spamfilter.Channel, spamfilter.Private:
		return "PRIVMSG"
	case spamfilter.ChannelNotice, spamfilter.PrivateNotice:
		return "NOTICE"
	case spamfilter.Topic:
		return "TOPIC"
	case spamfilter.Away:
		return "AWAY"
	}
	return "*"
}
