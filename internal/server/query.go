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
	opersOnly := m.Param(1) == "o"
	if irc.IsChannel(mask) {
		if ch := s.net.Channel(mask); ch != nil {
			inside := ch.Member(c.user) != nil
			if inside || !(ch.HasMode('s') || ch.HasMode('p')) {
				for _, mem := range ch.SortedMembers() {
					if !inside && mem.User.HasMode('i') {
						continue
					}
					if opersOnly && !mem.User.HasMode('o') {
						continue
					}
					s.whoLine(c, ch.Name, mem.User, mem.Prefix())
				}
			}
		}
		s.reply(c, irc.RplEndOfWho, mask, "End of /WHO list.")
		return
	}
	if mask == "0" {
		mask = "*"
	}
	for _, u := range s.net.Users() {
		if u != c.user && u.HasMode('i') && !state.SharesChannel(u, c.user) {
			continue
		}
		if opersOnly && !u.HasMode('o') {
			continue
		}
		if irc.Match(mask, u.Nick) || irc.Match(mask, u.Host) || irc.Match(mask, u.Realname) || irc.Match(mask, u.Server) {
			s.whoLine(c, "*", u, "")
		}
	}
	s.reply(c, irc.RplEndOfWho, m.Param(0), "End of /WHO list.")
}

func (s *Server) whoLine(c *Client, channel string, u *state.User, prefix string) {
	flags := "H"
	if u.Away != "" {
		flags = "G"
	}
	if u.HasMode('o') {
		flags += "*"
	}
	flags += prefix
	s.reply(c, irc.RplWhoReply, channel, u.Ident, u.Host, u.Server, u.Nick, flags, "0 "+u.Realname)
}

func (s *Server) cmdWhois(c *Client, m irc.Message) {
	// "WHOIS server nick" asks a given server; there is only this one.
	nicks := m.Params[len(m.Params)-1]
	for _, nick := range strings.SplitN(nicks, ",", 2)[:1] {
		u := s.net.UserByNick(nick)
		if u == nil {
			s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
			s.reply(c, irc.RplEndOfWhois, nick, "End of /WHOIS list.")
			continue
		}
		s.reply(c, irc.RplWhoisUser, u.Nick, u.Ident, u.Host, "*", u.Realname)
		if chans := s.whoisChannels(c.user, u); chans != "" {
			s.reply(c, irc.RplWhoisChannels, u.Nick, chans)
		}
		s.reply(c, irc.RplWhoisServer, u.Nick, u.Server, s.cfg.Server.Description)
		if u.Away != "" {
			s.reply(c, irc.RplAway, u.Nick, u.Away)
		}
		if u.TLS {
			s.reply(c, irc.RplWhoisSecure, u.Nick, "is using a secure connection")
		}
		if tc := s.clientOf(u); tc != nil {
			idle := int64(time.Since(tc.lastMsg).Seconds())
			s.reply(c, irc.RplWhoisIdle, u.Nick, strconv.FormatInt(idle, 10), strconv.FormatInt(u.SignOn.Unix(), 10), "seconds idle, signon time")
		}
		s.reply(c, irc.RplEndOfWhois, u.Nick, "End of /WHOIS list.")
	}
}

// whoisChannels lists u's channels that asker may see.
func (s *Server) whoisChannels(asker, u *state.User) string {
	var out []string
	for _, ch := range s.net.Channels() {
		mem := ch.Member(u)
		if mem == nil {
			continue
		}
		hidden := ch.HasMode('s') || ch.HasMode('p')
		if hidden && asker != u && ch.Member(asker) == nil {
			continue
		}
		out = append(out, mem.Prefix()+ch.Name)
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
	found := 0
	for i := len(s.whowas) - 1; i >= 0 && found < 10; i-- {
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
	if msg := m.Param(0); msg != "" {
		c.user.Away = irc.Truncate(msg, s.cfg.Limits.AwayLen)
		s.reply(c, irc.RplNowAway, "You have been marked as being away")
		return
	}
	c.user.Away = ""
	s.reply(c, irc.RplUnAway, "You are no longer marked as being away")
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

func (s *Server) cmdMotd(c *Client, m irc.Message)   { s.motd(c) }
func (s *Server) cmdLusers(c *Client, m irc.Message) { s.lusers(c) }

func (s *Server) cmdVersion(c *Client, m irc.Message) {
	s.reply(c, irc.RplVersion, version.Name+"-"+version.Version+".", s.cfg.Server.Name, version.String())
	s.sendISupport(c)
}

func (s *Server) cmdTime(c *Client, m irc.Message) {
	s.reply(c, irc.RplTime, s.cfg.Server.Name, time.Now().Format("Monday January 2 2006 -- 15:04:05 -07:00"))
}
