package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/version"
)

func (s *Server) cmdWho(c *Client, m irc.Message) {
	mask := m.Param(0)
	if mask == "" {
		mask = "*"
	}
	wx, opersOnly := parseWhox(m.Param(1))
	if irc.IsChannel(mask) {
		if ch := s.net.Channel(mask); ch != nil {
			inside := ch.Member(c.user) != nil || c.can('h')
			if inside || !(ch.HasMode('s') || ch.HasMode('p')) {
				for _, mem := range ch.SortedMembers() {
					if (!inside && mem.User.HasMode('i')) || !visibleTo(mem, c.user) {
						continue
					}
					if opersOnly && !mem.User.HasMode('o') {
						continue
					}
					s.whoLine(c, ch.Name, mem.User, memberPrefix(c, mem), wx)
				}
			}
		}
		s.reply(c, irc.RplEndOfWho, mask, "End of /WHO list.")
		return
	}
	if mask == "0" {
		mask = "*"
	}
	// An exact nick always shows that user, +i or not (like WHOIS).
	if !strings.ContainsAny(mask, "*?") {
		if u := s.net.UserByNick(mask); u != nil {
			if !opersOnly || u.HasMode('o') {
				s.whoLine(c, "*", u, "", wx)
			}
			s.reply(c, irc.RplEndOfWho, m.Param(0), "End of /WHO list.")
			return
		}
	}
	for _, u := range s.net.Users() {
		if u != c.user && u.HasMode('i') && !state.SharesChannel(u, c.user) && !c.can('h') {
			continue
		}
		if opersOnly && !u.HasMode('o') {
			continue
		}
		if irc.Match(mask, u.Nick) || irc.Match(mask, u.Host) || irc.Match(mask, u.Realname) || irc.Match(mask, u.Server) {
			s.whoLine(c, "*", u, "", wx)
		}
	}
	s.reply(c, irc.RplEndOfWho, m.Param(0), "End of /WHO list.")
}

// whox is what a WHOX request (WHO <mask> [flags]%<fields>[,<token>])
// asks for; nil for a plain WHO.
type whox struct {
	fields string // the asked field letters, in the order replies use
	token  string
}

// whoxOrder is the order of WHOX fields in a 354 reply.
const whoxOrder = "tcuihsnfdlaor"

// parseWhox reads WHO's second parameter: "o" (opers only) and, after a
// %, the WHOX fields and token.
func parseWhox(opt string) (*whox, bool) {
	flags, rest, ok := strings.Cut(opt, "%")
	opersOnly := strings.Contains(flags, "o")
	if !ok {
		return nil, opersOnly
	}
	letters, token, _ := strings.Cut(rest, ",")
	w := &whox{token: "0"}
	for _, f := range whoxOrder {
		if strings.ContainsRune(letters, f) {
			w.fields += string(f)
		}
	}
	if n := len(token); n >= 1 && n <= 3 && strings.Trim(token, "0123456789") == "" {
		w.token = token
	}
	return w, opersOnly
}

func (s *Server) whoLine(c *Client, channel string, u *state.User, prefix string, wx *whox) {
	flags := "H"
	if u.Away != "" {
		flags = "G"
	}
	if u.HasMode('o') && (!u.HasMode('H') || u == c.user || c.isOper()) {
		flags += "*"
	}
	if u.HasMode('B') {
		flags += "B"
	}
	flags += prefix
	if wx == nil {
		s.reply(c, irc.RplWhoReply, channel, u.Ident, u.Host, u.Server, u.Nick, flags, "0 "+u.Realname)
		return
	}
	out := make([]string, 0, len(wx.fields))
	for _, f := range wx.fields {
		switch f {
		case 't':
			out = append(out, wx.token)
		case 'c':
			out = append(out, channel)
		case 'u':
			out = append(out, u.Ident)
		case 'i':
			ip := "255.255.255.255" // only you and opers see a real address
			if (u == c.user || c.isOper()) && u.IP != "" {
				ip = u.IP
			}
			out = append(out, ip)
		case 'h':
			out = append(out, u.Host)
		case 's':
			out = append(out, u.Server)
		case 'n':
			out = append(out, u.Nick)
		case 'f':
			out = append(out, flags)
		case 'd':
			out = append(out, "0")
		case 'l':
			idle := "0"
			if tc := s.clientOf(u); tc != nil {
				idle = strconv.FormatInt(int64(time.Since(tc.lastMsg).Seconds()), 10)
			}
			out = append(out, idle)
		case 'a':
			acct := u.Account
			if acct == "" {
				acct = "0"
			}
			out = append(out, acct)
		case 'o':
			out = append(out, "n/a")
		case 'r':
			out = append(out, u.Realname)
		}
	}
	// r (the realname, which may hold spaces) is always last when asked.
	if strings.HasSuffix(wx.fields, "r") {
		s.reply(c, irc.RplWhoSpcRpl, out...)
		return
	}
	s.replyBare(c, irc.RplWhoSpcRpl, out...)
}

func (s *Server) cmdWhois(c *Client, m irc.Message) {
	nicks := m.Params[len(m.Params)-1]
	nick := strings.SplitN(nicks, ",", 2)[0]
	if len(m.Params) >= 2 && s.whoisElsewhere(c, m.Params[0], nick) {
		return // "WHOIS server nick" / "WHOIS nick nick": that server answers
	}
	u := s.net.UserByNick(nick)
	reply := func(code string, p ...string) { s.reply(c, code, p...) }
	if u == nil {
		reply(irc.ErrNoSuchNick, nick, "No such nick/channel")
		reply(irc.RplEndOfWhois, nick, "End of /WHOIS list.")
		return
	}
	s.whoisOne(c, u, reply)
}

// whoisOne answers a WHOIS on u for c (ours, or a stand-in for someone on
// another server) through reply.
func (s *Server) whoisOne(c *Client, u *state.User, reply func(code string, p ...string)) {
	reply(irc.RplWhoisUser, u.Nick, u.Ident, u.Host, "*", u.Realname)
	if chans := s.whoisChannels(c, u); chans != "" {
		reply(irc.RplWhoisChannels, u.Nick, chans)
	}
	if (u == c.user || c.can('h')) && !u.Service && (u.Host != u.RealHost || u != c.user) {
		reply(irc.RplWhoisHost, u.Nick, "is connecting from *@"+u.RealHost+" "+u.IP)
	}
	if tc := s.clientOf(u); tc != nil && tc.webirc != "" && (u == c.user || c.can('h')) {
		reply(irc.RplWhoisSpecial, u.Nick, "is connecting through WEBIRC gateway "+tc.webirc)
	}
	reply(irc.RplWhoisServer, u.Nick, u.Server, s.serverDescription(u.Server))
	if u.Away != "" {
		reply(irc.RplAway, u.Nick, u.Away)
	}
	if u.HasMode('B') {
		reply(irc.RplWhoisBot, u.Nick, "is a Bot on "+s.cfg.Server.Network)
	}
	tc := s.clientOf(u)
	switch {
	case u.Service:
		reply(irc.RplWhoisOperator, u.Nick, "is a Network Service")
	case tc != nil && tc.isOper() && (!u.HasMode('H') || u == c.user || c.isOper()):
		reply(irc.RplWhoisOperator, u.Nick, whoisOperText(tc.operTitle))
		for _, line := range tc.operSwhois {
			reply(irc.RplWhoisSpecial, u.Nick, line)
		}
	case tc == nil && u.Modes['o'] && (!u.HasMode('H') || c.isOper()):
		reply(irc.RplWhoisOperator, u.Nick, whoisOperText(""))
	}
	if u.HasMode('h') && !u.Service {
		reply(irc.RplWhoisHelpOp, u.Nick, "is available for help")
	}
	if u.Account != "" {
		reply(irc.RplWhoisAccount, u.Nick, u.Account, "is logged in as")
	}
	if u.TLS {
		reply(irc.RplWhoisSecure, u.Nick, "is using a secure connection")
	}
	if u.CertFP != "" && (u == c.user || c.isOper()) {
		reply(irc.RplWhoisCertFP, u.Nick, "has client certificate fingerprint "+u.CertFP)
	}
	if tc != nil {
		idle := int64(time.Since(tc.lastMsg).Seconds())
		reply(irc.RplWhoisIdle, u.Nick, strconv.FormatInt(idle, 10), strconv.FormatInt(u.SignOn.Unix(), 10), "seconds idle, signon time")
	}
	reply(irc.RplEndOfWhois, u.Nick, "End of /WHOIS list.")
	if u.HasMode('W') && u != c.user && tc != nil { // +W: opers see who looks them up
		me := c.user
		s.notice(tc, "*** "+me.Nick+" ("+me.TrueIdent()+"@"+me.RealHost+") did a /whois on you.")
	}
}

// whoisChannels lists u's channels that c may see.
func (s *Server) whoisChannels(c *Client, u *state.User) string {
	asker := c.user
	if u.HasMode('p') && asker != u && !c.isOper() {
		return "" // +p: channels stay private (UnrealIRCd)
	}
	var out []string
	for _, ch := range s.net.Channels() {
		mem := ch.Member(u)
		if mem == nil || (mem.Hidden && asker != u && !c.can('h')) {
			continue
		}
		hidden := ch.HasMode('s') || ch.HasMode('p')
		if hidden && asker != u && ch.Member(asker) == nil && !c.can('h') {
			continue
		}
		out = append(out, memberPrefix(c, mem)+ch.Name)
	}
	return strings.Join(out, " ")
}

type whowasEntry struct {
	nick, ident, host, realname, server string
	at                                  time.Time
}

const whowasSize = 1000

func (s *Server) addWhowas(u *state.User) { s.addWhowasNick(u.Prefix(), u) }

// addWhowasNick records the identity in prefix (nick!ident@host).
func (s *Server) addWhowasNick(prefix string, u *state.User) {
	nick, rest, _ := strings.Cut(prefix, "!")
	ident, host, _ := strings.Cut(rest, "@")
	s.whowas = append(s.whowas, whowasEntry{nick, ident, host, u.Realname, u.Server, time.Now()})
	if len(s.whowas) > whowasSize {
		s.whowas = s.whowas[len(s.whowas)-whowasSize:]
	}
}

func (s *Server) cmdWhowas(c *Client, m irc.Message) {
	nick := m.Params[0]
	// "WHOWAS nick [count]": count > 0 limits the answer (RFC 2812), at
	// most 10 entries either way.
	limit := 10
	if n, err := strconv.Atoi(m.Param(1)); err == nil && n > 0 && n < limit {
		limit = n
	}
	found := 0
	for i := len(s.whowas) - 1; i >= 0 && found < limit; i-- {
		e := s.whowas[i]
		if !irc.EqualFold(e.nick, nick) {
			continue
		}
		found++
		s.reply(c, irc.RplWhoWasUser, e.nick, e.ident, e.host, "*", e.realname)
		s.reply(c, irc.RplWhoisServer, e.nick, e.server, e.at.UTC().Format("Mon Jan 2 15:04:05 2006 UTC"))
	}
	if found == 0 {
		s.reply(c, irc.ErrWasNoSuchNick, nick, "There was no such nickname")
	}
	s.reply(c, irc.RplEndOfWhoWas, nick, "End of WHOWAS")
}

func (s *Server) cmdAway(c *Client, m irc.Message) {
	u := c.user
	msg := irc.Truncate(m.Param(0), s.cfg.Limits.AwayLen)
	s.emit(&state.Away{UID: u.UID, Message: msg})
	if msg != "" {
		s.reply(c, irc.RplNowAway, "You have been marked as being away")
	} else {
		s.reply(c, irc.RplUnAway, "You are no longer marked as being away")
	}

}

func (s *Server) cmdIson(c *Client, m irc.Message) {
	var on []string
	for _, p := range m.Params {
		for _, nick := range strings.Fields(p) {
			if u := s.net.UserByNick(nick); u != nil {
				on = append(on, u.Nick)
			}
		}
	}
	s.reply(c, irc.RplIsOn, strings.Join(on, " "))
}

func (s *Server) cmdUserhost(c *Client, m irc.Message) {
	var out []string
	for _, p := range m.Params {
		for _, nick := range strings.Fields(p) {
			if len(out) == 5 {
				break
			}
			u := s.net.UserByNick(nick)
			if u == nil {
				continue
			}
			entry := u.Nick
			if u.HasMode('o') {
				entry += "*"
			}
			if u.Away != "" {
				entry += "=-"
			} else {
				entry += "=+"
			}
			host := u.Host
			if u == c.user {
				host = u.RealHost
			}
			out = append(out, entry+u.Ident+"@"+host)
		}
	}
	s.reply(c, irc.RplUserHost, strings.Join(out, " "))
}

func (s *Server) cmdMotd(c *Client, m irc.Message) {
	if len(m.Params) > 0 && s.queryElsewhere(c, "MOTD", m.Params[0], "") {
		return
	}
	s.motd(c)
}
func (s *Server) cmdLusers(c *Client, m irc.Message) { s.lusers(c) }

func (s *Server) cmdVersion(c *Client, m irc.Message) {
	if len(m.Params) > 0 && s.queryElsewhere(c, "VERSION", m.Params[0], "") {
		return
	}
	s.reply(c, irc.RplVersion, version.Name+"-"+version.Version+".", s.cfg.Server.Name, version.String())
	s.sendISupport(c)
}

func (s *Server) cmdTime(c *Client, m irc.Message) {
	if len(m.Params) > 0 && s.queryElsewhere(c, "TIME", m.Params[0], "") {
		return
	}
	s.reply(c, irc.RplTime, s.cfg.Server.Name, time.Now().Format("Monday January 2 2006 -- 15:04:05 -07:00"))
}
