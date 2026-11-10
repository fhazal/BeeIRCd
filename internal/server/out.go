package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

const timeFormat = "2006-01-02T15:04:05.000Z"

// maxClientTags is the most bytes of client-only tags (+typing and the
// like) one message may carry.
const maxClientTags = 4094

// outMsg is one message from a user. It is formatted per receiver, because
// tags depend on what each client asked for with CAP.
type outMsg struct {
	msg       irc.Message
	extParams []string          // JOIN parameters for extended-join clients
	tags      map[string]string // client-only tags, sent with message-tags
	msgid     string
	account   string
	at        time.Time
	needCap   string // only clients with this capability get it
}

// out starts a message from u.
func (s *Server) out(u *state.User, cmd string, params ...string) *outMsg {
	return &outMsg{
		msg:     irc.Message{Source: u.Prefix(), Command: cmd, Params: params},
		account: u.Account,
		at:      time.Now(),
	}
}

// text marks the last parameter as free text.
func (o *outMsg) text() *outMsg {
	o.msg.Trailing = true
	return o
}

// format builds the line for c; false means c must not get it.
func (c *Client) format(o *outMsg) (string, bool) {
	if o.needCap != "" && !c.caps[o.needCap] {
		return "", false
	}
	m := o.msg
	if o.extParams != nil && c.caps["extended-join"] {
		m.Params = o.extParams
		m.Trailing = true
	}
	var tags map[string]string
	add := func(k, v string) {
		if tags == nil {
			tags = make(map[string]string)
		}
		tags[k] = v
	}
	if c.caps["server-time"] {
		add("time", o.at.UTC().Format(timeFormat))
	}
	if c.caps["account-tag"] && o.account != "" {
		add("account", o.account)
	}
	if c.caps["message-tags"] {
		if o.msgid != "" {
			add("msgid", o.msgid)
		}
		for k, v := range o.tags {
			add(k, v)
		}
	}
	m.Tags = tags
	return m.String(), true
}

// deliver sends o to c when c may have it.
func (c *Client) deliver(o *outMsg) {
	if line, ok := c.format(o); ok {
		c.send(line)
	}
}

// deliverTo sends o to a user connected here.
func (s *Server) deliverTo(u *state.User, o *outMsg) {
	if c := s.clients[u.UID]; c != nil {
		c.deliver(o)
	}
}

// broadcast sends o to every member of ch except skip (may be nil).
func (s *Server) broadcast(ch *state.Channel, o *outMsg, skip *state.User) {
	for _, m := range ch.Members {
		if m.User != skip {
			s.deliverTo(m.User, o)
		}
	}
}

// toNeighbours sends o to everyone sharing a channel with u, and to u too
// when self is true.
func (s *Server) toNeighbours(u *state.User, o *outMsg, self bool) {
	if self {
		s.deliverTo(u, o)
	}
	for _, n := range s.net.Neighbours(u) {
		s.deliverTo(n, o)
	}
}

// newMsgID returns an ID unique across the network.
func (s *Server) newMsgID() string {
	s.msgSeq++
	return s.cfg.Server.SID + strconv.FormatInt(time.Now().UnixMilli(), 36) + strconv.FormatUint(s.msgSeq, 36)
}

// clientTags keeps the client-only tags (those starting with '+') of an
// incoming message, or none when together they are too big.
func clientTags(in map[string]string) map[string]string {
	var out map[string]string
	size := 0
	for k, v := range in {
		if !strings.HasPrefix(k, "+") {
			continue
		}
		size += len(k) + len(v) + 2
		if out == nil {
			out = make(map[string]string)
		}
		out[k] = v
	}
	if size > maxClientTags {
		return nil
	}
	return out
}

// addTag puts one tag in front of an already built line.
func addTag(line, key, value string) string {
	tag := key
	if value != "" {
		tag += "=" + irc.EscapeTag(value)
	}
	if strings.HasPrefix(line, "@") {
		return "@" + tag + ";" + line[1:]
	}
	return "@" + tag + " " + line
}

// flushLabeled sends what a labeled command produced, as labeled-response
// asks: an ACK for nothing, the label on a single line, or a batch.
func (s *Server) flushLabeled(c *Client, label string, lines []string) {
	name := s.cfg.Server.Name
	switch {
	case len(lines) == 0:
		c.send(addTag(":"+name+" ACK", "label", label))
	case len(lines) == 1:
		c.send(addTag(lines[0], "label", label))
	case !c.caps["batch"]:
		for _, l := range lines {
			c.send(l)
		}
	default:
		s.batchSeq++
		id := strconv.FormatUint(s.batchSeq, 36)
		c.send(addTag(":"+name+" BATCH +"+id+" labeled-response", "label", label))
		for _, l := range lines {
			c.send(addTag(l, "batch", id))
		}
		c.send(":" + name + " BATCH -" + id)
	}
}
