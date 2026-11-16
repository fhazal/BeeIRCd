package server

import (
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// WATCH: the older way to hear when a nick comes online or leaves (many
// clients still use it for their notify list). It shares the index of
// MONITOR; only the list and the numerics are its own (UnrealIRCd's).

const watchMax = 128

const (
	rplLogon          = "600"
	rplLogoff         = "601"
	rplWatchOff       = "602"
	rplWatchStat      = "603"
	rplNowOn          = "604"
	rplNowOff         = "605"
	rplWatchList      = "606"
	rplEndOfWatchList = "607"
	errTooManyWatch   = "512"
)

func init() {
	commands["WATCH"] = command{0, false, (*Server).cmdWatch}
	helpTopics["WATCH"] = []string{"WATCH [+nick] [-nick] [L] [S] [C]",
		"Your notify list, kept on the server: +nick adds, -nick removes, L lists who is online (uppercase: also who is offline), S shows a summary, C clears it.",
		"At most " + strconv.Itoa(watchMax) + " nicks. Newer clients use MONITOR for the same thing."}
}

// watchStatus sends 604 (online) or 605 (offline) for nick; off is the
// numeric used when the nick isn't there.
func (s *Server) watchStatus(c *Client, nick, on, off, onText, offText string) {
	if u := s.net.UserByNick(nick); u != nil && !u.Service {
		s.reply(c, on, u.Nick, u.Ident, u.Host, strconv.FormatInt(u.NickTS, 10), onText)
	} else if off != "" {
		s.reply(c, off, nick, "*", "*", "0", offText)
	}
}

func (s *Server) cmdWatch(c *Client, m irc.Message) {
	var words []string
	for _, p := range m.Params {
		words = append(words, strings.FieldsFunc(p, func(r rune) bool { return r == ' ' || r == ',' })...)
	}
	if len(words) == 0 {
		words = []string{"l"}
	}
	listed := false
	for _, w := range words {
		switch {
		case w[0] == '+':
			nick := w[1:]
			if !irc.ValidNick(nick, s.cfg.Limits.NickLen) {
				continue
			}
			key := irc.Fold(nick)
			if _, have := c.watch[key]; !have {
				if len(c.watch) >= watchMax {
					s.reply(c, errTooManyWatch, nick, "Maximum size for WATCH-list is "+strconv.Itoa(watchMax)+" entries")
					continue
				}
				if c.watch == nil {
					c.watch = make(map[string]string)
				}
				c.watch[key] = nick
				s.indexWatch(c, key)
			}
			s.watchStatus(c, nick, rplNowOn, rplNowOff, "is online", "is offline")
		case w[0] == '-':
			nick := w[1:]
			key := irc.Fold(nick)
			if _, have := c.watch[key]; !have {
				continue
			}
			delete(c.watch, key)
			s.unindexWatch(c, key)
			s.watchStatus(c, nick, rplWatchOff, rplWatchOff, "stopped watching", "stopped watching")
		case w == "C" || w == "c":
			s.watchDrop(c)
		case (w == "S" || w == "s") && !listed:
			listed = true
			on := 0
			for cl := range s.watchers[irc.Fold(c.user.Nick)] {
				if _, ok := cl.watch[irc.Fold(c.user.Nick)]; ok {
					on++
				}
			}
			s.reply(c, rplWatchStat, "You have "+strconv.Itoa(len(c.watch))+" and are on "+strconv.Itoa(on)+" WATCH entries")
			var nicks []string
			for _, n := range c.watch {
				nicks = append(nicks, n)
			}
			for len(nicks) > 0 {
				line, n := "", 0
				for n < len(nicks) && len(line)+len(nicks[n])+1 < 400 {
					line += nicks[n] + " "
					n++
				}
				s.reply(c, rplWatchList, strings.TrimSpace(line))
				nicks = nicks[n:]
			}
			s.reply(c, rplEndOfWatchList, "End of WATCH "+w)
		case (w == "L" || w == "l") && !listed:
			listed = true
			off := ""
			if w == "L" {
				off = rplNowOff
			}
			for _, nick := range c.watch {
				s.watchStatus(c, nick, rplNowOn, off, "is online", "is offline")
			}
			s.reply(c, rplEndOfWatchList, "End of WATCH "+w)
		}
	}
}

// watchDrop empties c's WATCH list.
func (s *Server) watchDrop(c *Client) {
	for key := range c.watch {
		delete(c.watch, key)
		s.unindexWatch(c, key)
	}
}
