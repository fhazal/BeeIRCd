package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/store"
)

// IRCv3 draft/read-marker: how far someone has read in a channel or a
// private chat, shared by every connection of their account (on any
// BeeIRCd server of the network). Without an account the marker lives on
// the connection only.

const readMarkerCap = "draft/read-marker"

// markTime is the timestamp= format (UTC, milliseconds).
const markTime = "2006-01-02T15:04:05.000Z"

// markKey is the folded account and target a marker is kept under.
type markKey struct{ account, target string }

// loadReadMarkers fills s.marks from the database.
func (s *Server) loadReadMarkers() {
	s.marks = map[markKey]time.Time{}
	ms, err := s.store.ReadMarkers()
	if err != nil {
		s.log.Printf("services database: read markers: %v", err)
		return
	}
	for _, m := range ms {
		s.marks[markKey{m.Account, m.Target}] = m.At
	}
}

// readMark is c's marker for target (zero when none).
func (s *Server) readMark(c *Client, target string) time.Time {
	if acc := c.user.Account; acc != "" {
		return s.marks[markKey{irc.Fold(acc), irc.Fold(target)}]
	}
	return c.marks[irc.Fold(target)]
}

func markParam(at time.Time) string {
	if at.IsZero() {
		return "*"
	}
	return "timestamp=" + at.UTC().Format(markTime)
}

func (s *Server) sendMark(c *Client, target string, at time.Time) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "MARKREAD", Params: []string{target, markParam(at)}}.String())
}

// joinMark tells a read-marker client how far it read in ch, on JOIN.
func (s *Server) joinMark(c *Client, name string) {
	if c.caps[readMarkerCap] {
		s.sendMark(c, name, s.readMark(c, name))
	}
}

func (s *Server) markFail(c *Client, code string, params ...string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{"MARKREAD", code}, params...), Trailing: true}.String())
}

// cmdMarkread is MARKREAD <target> [timestamp=...].
func (s *Server) cmdMarkread(c *Client, m irc.Message) {
	if !c.caps[readMarkerCap] {
		s.reply(c, irc.ErrUnknownCommand, "MARKREAD", "Unknown command")
		return
	}
	if len(m.Params) == 0 {
		s.markFail(c, "NEED_MORE_PARAMS", "Missing parameters")
		return
	}
	target := m.Params[0]
	if strings.HasPrefix(target, "#") {
		if !irc.ValidChannel(target, s.cfg.Limits.ChannelLen) {
			s.markFail(c, "INVALID_PARAMS", target, "Invalid target")
			return
		}
	} else if !irc.ValidNick(target, s.cfg.Limits.NickLen) {
		s.markFail(c, "INVALID_PARAMS", target, "Invalid target")
		return
	}
	if len(m.Params) == 1 {
		s.sendMark(c, target, s.readMark(c, target))
		return
	}
	v, ok := strings.CutPrefix(m.Params[1], "timestamp=")
	at, err := time.Parse(time.RFC3339Nano, v)
	if !ok || err != nil {
		s.markFail(c, "INVALID_PARAMS", m.Params[1], "Invalid timestamp")
		return
	}
	at = at.UTC().Truncate(time.Millisecond)
	if now := time.Now().UTC().Truncate(time.Millisecond); at.After(now) {
		at = now // a marker in the future would never move again
	}
	acc := c.user.Account
	if acc == "" {
		if c.marks == nil {
			c.marks = map[string]time.Time{}
		}
		if at.After(c.marks[irc.Fold(target)]) {
			c.marks[irc.Fold(target)] = at
		}
		s.sendMark(c, target, c.marks[irc.Fold(target)])
		return
	}
	if !s.setMark(acc, target, at) {
		s.sendMark(c, target, s.readMark(c, target)) // already further on
		return
	}
	s.linkMark(nil, acc, target, at)
}

// setMark moves acc's marker for target to at and tells acc's connections
// here; false when the marker was at or past it already.
func (s *Server) setMark(acc, target string, at time.Time) bool {
	k := markKey{irc.Fold(acc), irc.Fold(target)}
	if !at.After(s.marks[k]) {
		return false
	}
	s.marks[k] = at
	m := store.ReadMarker{Account: k.account, Target: k.target, At: at}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.store.SetReadMarker(m); err != nil {
			s.post(func() { s.log.Printf("services database: read marker: %v", err) })
		}
	}()
	for _, oc := range s.clients {
		if oc.registered && oc.user != nil && oc.caps[readMarkerCap] && irc.Fold(oc.user.Account) == k.account {
			s.sendMark(oc, target, at)
		}
	}
	return true
}

// linkMark passes a marker on to the BeeIRCd servers (all but from).
func (s *Server) linkMark(from *linkConn, acc, target string, at time.Time) {
	it := &link.Bee{Source: s.cfg.Server.SID, Sub: "MARKREAD", Args: []string{acc, target, at.UTC().Format(markTime)}}
	for _, o := range s.upLinks(from) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
}

// remoteBee handles BEE lines from another BeeIRCd server.
func (s *Server) remoteBee(l *linkConn, it *link.Bee) {
	switch it.Sub {
	case "MARKREAD":
		if len(it.Args) < 3 || it.Args[0] == "" || it.Args[1] == "" {
			return
		}
		at, err := time.Parse(time.RFC3339Nano, it.Args[2])
		if err != nil {
			return
		}
		if s.setMark(it.Args[0], it.Args[1], at.UTC().Truncate(time.Millisecond)) {
			s.linkMark(l, it.Args[0], it.Args[1], at)
		}
	case "REDACT":
		s.remoteRedact(l, it)
	case "RENAME":
		s.remoteRename(l, it)
	case "METADATA":
		s.remoteMetadata(l, it)
	case "READONLY":
		s.remoteReadOnly(l, it)
	default:
		for _, o := range s.upLinks(l) { // a newer server's; pass it on
			for _, line := range o.codecOf().Encode(it) {
				o.send(line)
			}
		}
	}
}
