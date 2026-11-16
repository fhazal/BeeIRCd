package server

import (
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// DCC file rules (UnrealIRCd's deny dcc / allow dcc and DCCALLOW):
// [[deny_dcc]] blocks stop DCC SEND of files by name. A soft block only
// holds until the receiver has put the sender on their DCCALLOW list.

const (
	dccAllowMax     = 10
	rplDccStatus    = "617"
	rplDccList      = "618"
	rplEndOfDccList = "619"
	rplDccInfo      = "620"
	errTooManyDcc   = "514"
)

func init() {
	commands["DCCALLOW"] = command{0, false, (*Server).cmdDccAllow}
	helpTopics["DCCALLOW"] = []string{"DCCALLOW [+nick][,-nick] | LIST | HELP",
		"Some file types are only delivered by DCC from people you trust: +nick lets someone send them to you, -nick takes that back, LIST shows your list (at most " + strconv.Itoa(dccAllowMax) + ").",
		"The list lasts until you disconnect."}
}

var dccAllowHelp = []string{
	"/DCCALLOW [<+|->nick[,<+|->nick, ...]] [list] [help]",
	"Some kinds of files are only delivered from people you have allowed.",
	"To let Bob send you such files:",
	"/DCCALLOW +Bob",
	"Bob has to send the file again if it was refused before you added them.",
	"/DCCALLOW -Bob",
	"takes Bob off your list again.",
	"/DCCALLOW list",
	"shows who is on your list.",
}

// dccFile is the file name in a "DCC SEND" request, without its path.
func dccFile(text string) (string, bool) {
	const lead = "\x01DCC SEND "
	if len(text) <= len(lead) || !strings.EqualFold(text[:len(lead)], lead) {
		return "", false
	}
	rest := strings.TrimLeft(text[len(lead):], " ")
	name := ""
	if strings.HasPrefix(rest, "\"") {
		name, _, _ = strings.Cut(rest[1:], "\"")
	} else {
		name, _, _ = strings.Cut(rest, " ")
	}
	name = strings.TrimRight(name, "\x01")
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	return name, name != ""
}

// dccDenied is the [[deny_dcc]] block (hard or soft) that stops file,
// nil when none does or an [[allow_dcc]] block lets it through.
func (s *Server) dccDenied(file string, soft bool) *config.DenyDCC {
	for _, a := range s.cfg.AllowDCC {
		if a.Soft == soft && irc.Match(a.Filename, file) {
			return nil
		}
	}
	for i, d := range s.cfg.DenyDCC {
		if d.Soft == soft && irc.Match(d.Filename, file) {
			return &s.cfg.DenyDCC[i]
		}
	}
	return nil
}

func dccReason(d *config.DenyDCC) string {
	if d.Reason == "" {
		return "this kind of file is not allowed here"
	}
	return d.Reason
}

// dccSoftBlocked reports whether a DCC SEND of file from from to t (one
// of ours) is held back: a soft block and from isn't on t's DCCALLOW
// list. Both hear about it.
func (s *Server) dccSoftBlocked(from, t *state.User, file string) bool {
	tc := s.clientOf(t)
	if tc == nil || from == nil || from == t || from.Modes['o'] || from.Service || tc.dccAllow[from.UID] {
		return false
	}
	d := s.dccDenied(file, true)
	if d == nil {
		return false
	}
	host{s}.Numeric(from.UID, irc.ErrCantSendToUser, t.Nick, "Cannot DCC SEND file "+file+" ("+dccReason(d)+"): "+t.Nick+" has not allowed files like this from you")
	s.notice(tc, "*** "+from.Nick+" ("+from.Ident+"@"+from.Host+") tried to DCC SEND you a file named '"+file+"', the request has been blocked.")
	if !tc.dccWarned {
		tc.dccWarned = true
		s.notice(tc, "*** Files like these might contain malicious content (viruses, trojans). If you trust "+from.Nick+" and want the file: /DCCALLOW +"+from.Nick+" and ask them to send it again. See /DCCALLOW HELP.")
	}
	return true
}

// cmdDccAllow is DCCALLOW +nick,-nick | LIST | HELP.
func (s *Server) cmdDccAllow(c *Client, m irc.Message) {
	if len(m.Params) == 0 || m.Params[0] == "" {
		s.notice(c, "No command specified for DCCALLOW. Type '/DCCALLOW HELP' for more information.")
		return
	}
	for _, w := range strings.FieldsFunc(m.Params[0], func(r rune) bool { return r == ',' || r == ' ' }) {
		switch {
		case strings.EqualFold(w, "LIST"):
			s.reply(c, rplDccInfo, "The following users are on your dcc allow list:")
			for uid := range c.dccAllow {
				if u := s.net.UserByUID(uid); u != nil {
					s.reply(c, rplDccList, u.Nick+" ("+u.Ident+"@"+u.Host+")")
				} else {
					delete(c.dccAllow, uid)
				}
			}
			s.reply(c, rplEndOfDccList, "End of DCCALLOW LIST")
		case strings.EqualFold(w, "HELP"):
			for _, l := range dccAllowHelp {
				s.reply(c, rplDccInfo, l)
			}
			s.reply(c, rplEndOfDccList, "End of DCCALLOW HELP")
		case w[0] == '+' || w[0] == '-':
			t := s.net.UserByNick(w[1:])
			if t == nil || t.Service {
				s.reply(c, irc.ErrNoSuchNick, w[1:], "No such nick/channel")
				continue
			}
			if t == c.user {
				continue
			}
			if w[0] == '-' {
				if c.dccAllow[t.UID] {
					delete(c.dccAllow, t.UID)
					s.reply(c, rplDccStatus, t.Nick+" has been removed from your DCC allow list")
				}
				continue
			}
			if c.dccAllow[t.UID] {
				continue
			}
			for uid := range c.dccAllow {
				if s.net.UserByUID(uid) == nil {
					delete(c.dccAllow, uid)
				}
			}
			if len(c.dccAllow) >= dccAllowMax {
				s.reply(c, errTooManyDcc, t.Nick, "Your dcc allow list is full. Maximum size is "+strconv.Itoa(dccAllowMax)+" entries")
				continue
			}
			if c.dccAllow == nil {
				c.dccAllow = make(map[string]bool)
			}
			c.dccAllow[t.UID] = true
			s.reply(c, rplDccStatus, t.Nick+" has been added to your DCC allow list")
		}
	}
}
