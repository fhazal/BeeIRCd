package server

// 1.5.9c channel history: +H lines:time (UnrealIRCd syntax), playback on
// join for clients without chathistory, and IRCv3 draft/chathistory.
// History lives in memory (internal/history) and is saved encrypted to
// [history] file when [history] secret is set.

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/history"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

const (
	chathistoryCap = "draft/chathistory"
	// chathistoryMax is the most lines one CHATHISTORY request returns
	// (005 CHATHISTORY=100); bigger limits are cut to it.
	chathistoryMax = 100
	// historySaveEvery: how often changed history is written to disk.
	historySaveEvery = 5 * time.Minute
)

// histState is the server's channel history.
type histState struct {
	store     *history.Store
	dirty     bool // lines added or dropped since the last save
	saving    bool // a save is running off the loop
	lastSave  time.Time
	lastSweep time.Time
}

// setupHistory makes the store, loads the saved file and sets the +H
// every new channel gets.
func (s *Server) setupHistory() {
	h := s.cfg.History
	s.hist.store = history.New(int64(h.MaxMemoryMB)<<20, irc.Fold)
	s.hist.lastSave, s.hist.lastSweep = time.Now(), time.Now()
	if h.Default != "" {
		lines, age, _ := config.ParseHistoryMode(h.Default)
		v := s.clampHistory(lines, age, false)
		s.net.NewChannelParams = map[byte]string{'H': v}
	}
	if h.Secret == "" {
		s.log.Printf("channel history is kept in memory only ([history] secret is not set)")
		return
	}
	chans, lines, err := s.hist.store.Load(h.File, h.Secret, time.Now())
	switch {
	case err == history.ErrWrongSecret:
		s.log.Printf("warning: %s was saved with another [history] secret; starting with no history (the old file is replaced at the next save)", h.File)
	case err != nil:
		s.log.Printf("warning: reading %s: %v; starting with no history", h.File, err)
	case chans > 0:
		s.log.Printf("channel history: %d line(s) in %d channel(s) loaded from %s", lines, chans, h.File)
	}
}

// clampHistory writes lines:age as +H stores it, cut to the server's caps.
func (s *Server) clampHistory(lines int, age time.Duration, registered bool) string {
	h := s.cfg.History
	most := h.MaxLinesUnregistered
	if registered {
		most = h.MaxLinesRegistered
	}
	lines = min(lines, most)
	age = min(age, h.MaxTime.Duration)
	if age < time.Minute {
		age = time.Minute
	}
	return config.FormatHistoryMode(lines, age)
}

// checkHistoryMode is checkParamMode for +H.
func (s *Server) checkHistoryMode(name, arg string) (string, string) {
	lines, age, err := config.ParseHistoryMode(arg)
	if err != nil {
		return "", "Use +H <lines>:<time>, like +H 50:1d (time in minutes, or with m, h or d)"
	}
	ch := s.net.Channel(name)
	return s.clampHistory(lines, age, ch != nil && ch.HasMode('r')), ""
}

// historyLimit is ch's +H as a history.Limit; ok false when ch has no +H.
func historyLimit(ch *state.Channel) (history.Limit, bool) {
	if !ch.HasMode('H') {
		return history.Limit{}, false
	}
	lines, age, err := config.ParseHistoryMode(ch.Param('H'))
	if err != nil {
		return history.Limit{}, false
	}
	return history.Limit{Lines: lines, Age: age}, true
}

// recordHistory keeps a channel PRIVMSG or NOTICE for +H.
func (s *Server) recordHistory(ch *state.Channel, o *outMsg) {
	lim, ok := historyLimit(ch)
	if !ok {
		return
	}
	var tags map[string]string
	for k, v := range o.tags {
		if k == "+typing" || k == "+draft/typing" {
			continue
		}
		if tags == nil {
			tags = make(map[string]string, len(o.tags))
		}
		tags[k] = v
	}
	s.hist.store.Add(ch.Name, lim, history.Line{
		At:      o.at,
		MsgID:   o.msgid,
		Source:  o.msg.Source,
		Account: o.account,
		Verb:    o.msg.Command,
		Target:  ch.Name,
		Text:    o.msg.Param(1),
		Tags:    tags,
	})
	s.hist.dirty = true
}

// historyModeChanged follows +H/-H on a channel.
func (s *Server) historyModeChanged(e *state.ChannelMode) {
	for _, m := range e.Changes {
		if m.Mode != 'H' {
			continue
		}
		if !m.Add {
			s.hist.store.Drop(e.Channel)
		} else if ch := s.net.Channel(e.Channel); ch != nil {
			if lim, ok := historyLimit(ch); ok {
				s.hist.store.SetLimit(ch.Name, lim)
			}
		}
		s.hist.dirty = true
	}
}

// historyChannels lists the +H channels ev may empty, so their history can
// go when the channel does (like UnrealIRCd: no channel, no history).
func (s *Server) historyChannels(ev state.Event) []string {
	switch e := ev.(type) {
	case *state.Part:
		return []string{e.Channel}
	case *state.Kick:
		return []string{e.Channel}
	case *state.UserQuit:
		var out []string
		if u := s.net.UserByUID(e.UID); u != nil {
			for _, ch := range u.Channels {
				if ch.HasMode('H') && len(ch.Members) == 1 {
					out = append(out, ch.Name)
				}
			}
		}
		return out
	case *state.ChannelMode:
		for _, m := range e.Changes {
			if m.Mode == 'P' && !m.Add {
				return []string{e.Channel}
			}
		}
	}
	return nil
}

// dropGoneHistory forgets the history of channels that no longer exist.
func (s *Server) dropGoneHistory(names []string) {
	if s.closing() {
		return // shutdown already took its snapshot
	}
	for _, name := range names {
		if s.net.Channel(name) == nil && s.hist.store.Lines(name) > 0 {
			s.hist.store.Drop(name)
			s.hist.dirty = true
		}
	}
}

func (s *Server) closing() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// historyTick expires old lines once a minute and saves every few minutes.
func (s *Server) historyTick(now time.Time) {
	if now.Sub(s.hist.lastSweep) >= time.Minute {
		s.hist.lastSweep = now
		before := s.hist.store.Bytes()
		s.hist.store.Expire(now)
		if s.hist.store.Bytes() != before {
			s.hist.dirty = true
		}
	}
	h := s.cfg.History
	if h.Secret == "" || !s.hist.dirty || s.hist.saving || now.Sub(s.hist.lastSave) < historySaveEvery {
		return
	}
	snap := s.hist.store.Snapshot()
	s.hist.dirty, s.hist.saving, s.hist.lastSave = false, true, now
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := history.Save(h.File, h.Secret, snap)
		s.post(func() {
			s.hist.saving = false
			if err != nil {
				s.hist.dirty = true
				s.log.Printf("saving channel history: %v", err)
			}
		})
	}()
}

// historySnapshot is taken at shutdown, before everyone quits.
func (s *Server) historySnapshot() history.Snapshot {
	if s.cfg.History.Secret == "" || s.hist.store == nil {
		return nil
	}
	return s.hist.store.Snapshot()
}

func (s *Server) saveHistorySnapshot(snap history.Snapshot) {
	if snap == nil {
		return
	}
	if err := history.Save(s.cfg.History.File, s.cfg.History.Secret, snap); err != nil {
		s.log.Printf("saving channel history: %v", err)
	}
}

// historyOut rebuilds a stored line as a message for c.
func historyOut(l *history.Line) *outMsg {
	return &outMsg{
		msg:     irc.Message{Source: l.Source, Command: l.Verb, Params: []string{l.Target, l.Text}, Trailing: true},
		tags:    l.Tags,
		msgid:   l.MsgID,
		account: l.Account,
		at:      l.At,
	}
}

// sendHistoryBatch sends lines to c in one batch of kind (chathistory)
// for target, or plain when c has no batch.
func (s *Server) sendHistoryBatch(c *Client, target string, lines []history.Line) {
	id := ""
	if c.caps["batch"] {
		s.batchSeq++
		id = "h" + strconv.FormatUint(s.batchSeq, 36)
		c.send(":" + s.cfg.Server.Name + " BATCH +" + id + " chathistory " + target)
	}
	for i := range lines {
		line, ok := c.format(historyOut(&lines[i]))
		if !ok {
			continue
		}
		line = strings.TrimSuffix(line, crlf)
		if id != "" {
			line = addTag(line, "batch", id)
		}
		c.send(line)
	}
	if id != "" {
		c.send(":" + s.cfg.Server.Name + " BATCH -" + id)
	}
}

// joinPlayback shows the newest lines to someone joining a +H channel
// whose client can't ask for history itself.
func (s *Server) joinPlayback(c *Client, ch *state.Channel) {
	h := s.cfg.History
	if !ch.HasMode('H') || c.caps[chathistoryCap] || h.PlaybackLines == 0 {
		return
	}
	lines := s.hist.store.Recent(ch.Name, h.PlaybackLines, time.Now().Add(-h.PlaybackTime.Duration))
	if len(lines) > 0 {
		s.sendHistoryBatch(c, ch.Name, lines)
	}
}

// ---- CHATHISTORY ----

func (s *Server) chathistoryFail(c *Client, code string, params ...string) {
	m := irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{"CHATHISTORY", code}, params...), Trailing: true}
	c.send(m.String())
}

// parseRef reads a CHATHISTORY reference: timestamp=..., msgid=... or *.
func parseRef(arg string, star bool) (history.Ref, bool) {
	if arg == "*" {
		return history.Ref{Star: true}, star
	}
	k, v, ok := strings.Cut(arg, "=")
	if !ok || v == "" {
		return history.Ref{}, false
	}
	switch k {
	case "msgid":
		return history.Ref{MsgID: v}, true
	case "timestamp":
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return history.Ref{}, false
		}
		return history.Ref{At: t}, true
	}
	return history.Ref{}, false
}

func parseLimit(arg string) (int, bool) {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return 0, false
	}
	return min(n, chathistoryMax), true
}

func (s *Server) cmdChathistory(c *Client, m irc.Message) {
	sub := strings.ToUpper(m.Params[0])
	need := map[string]int{"BEFORE": 4, "AFTER": 4, "LATEST": 4, "AROUND": 4, "BETWEEN": 5, "TARGETS": 4}
	n, known := need[sub]
	if !known {
		s.chathistoryFail(c, "UNKNOWN_COMMAND", m.Params[0], "Unknown subcommand")
		return
	}
	if len(m.Params) < n {
		s.chathistoryFail(c, "NEED_MORE_PARAMS", sub, "Missing parameters")
		return
	}
	if sub == "TARGETS" {
		s.chathistoryTargets(c, m.Params[1], m.Params[2], m.Params[3])
		return
	}
	target := m.Params[1]
	limit, ok := parseLimit(m.Params[len(m.Params)-1])
	if !ok {
		s.chathistoryFail(c, "INVALID_PARAMS", sub, m.Params[len(m.Params)-1], "Invalid limit")
		return
	}
	ref, ok := parseRef(m.Params[2], sub == "LATEST")
	if !ok {
		s.chathistoryFail(c, "INVALID_PARAMS", sub, m.Params[2], "Invalid message reference")
		return
	}
	var ref2 history.Ref
	if sub == "BETWEEN" {
		if ref2, ok = parseRef(m.Params[3], false); !ok {
			s.chathistoryFail(c, "INVALID_PARAMS", sub, m.Params[3], "Invalid message reference")
			return
		}
	}
	ch := s.net.Channel(target)
	if !irc.IsChannel(target) || ch == nil || ch.Member(c.user) == nil {
		// Only channel history is kept; you must be in the channel.
		s.chathistoryFail(c, "INVALID_TARGET", sub, target, "Messages could not be retrieved")
		return
	}
	var lines []history.Line
	if ch.HasMode('H') {
		st := s.hist.store
		switch sub {
		case "BEFORE":
			lines = st.Before(ch.Name, ref, limit)
		case "AFTER":
			lines = st.After(ch.Name, ref, limit)
		case "LATEST":
			lines = st.Latest(ch.Name, ref, limit)
		case "AROUND":
			lines = st.Around(ch.Name, ref, limit)
		case "BETWEEN":
			lines = st.Between(ch.Name, ref, ref2, limit)
		}
	}
	// Always a batch, even when empty.
	s.sendHistoryBatch(c, ch.Name, lines)
}

// chathistoryTargets is CHATHISTORY TARGETS: the caller's +H channels with
// lines between two timestamps.
func (s *Server) chathistoryTargets(c *Client, a, b, limitArg string) {
	from, ok1 := parseRef(a, false)
	to, ok2 := parseRef(b, false)
	if !ok1 || !ok2 || from.MsgID != "" || to.MsgID != "" {
		s.chathistoryFail(c, "INVALID_PARAMS", "TARGETS", a, "Use timestamp= references")
		return
	}
	limit, ok := parseLimit(limitArg)
	if !ok {
		s.chathistoryFail(c, "INVALID_PARAMS", "TARGETS", limitArg, "Invalid limit")
		return
	}
	var names []string
	for _, ch := range c.user.Channels {
		if ch.HasMode('H') {
			names = append(names, ch.Name)
		}
	}
	found := s.hist.store.Targets(names, from.At, to.At, limit)
	name := s.cfg.Server.Name
	id := ""
	if c.caps["batch"] {
		s.batchSeq++
		id = "h" + strconv.FormatUint(s.batchSeq, 36)
		c.send(":" + name + " BATCH +" + id + " draft/chathistory-targets")
	}
	for _, t := range found {
		line := ":" + name + " CHATHISTORY TARGETS " + t.Name + " " + t.Latest.UTC().Format(timeFormat)
		if id != "" {
			line = addTag(line, "batch", id)
		}
		c.send(line)
	}
	if id != "" {
		c.send(":" + name + " BATCH -" + id)
	}
}
