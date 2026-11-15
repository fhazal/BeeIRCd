package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
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
	if from != nil && to == nil {
		s.sendRemote(toUID, &link.Message{Source: fromUID, Verb: "NOTICE", Target: toUID, Text: text})
		return
	}
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
	prefix := ""
	if min != 0 {
		prefix = string(status)
	}
	s.routeChannel(nil, fromUID, ch, prefix, cmd, text, nil)
}

func (h host) Numeric(uid, code string, params ...string) {
	if c := h.s.clients[uid]; c != nil {
		h.s.reply(c, code, params...)
		return
	}
	if (code == irc.RplLoggedIn || code == irc.RplLoggedOut) && !h.s.beeLink(uid) {
		return // UnrealIRCd says it itself when SVSLOGIN arrives
	}
	h.s.sendRemote(uid, &link.Numeric{Source: h.s.cfg.Server.SID, Code: code, Params: append([]string{uid}, params...)})
}

func (h host) ServerNotice(uid, text string) {
	if c := h.s.clients[uid]; c != nil {
		h.s.notice(c, text)
		return
	}
	h.s.sendRemote(uid, &link.Message{Source: h.s.cfg.Server.SID, Verb: "NOTICE", Target: uid, Text: text})
}

func (h host) Snotice(text string) { h.s.snotice("%s", text) }

func (h host) Disconnect(uid, reason string) {
	if c := h.s.clients[uid]; c != nil {
		h.s.quit(c, reason)
		return
	}
	if u := h.s.net.UserByUID(uid); u != nil && h.s.linkOf(uid) != nil {
		h.s.killRemote(u, h.s.cfg.Server.Name, reason)
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
	if u := h.s.net.UserByUID(uid); u != nil && u.Modes['o'] {
		// An oper on another server; "o" = oper, nothing more known.
		return orStr(u.OperFlags, "o")
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

func (h host) CheckModeParam(channel string, m byte, arg string) (string, string) {
	return h.s.checkParamMode(channel, m, arg)
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
	return services.HostStats{Started: h.s.created, PeakUsers: h.s.maxLocal, Loop: h.s.loop.summary(), DNSBL: h.s.dnsblSummary(), Links: h.s.uplinkLines()}
}

// botReply sends a notice from a service bot to c.
func (s *Server) botReply(c *Client, bot *state.User, text string) {
	switch {
	case c.user == nil:
	case c.adminFrom != "" || bot == nil || bot.UID == "":
		// AdminServ run from another server, or no bot here: a notice
		// from this server says which one answers.
		host{s}.ServerNotice(c.uid, "["+s.cfg.Server.Name+" AdminServ] "+text)
	default:
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
func (s *Server) cmdHS(c *Client, m irc.Message) { s.toAlias(c, "hs", m) }
func (s *Server) cmdBS(c *Client, m irc.Message) { s.toAlias(c, "bs", m) }

func (h host) DefaultHost(uid string) string {
	u := h.s.net.UserByUID(uid)
	if u == nil {
		return ""
	}
	if h.s.cfg.CloakEnabled() && u.Modes['x'] {
		return h.s.cloakFor(u)
	}
	return u.RealHost
}

func (s *Server) toAlias(c *Client, alias string, m irc.Message) {
	text := strings.Join(m.Params, " ")
	if s.svc.Off() {
		// Services run on another server: the line goes to its bot.
		nick := aliasBots[alias]
		bot := s.net.UserByNick(nick)
		if bot == nil || s.ours(bot.UID) || strings.TrimSpace(text) == "" {
			s.reply(c, irc.ErrServicesDown, nick, "Services are not available right now")
			return
		}
		s.routeUser(c.uid, bot, "PRIVMSG", text, nil)
		return
	}
	s.svc.MessageAlias(c.uid, alias, text)
}

// aliasBots are the bots behind /ns, /cs ...
var aliasBots = map[string]string{"ns": "NickServ", "cs": "ChanServ", "ms": "MemoServ", "os": "OperServ", "hs": "HostServ", "as": "AdminServ"}

// lockedFor and friends keep the oper password code short.
func (s *Server) lockedFor(key int64, src string) time.Duration { return s.svc.LockedFor(key, src) }

func (h host) ResetPeak() { h.s.maxLocal = h.s.localUsers() }

func (h host) JoinBlocked(uid, channel, key string) (string, bool) {
	s := h.s
	c, ch := s.clients[uid], s.net.Channel(channel)
	if c == nil || c.user == nil || ch == nil {
		return "", false
	}
	code, _, _ := s.joinRefusal(c, ch, key, false)
	switch code {
	case irc.ErrBannedFromChan, irc.ErrInviteOnlyChan, irc.ErrBadChannelKey, irc.ErrChannelIsFull, irc.ErrNeedReggedNick:
		return code, true
	}
	return code, false
}

func (h host) ForceJoin(uid, channel string) {
	s := h.s
	c := s.clients[uid]
	if c == nil && s.net.UserByUID(uid) != nil {
		s.svsJoinRemote(uid, channel) // on another server: it joins them
		return
	}
	if c == nil || c.user == nil {
		return
	}
	if s.emit(&state.Join{UID: uid, Channel: channel, TS: time.Now().Unix()}) != nil {
		return
	}
	if ch := s.net.Channel(channel); ch != nil {
		if ch.Topic != "" {
			s.sendTopic(c, ch)
		}
		s.joinNames(c, ch)
	}
}

func (h host) SetUserModes(uid, srcUID, modes string) bool {
	c := h.s.clients[uid]
	if c == nil || c.user == nil {
		return false
	}
	return h.s.setUserModes(c, srcUID, modes)
}
