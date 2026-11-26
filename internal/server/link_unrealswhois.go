package server

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// UnrealIRCd's SWHOIS: extra WHOIS lines on a user, each with the tag of
// whoever set it and a priority. We keep them on the user, show them as 320
// lines in WHOIS, and pass them on; our opers' own extra lines go to
// UnrealIRCd the same way (tag "beeoper").

const maxSwhoisLines = 10

// swhoisLine writes one SWHOIS for a link: add or delete (text "*" removes
// every line of the tag).
func swhoisLine(src, uid string, add bool, tag string, prio int, text string) string {
	sign := "-"
	if add {
		sign = "+"
	}
	return irc.Message{Source: src, Command: "SWHOIS", Params: []string{uid, sign, tag, strconv.Itoa(prio), text}, Trailing: true}.String()
}

func cleanSwhois(t string) string {
	return irc.Truncate(strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, t)), 200)
}

// remoteSwhois is ":src SWHOIS target + tag prio :text" (add), "... - tag
// prio :text" (delete; text "*" = all of the tag) or the old
// ":src SWHOIS target :text" (empty text deletes). From a server or an oper
// only. A change goes on to the other links that are not InspIRCd's.
func (s *Server) remoteSwhois(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 {
		return
	}
	srv, from, ok := s.linkSource(l, m.Source)
	if !ok || (from == nil && srv == nil) || (from != nil && !from.Modes['o']) {
		return
	}
	u := s.userByUIDOrNick(m.Params[0])
	if u == nil {
		return
	}
	var tag, text string
	var prio int
	var add bool
	if len(m.Params) > 4 && m.Params[4] != "" {
		add = m.Params[1] == "+"
		tag = irc.Truncate(m.Params[2], 64)
		prio, _ = strconv.Atoi(m.Params[3])
		text = m.Params[4]
	} else {
		tag = m.Source
		if srv != nil {
			tag = srv.name
		}
		if text = m.Params[1]; text == "" {
			text = "*"
		} else {
			add = true
		}
	}
	changed := false
	if add {
		text = cleanSwhois(text)
		if text == "" || len(u.Swhois) >= maxSwhoisLines {
			return
		}
		for _, w := range u.Swhois {
			if w.Text == text {
				return
			}
		}
		lines := append(append([]state.SwhoisLine(nil), u.Swhois...), state.SwhoisLine{Tag: tag, Priority: prio, Text: text})
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].Priority < lines[j].Priority })
		u.Swhois, changed = lines, true
	} else {
		var keep []state.SwhoisLine
		for _, w := range u.Swhois {
			if w.Tag == tag && (text == "*" || w.Text == text) {
				changed = true
				continue
			}
			keep = append(keep, w)
		}
		u.Swhois = keep
	}
	if changed {
		s.noticeLinks(l, m)
	}
}

// swhoisBurst is the extra WHOIS lines of everyone not behind l, for a link
// that understands them (not InspIRCd's).
func (s *Server) swhoisBurst(l *linkConn) []string {
	if l.bridge != nil {
		return nil
	}
	var out []string
	for _, u := range s.net.Users() {
		if len(u.Swhois) == 0 || s.behind(l, uidServer(u.UID)) {
			continue
		}
		for _, w := range u.Swhois {
			out = append(out, swhoisLine(s.cfg.Server.SID, u.UID, true, w.Tag, w.Priority, w.Text))
		}
	}
	return out
}
