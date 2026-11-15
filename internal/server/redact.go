package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/state"
)

// IRCv3 draft/message-redaction: REDACT takes back a message. The sender
// may for redactWindow, channel ops (and opers) at any time. Clients with
// the cap hide it; +H history forgets it; other BeeIRCd servers pass it on.

const (
	redactCap    = "draft/message-redaction"
	redactWindow = 15 * time.Minute
	recentMax    = 100000 // messages remembered for REDACT, at most
)

// recentMsg is who sent a message, for REDACT.
type recentMsg struct {
	uid, account string
	target       string // folded channel, or the folded nick it went to
	at           time.Time
}

// recentMsgs remembers the messages of the last redactWindow.
type recentMsgs struct {
	by    map[string]recentMsg // by msgid
	order []string
}

// noteMsg remembers msgid from u to target.
func (s *Server) noteMsg(msgid string, u *state.User, target string) {
	if msgid == "" || u == nil {
		return
	}
	r := &s.recent
	if r.by == nil {
		r.by = map[string]recentMsg{}
	}
	now := time.Now()
	for len(r.order) > 0 && (len(r.order) >= recentMax || now.Sub(r.by[r.order[0]].at) > redactWindow) {
		delete(r.by, r.order[0])
		r.order = r.order[1:]
	}
	r.by[msgid] = recentMsg{uid: u.UID, account: u.Account, target: irc.Fold(target), at: now}
	r.order = append(r.order, msgid)
}

func (s *Server) redactFail(c *Client, code string, params ...string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{"REDACT", code}, params...), Trailing: true}.String())
}

// cmdRedact is REDACT <target> <msgid> [reason].
func (s *Server) cmdRedact(c *Client, m irc.Message) {
	if !c.caps[redactCap] {
		s.reply(c, irc.ErrUnknownCommand, "REDACT", "Unknown command")
		return
	}
	if len(m.Params) < 2 {
		s.redactFail(c, "NEED_MORE_PARAMS", "Missing parameters")
		return
	}
	u := c.user
	target, msgid, reason := m.Params[0], m.Params[1], m.Param(2)
	reason = irc.Truncate(reason, s.cfg.Limits.KickLen)
	rec, known := s.recent.by[msgid]
	own := known && (rec.uid == u.UID || (u.Account != "" && rec.account == u.Account))
	fresh := known && time.Since(rec.at) <= redactWindow
	if irc.IsChannel(target) {
		ch := s.net.Channel(target)
		if ch == nil || (ch.Member(u) == nil && !u.HasMode('o')) {
			s.redactFail(c, "INVALID_TARGET", target, "You can't redact messages there")
			return
		}
		op := u.HasMode('o')
		if mem := ch.Member(u); mem != nil && mem.AtLeast(state.RankOp) {
			op = true
		}
		if !known {
			l, inHist := s.hist.store.Find(ch.Name, msgid)
			switch {
			case !inHist:
				s.redactFail(c, "UNKNOWN_MSGID", target, msgid, "This message doesn't exist or is too old")
				return
			case !op && u.Account != "" && l.Account == u.Account:
				s.redactFail(c, "REDACT_WINDOW_EXPIRED", target, msgid, strconv.Itoa(int(redactWindow.Seconds())), "Too late: you can redact your messages for "+redactMinutes())
				return
			case !op:
				s.redactFail(c, "REDACT_FORBIDDEN", target, msgid, "You can only redact your own messages")
				return
			}
		} else if rec.target != irc.Fold(ch.Name) {
			s.redactFail(c, "UNKNOWN_MSGID", target, msgid, "This message wasn't sent there")
			return
		} else if !op && !own {
			s.redactFail(c, "REDACT_FORBIDDEN", target, msgid, "You can only redact your own messages")
			return
		} else if !op && !fresh {
			s.redactFail(c, "REDACT_WINDOW_EXPIRED", target, msgid, strconv.Itoa(int(redactWindow.Seconds())), "Too late: you can redact your messages for "+redactMinutes())
			return
		}
		s.redactChannel(nil, u, ch, msgid, reason)
		s.linkRedact(nil, u.UID, ch.Name, msgid, reason)
		return
	}
	t := s.net.UserByNick(target)
	if t == nil {
		s.redactFail(c, "INVALID_TARGET", target, "No such nick")
		return
	}
	switch {
	case !known || rec.target != irc.Fold(t.Nick):
		s.redactFail(c, "UNKNOWN_MSGID", target, msgid, "This message doesn't exist or is too old")
		return
	case !own:
		s.redactFail(c, "REDACT_FORBIDDEN", target, msgid, "You can only redact your own messages")
		return
	case !fresh:
		s.redactFail(c, "REDACT_WINDOW_EXPIRED", target, msgid, strconv.Itoa(int(redactWindow.Seconds())), "Too late: you can redact your messages for "+redactMinutes())
		return
	}
	o := s.redactOut(u, t.Nick, msgid, reason)
	c.deliver(o)
	if s.ours(t.UID) {
		s.deliverTo(t, o)
	} else {
		s.linkRedact(nil, u.UID, t.UID, msgid, reason)
	}
}

func redactMinutes() string { return strconv.Itoa(int(redactWindow.Minutes())) + " minutes" }

func (s *Server) redactOut(u *state.User, target, msgid, reason string) *outMsg {
	params := []string{target, msgid}
	if reason != "" {
		params = append(params, reason)
	}
	o := s.out(u, "REDACT", params...)
	o.msg.Trailing = reason != ""
	o.needCap = redactCap
	return o
}

// redactChannel tells ch's members here and forgets the line.
func (s *Server) redactChannel(except *state.User, u *state.User, ch *state.Channel, msgid, reason string) {
	o := s.redactOut(u, ch.Name, msgid, reason)
	for _, mem := range ch.Members {
		if mem.User != except {
			s.deliverTo(mem.User, o)
		}
	}
	if s.hist.store.Remove(ch.Name, msgid) {
		s.hist.dirty = true
	}
	delete(s.recent.by, msgid)
}

// linkRedact passes a redaction to the other BeeIRCd servers.
func (s *Server) linkRedact(from *linkConn, src, target, msgid, reason string) {
	it := &link.Bee{Source: src, Sub: "REDACT", Args: []string{target, msgid, reason}}
	for _, o := range s.upLinks(from) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
}

// remoteRedact is BEE REDACT <target> <msgid> :reason from l.
func (s *Server) remoteRedact(l *linkConn, it *link.Bee) {
	if len(it.Args) < 2 {
		return
	}
	u := s.net.UserByUID(it.Source)
	if u == nil {
		return
	}
	target, msgid := it.Args[0], it.Args[1]
	reason := ""
	if len(it.Args) > 2 {
		reason = strings.TrimSpace(it.Args[2])
	}
	if irc.IsChannel(target) {
		if ch := s.net.Channel(target); ch != nil {
			s.redactChannel(nil, u, ch, msgid, reason)
		}
		s.linkRedact(l, it.Source, target, msgid, reason)
		return
	}
	t := s.net.UserByUID(target)
	if t == nil {
		return
	}
	if s.ours(t.UID) {
		s.deliverTo(t, s.redactOut(u, t.Nick, msgid, reason))
		return
	}
	s.linkRedact(l, it.Source, target, msgid, reason)
}
