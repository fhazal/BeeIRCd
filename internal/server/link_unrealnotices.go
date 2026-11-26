package server

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// Server notices from UnrealIRCd: SENDSNO (a snomask notice from one
// server) and SLOG (a server's log line, sent to the whole network). Both
// go on to every other link that is not an InspIRCd bridge, as UnrealIRCd
// passes them on.

// unrealSno maps an UnrealIRCd snomask letter to ours. Letters that mean
// the same keep their letter; the far kinds become the REMOTE ones, TKL and
// Q-line news is "bans", spamfilter is f, and the rest (flood, junk, eyes,
// vhosts, DCC, the generic s ...) is announcements.
func unrealSno(l byte) byte {
	switch l {
	case 'c', 'k', 'n', 'N', 'o':
		return l
	case 'F':
		return 'C'
	case 'G', 'q':
		return 'X'
	case 'S':
		return 'f'
	}
	return 'a'
}

// snoticeAny sends text to every oper with +s whose snomask has any of the
// letters, once each.
func (s *Server) snoticeAny(letters []byte, text string) {
	if len(letters) == 0 {
		return
	}
	for c := range s.opers {
		for _, l := range letters {
			if c.wantsSno(l) {
				s.notice(c, "*** "+snoCategory(letters[0])+": "+text)
				break
			}
		}
	}
}

// noticeLinks sends the line on to every link but except that is not an
// InspIRCd bridge.
func (s *Server) noticeLinks(except *linkConn, m irc.Message) {
	line := m.String()
	for _, o := range s.upLinks(except) {
		if o.bridge == nil {
			o.send(line)
		}
	}
}

// remoteSendSno is ":sid SENDSNO <letters> :text" from a server: the opers
// here whose mask has any of the letters get the text, the other links
// get it on.
func (s *Server) remoteSendSno(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 || m.Params[1] == "" {
		return
	}
	if srv, u, ok := s.linkSource(l, m.Source); !ok || u != nil || srv == nil {
		return // only a server sends it
	}
	var letters []byte
	for i := 0; i < len(m.Params[0]); i++ {
		letters = append(letters, unrealSno(m.Params[0][i]))
	}
	s.snoticeAny(letters, irc.Truncate(m.Params[1], 400))
	s.noticeLinks(l, m)
}

// slogLetter is the notice letter for a log line from another server, by
// its subsystem: connects, quits, nick changes, links, opers, kills and
// bans have theirs (the far kind), anything else is an announcement.
func slogLetter(subsystem string) byte {
	switch strings.ToLower(subsystem) {
	case "connect":
		return 'C'
	case "quit":
		return 'Q'
	case "nick":
		return 'N'
	case "link":
		return 'L'
	case "oper":
		return 'O'
	case "kill":
		return 'K'
	case "tkl":
		return 'X'
	}
	return 'a'
}

// remoteSlog is ":sid SLOG level subsystem event_id :text" (with the
// unrealircd.org/json-log tag) from a server: the first line of a log
// message, which UnrealIRCd shows through its own logging settings. Here it
// goes to the opers with the notice letter for its subsystem, and on to the
// other links with its tag.
func (s *Server) remoteSlog(l *linkConn, m irc.Message) {
	if len(m.Params) < 4 || m.Params[3] == "" {
		return
	}
	if srv, u, ok := s.linkSource(l, m.Source); !ok || u != nil || srv == nil {
		return
	}
	s.snoticeAny([]byte{slogLetter(m.Params[1])}, irc.Truncate(m.Params[3], 400))
	s.noticeLinks(l, m)
}
