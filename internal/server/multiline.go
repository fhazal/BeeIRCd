package server

import (
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// IRCv3 draft/multiline: a client sends several lines as one message in
// a BATCH. They are checked and delivered once, as one message: in a
// batch to clients with draft/multiline (and batch), as separate lines to
// everyone else and to other servers.

const (
	multilineCap = "draft/multiline"
	concatTag    = "draft/multiline-concat"
	mlMaxBytes   = 4096
	mlMaxLines   = 24
)

// mlPart is one line of a multiline message; concat lines join the line
// before them without a line break.
type mlPart struct {
	text   string
	concat bool
}

// mlIn is a multiline batch a client is sending.
type mlIn struct {
	id, target, verb string
	tags             map[string]string // client tags, from the BATCH line
	parts            []mlPart
	bytes            int
	label            string // labeled-response label of the BATCH + line
	bad              string // a FAIL code once the batch is broken
	badCtx           []string
}

// mlOut is the multiline message being delivered (on the loop, while
// message runs for it).
type mlOut struct {
	parts    []mlPart
	combined string // the text as message saw it
}

// partsFor is what to deliver for text: the parts as sent, or text's
// lines when a check changed it (the +G censor).
func (m *mlOut) partsFor(text string) []mlPart {
	if text == m.combined {
		return m.parts
	}
	var out []mlPart
	for _, l := range strings.Split(text, "\n") {
		out = append(out, mlPart{text: l})
	}
	return out
}

func joinParts(parts []mlPart) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 && !p.concat {
			b.WriteByte('\n')
		}
		b.WriteString(p.text)
	}
	return b.String()
}

func (s *Server) batchFail(c *Client, code string, params ...string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{"BATCH", code}, params...), Trailing: true}.String())
}

// cmdBatch is BATCH +id draft/multiline <target> and BATCH -id.
func (s *Server) cmdBatch(c *Client, m irc.Message) {
	ref := m.Params[0]
	switch {
	case strings.HasPrefix(ref, "+") && len(ref) > 1:
		if len(m.Params) < 3 || m.Params[1] != multilineCap || !c.caps[multilineCap] {
			s.batchFail(c, "UNKNOWN_TYPE", m.Param(1), "This batch type isn't supported")
			return
		}
		if c.mlIn != nil {
			s.batchFail(c, "MULTILINE_INVALID", "A multiline batch is open already")
			c.mlIn = nil
			return
		}
		c.mlIn = &mlIn{id: ref[1:], target: m.Params[2], tags: clientTags(m.Tags)}
	case strings.HasPrefix(ref, "-") && len(ref) > 1:
		in := c.mlIn
		if in == nil || in.id != ref[1:] {
			s.batchFail(c, "MULTILINE_INVALID", "No such batch is open")
			return
		}
		c.mlIn = nil
		if in.label != "" && c.capture == nil {
			held := []string{}
			c.capture = &held
			s.multilineDone(c, in)
			if c.capture != nil {
				c.capture = nil
				s.flushLabeled(c, in.label, held)
			}
			return
		}
		s.multilineDone(c, in)
	default:
		s.batchFail(c, "MULTILINE_INVALID", "Invalid batch reference")
	}
}

// multilineLine takes one line of c's open batch.
func (s *Server) multilineLine(c *Client, m irc.Message) {
	in := c.mlIn
	if in.bad != "" {
		return
	}
	if m.Command != "PRIVMSG" && m.Command != "NOTICE" {
		in.bad, in.badCtx = "MULTILINE_INVALID", []string{"Only PRIVMSG and NOTICE may be in a multiline batch"}
		return
	}
	if in.verb == "" {
		in.verb = m.Command
	} else if in.verb != m.Command {
		in.bad, in.badCtx = "MULTILINE_INVALID", []string{"PRIVMSG and NOTICE can't be mixed"}
		return
	}
	if len(m.Params) < 2 || irc.Fold(m.Params[0]) != irc.Fold(in.target) {
		in.bad, in.badCtx = "MULTILINE_INVALID_TARGET", []string{in.target, m.Param(0), "Every line must go to the batch's target"}
		return
	}
	_, concat := m.Tags[concatTag]
	text := m.Params[1]
	if concat && text == "" {
		in.bad, in.badCtx = "MULTILINE_INVALID", []string{"A concat line can't be blank"}
		return
	}
	if len(in.parts) > 0 && !concat {
		in.bytes++ // the line break
	}
	in.bytes += len(text)
	in.parts = append(in.parts, mlPart{text: text, concat: concat})
	switch {
	case len(in.parts) > mlMaxLines:
		in.bad, in.badCtx = "MULTILINE_MAX_LINES", []string{strconv.Itoa(mlMaxLines), "Too many lines"}
	case in.bytes > mlMaxBytes:
		in.bad, in.badCtx = "MULTILINE_MAX_BYTES", []string{strconv.Itoa(mlMaxBytes), "Too many bytes"}
	}
}

// multilineDone sends a finished batch as one message.
func (s *Server) multilineDone(c *Client, in *mlIn) {
	if in.bad != "" {
		s.batchFail(c, in.bad, in.badCtx...)
		return
	}
	if len(in.parts) == 0 || strings.TrimSpace(joinParts(in.parts)) == "" {
		s.batchFail(c, "MULTILINE_INVALID", "The message is empty")
		return
	}
	if strings.Contains(in.target, ",") {
		s.batchFail(c, "MULTILINE_INVALID_TARGET", in.target, in.target, "One target only")
		return
	}
	combined := joinParts(in.parts)
	// The spam filter sees the whole message once, lines joined by a
	// space (concat lines without one), as if it had come in one line.
	check := irc.Message{Command: in.verb, Params: []string{in.target, strings.ReplaceAll(combined, "\n", " ")}}
	if !s.spamCheck(c, &check) {
		return
	}
	s.ml = &mlOut{parts: in.parts, combined: combined}
	defer func() { s.ml = nil }()
	s.message(c, irc.Message{Command: in.verb, Params: []string{in.target, combined}, Tags: in.tags}, in.verb)
}

// mlLines are the lines o (a multiline message) gives c: a batch for
// multiline clients, plain lines (blank ones left out) for others.
func (c *Client) mlLines(o *outMsg) []string {
	plain := *o
	plain.multi, plain.built = nil, 0
	if c.caps[multilineCap] && c.caps["batch"] {
		c.s.batchSeq++
		id := "m" + strconv.FormatUint(c.s.batchSeq, 36)
		target := o.msg.Params[0]
		open := plain
		open.msg = irc.Message{Source: o.msg.Source, Command: "BATCH", Params: []string{"+" + id, multilineCap, target}}
		out := []string{c.build(&open)}
		for _, p := range o.multi {
			line := irc.Message{Source: o.msg.Source, Command: o.msg.Command, Params: []string{target, p.text}, Trailing: true}.String()
			if p.concat {
				line = addTag(line, concatTag, "")
			}
			out = append(out, addTag(line, "batch", id))
		}
		return append(out, irc.Message{Source: o.msg.Source, Command: "BATCH", Params: []string{"-" + id}}.String())
	}
	var out []string
	first := true
	for _, p := range o.multi {
		if p.text == "" {
			continue
		}
		l := plain
		l.msg.Params = []string{o.msg.Params[0], p.text}
		if !first {
			l.msgid = "" // one message, one ID: the first line carries it
		}
		first = false
		out = append(out, c.build(&l))
	}
	return out
}

// mlTexts are the lines of a multiline message for other servers,
// history and services (blank ones left out), or just text.
func (s *Server) mlTexts(text string) []string {
	if s.ml == nil {
		return []string{text}
	}
	var out []string
	for _, p := range s.ml.partsFor(text) {
		if p.text != "" {
			out = append(out, p.text)
		}
	}
	return out
}

// mlRouteChannel is routeChannel for each line of a multiline message.
func (s *Server) mlRouteChannel(src string, ch *state.Channel, status, verb, text string, tags map[string]string, msgid string) {
	for i, t := range s.mlTexts(text) {
		id := msgid
		if i > 0 {
			id = s.newMsgID()
		}
		s.routeChannel(nil, src, ch, status, verb, t, linkTags(tags, id))
	}
}

// mlRouteUser is routeUser for each line of a multiline message.
func (s *Server) mlRouteUser(src string, t *state.User, verb, text string, tags map[string]string, msgid string) {
	for i, line := range s.mlTexts(text) {
		id := msgid
		if i > 0 {
			id = s.newMsgID()
		}
		s.routeUser(src, t, verb, line, linkTags(tags, id))
	}
}

// mlRead follows a client's multiline batches as its lines are read, so
// flood control counts a batch once: the BATCH + line pays, the lines in
// it (up to the batch limit) don't.
type mlRead struct {
	id   string
	left int
}

// free reports whether line is a line of the open batch that costs
// nothing.
func (b *mlRead) free(line string) bool {
	if !strings.HasPrefix(line, "@") && !strings.HasPrefix(line, "BATCH ") && !strings.HasPrefix(line, "batch ") {
		return false
	}
	m, err := irc.Parse(line)
	if err != nil {
		return false
	}
	if strings.EqualFold(m.Command, "BATCH") && len(m.Params) > 0 {
		ref := m.Params[0]
		switch {
		case strings.HasPrefix(ref, "+") && len(m.Params) > 1 && m.Params[1] == multilineCap:
			b.id, b.left = ref[1:], mlMaxLines
		case strings.HasPrefix(ref, "-") && ref[1:] == b.id && b.id != "":
			b.id = ""
			return true
		}
		return false
	}
	if b.id == "" || b.left <= 0 || m.Tags["batch"] != b.id {
		return false
	}
	b.left--
	return true
}
