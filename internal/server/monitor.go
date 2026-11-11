package server

import (
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// MONITOR (IRCv3): a client keeps a list of nicks and hears when one comes
// online or goes away. The list lives on the connection only. Events
// drive it, so remote users count the same once servers link.

const monitorMax = 100

func init() {
	commands["MONITOR"] = command{1, false, (*Server).cmdMonitor}
}

func (s *Server) cmdMonitor(c *Client, m irc.Message) {
	switch strings.ToUpper(m.Params[0]) {
	case "+":
		if len(m.Params) < 2 {
			s.reply(c, irc.ErrNeedMoreParams, "MONITOR", "Not enough parameters")
			return
		}
		var online, offline, full []string
		for _, nick := range strings.Split(m.Params[1], ",") {
			if nick == "" || !irc.ValidNick(nick, s.cfg.Limits.NickLen) {
				continue
			}
			key := irc.Fold(nick)
			if _, have := c.monitor[key]; !have {
				if len(c.monitor) >= monitorMax {
					full = append(full, nick)
					continue
				}
				if c.monitor == nil {
					c.monitor = make(map[string]string)
				}
				c.monitor[key] = nick
				w := s.watchers[key]
				if w == nil {
					w = make(map[*Client]bool)
					s.watchers[key] = w
				}
				w[c] = true
			}
			if u := s.net.UserByNick(nick); u != nil {
				online = append(online, u.Prefix())
			} else {
				offline = append(offline, nick)
			}
		}
		s.monitorReply(c, irc.RplMonOnline, online)
		s.monitorReply(c, irc.RplMonOffline, offline)
		if len(full) > 0 {
			s.replyBare(c, irc.ErrMonListFull, strconv.Itoa(monitorMax), strings.Join(full, ","), "Monitor list is full.")
		}
	case "-":
		if len(m.Params) < 2 {
			s.reply(c, irc.ErrNeedMoreParams, "MONITOR", "Not enough parameters")
			return
		}
		for _, nick := range strings.Split(m.Params[1], ",") {
			s.unwatch(c, irc.Fold(nick))
		}
	case "C":
		for key := range c.monitor {
			s.unwatch(c, key)
		}
	case "L":
		var list []string
		for _, nick := range c.monitor {
			list = append(list, nick)
		}
		s.monitorReply(c, irc.RplMonList, list)
		s.reply(c, irc.RplEndOfMonList, "End of MONITOR list")
	case "S":
		var online, offline []string
		for _, nick := range c.monitor {
			if u := s.net.UserByNick(nick); u != nil {
				online = append(online, u.Prefix())
			} else {
				offline = append(offline, nick)
			}
		}
		s.monitorReply(c, irc.RplMonOnline, online)
		s.monitorReply(c, irc.RplMonOffline, offline)
	default:
		s.reply(c, irc.ErrUnknownCommand, "MONITOR", "Unknown MONITOR subcommand")
	}
}

// monitorReply sends targets in as few lines as fit.
func (s *Server) monitorReply(c *Client, code string, targets []string) {
	for len(targets) > 0 {
		line, n := "", 0
		for n < len(targets) && len(line)+len(targets[n])+1 < 400 {
			if line != "" {
				line += ","
			}
			line += targets[n]
			n++
		}
		if n == 0 {
			n, line = 1, targets[0]
		}
		s.reply(c, code, line)
		targets = targets[n:]
	}
}

func (s *Server) unwatch(c *Client, key string) {
	if _, ok := c.monitor[key]; !ok {
		return
	}
	delete(c.monitor, key)
	if w := s.watchers[key]; w != nil {
		delete(w, c)
		if len(w) == 0 {
			delete(s.watchers, key)
		}
	}
}

// monitorDrop forgets everything a closing client watched.
func (s *Server) monitorDrop(c *Client) {
	for key := range c.monitor {
		s.unwatch(c, key)
	}
}

// monitorOnline and monitorOffline tell the watchers of a nick.
func (s *Server) monitorOnline(u *state.User) {
	for c := range s.watchers[irc.Fold(u.Nick)] {
		s.reply(c, irc.RplMonOnline, u.Prefix())
	}
}

func (s *Server) monitorOffline(nick string) {
	for c := range s.watchers[irc.Fold(nick)] {
		s.reply(c, irc.RplMonOffline, nick)
	}
}

// monitorPrepare runs before ev changes the network and returns what tells
// watchers afterwards.
func (s *Server) monitorPrepare(ev state.Event) func() {
	if len(s.watchers) == 0 {
		return nil
	}
	switch e := ev.(type) {
	case *state.UserAdd:
		return func() {
			if u := s.net.UserByUID(e.User.UID); u != nil && !u.Service {
				s.monitorOnline(u)
			}
		}
	case *state.UserQuit:
		if u := s.net.UserByUID(e.UID); u != nil {
			nick := u.Nick
			return func() { s.monitorOffline(nick) }
		}
	case *state.NickChange:
		if u := s.net.UserByUID(e.UID); u != nil && !irc.EqualFold(u.Nick, e.Nick) {
			old := u.Nick
			return func() {
				s.monitorOffline(old)
				if nu := s.net.UserByUID(e.UID); nu != nil {
					s.monitorOnline(nu)
				}
			}
		}
	}
	return nil
}
