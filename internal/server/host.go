package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// host is the server as services see it (services.Host). Everything goes to
// people connected here; once servers link, the link layer carries the
// rest.
type host struct{ s *Server }

var _ services.Host = host{}

func (h host) Emit(ev state.Event) error { return h.s.emit(ev) }

func (h host) Notice(fromUID, toUID, text string) {
	s := h.s
	from, to := s.net.UserByUID(fromUID), s.clients[toUID]
	if from == nil || to == nil || to.user == nil {
		return
	}
	to.deliver(s.out(from, "NOTICE", to.target(), text).text())
}

func (h host) ChannelMessage(fromUID, channel string, status byte, privmsg bool, text string) {
	s := h.s
	from, ch := s.net.UserByUID(fromUID), s.net.Channel(channel)
	if from == nil || ch == nil {
		return
	}
	cmd := "NOTICE"
	if privmsg {
		cmd = "PRIVMSG"
	}
	target, min := ch.Name, state.Rank(0)
	if r := state.RankForPrefix(status); status != 0 && r != 0 {
		target, min = string(status)+ch.Name, r
	}
	o := s.out(from, cmd, target, text).text()
	for _, m := range ch.Members {
		if min == 0 || m.AtLeast(min) {
			s.deliverTo(m.User, o)
		}
	}
}

func (h host) Numeric(uid, code string, params ...string) {
	if c := h.s.clients[uid]; c != nil {
		h.s.reply(c, code, params...)
	}
}

func (h host) ServerNotice(uid, text string) {
	if c := h.s.clients[uid]; c != nil {
		h.s.notice(c, text)
	}
}

func (h host) Snotice(text string) { h.s.snotice("%s", text) }

func (h host) Disconnect(uid, reason string) {
	if c := h.s.clients[uid]; c != nil {
		h.s.quit(c, reason)
	}
}

func (h host) Kill(uid, by, reason string) {
	if u := h.s.net.UserByUID(uid); u != nil {
		h.s.killUser(u, by, reason)
	}
}

func (h host) OperFlags(uid string) string {
	if c := h.s.clients[uid]; c != nil {
		return c.operFlags
	}
	return ""
}

func (h host) AccountGone(id int64) {
	for _, c := range h.s.clients {
		if c.user != nil && c.operAccount == id {
			h.s.deoper(c, "Your account is gone, so you are no longer an operator.")
		}
	}
}

func (h host) IsAdminAccount(a *store.Account) bool { return h.s.isAdminAccount(a) }

func (h host) ApplyChannelModes(uid, srcUID, channel, modes string, args []string) {
	s := h.s
	c, src, ch := s.clients[uid], s.net.UserByUID(srcUID), s.net.Channel(channel)
	if c == nil || src == nil || ch == nil {
		return
	}
	s.applyChannelModes(c, src, ch, servicesRank, modes, args)
}

func (h host) ApplySetting(uid, botUID, key, value string) {
	s := h.s
	c, bot := s.clients[uid], s.net.UserByUID(botUID)
	if c != nil && bot != nil {
		s.applySetting(c, bot, key, value)
	}
}

func (h host) Post(f func()) { h.s.post(f) }

func (h host) Stats() services.HostStats {
	return services.HostStats{Started: h.s.created, PeakUsers: h.s.maxLocal, Loop: h.s.loop.summary()}
}

// botReply sends a notice from a service bot to c.
func (s *Server) botReply(c *Client, bot *state.User, text string) {
	if c.user != nil {
		s.svc.Reply(bot, c.uid, text)
	}
}

// accountID is the NickServ account c is identified to, 0 for none.
func (s *Server) accountID(c *Client) int64 { return s.svc.AccountID(c.uid) }

// /ns, /cs, /ms, /os: the rest of the line goes to the service as if sent
// with /msg.
func (s *Server) cmdNS(c *Client, m irc.Message) { s.toAlias(c, "ns", m) }
func (s *Server) cmdCS(c *Client, m irc.Message) { s.toAlias(c, "cs", m) }
func (s *Server) cmdMS(c *Client, m irc.Message) { s.toAlias(c, "ms", m) }
func (s *Server) cmdOS(c *Client, m irc.Message) { s.toAlias(c, "os", m) }
func (s *Server) cmdAS(c *Client, m irc.Message) { s.toAlias(c, "as", m) }

func (s *Server) toAlias(c *Client, alias string, m irc.Message) {
	s.svc.MessageAlias(c.uid, alias, strings.Join(m.Params, " "))
}

// lockedFor and friends keep the oper password code short.
func (s *Server) lockedFor(key int64) time.Duration { return s.svc.LockedFor(key) }

func (h host) ResetPeak() { h.s.maxLocal = h.s.localUsers() }
