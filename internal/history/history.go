// Package history keeps channel history for channel mode +H and IRCv3
// CHATHISTORY, like UnrealIRCd's history_backend_mem: in memory, per
// channel, cut by line count and age, with a server-wide memory cap, and
// saved encrypted to one file so a restart doesn't lose it.
//
// A Store is used from the server's loop only; Snapshot copies what Save
// needs so saving can run elsewhere.
package history

import (
	"sort"
	"time"
)

// Line is one recorded PRIVMSG or NOTICE.
type Line struct {
	At      time.Time // millisecond precision, as sent in the time tag
	MsgID   string
	Source  string // nick!ident@host at the time
	Account string
	Bot     bool   // sent by a +B user
	Verb    string // PRIVMSG or NOTICE
	Target  string // the channel as it was written then
	Text    string
	Tags    map[string]string // client-only tags (+typing, +draft/reply ...)
}

func (l *Line) size() int64 {
	n := 96 + len(l.MsgID) + len(l.Source) + len(l.Account) + len(l.Verb) + len(l.Target) + len(l.Text)
	for k, v := range l.Tags {
		n += len(k) + len(v) + 16
	}
	return int64(n)
}

// Limit is how much a channel keeps: +H lines:age.
type Limit struct {
	Lines int
	Age   time.Duration
}

type chanLog struct {
	name  string
	limit Limit
	lines []Line
	bytes int64
}

// Store holds the history of every +H channel, by folded channel name.
type Store struct {
	logs     map[string]*chanLog
	bytes    int64
	maxBytes int64
	fold     func(string) string
}

// New makes a store that keeps at most maxBytes (0 = no cap). fold maps a
// channel name to its key (IRC case folding).
func New(maxBytes int64, fold func(string) string) *Store {
	return &Store{logs: make(map[string]*chanLog), maxBytes: maxBytes, fold: fold}
}

// Bytes is about how much memory the history holds.
func (s *Store) Bytes() int64 { return s.bytes }

// Lines counts the stored lines of channel.
func (s *Store) Lines(channel string) int {
	if l := s.logs[s.fold(channel)]; l != nil {
		return len(l.lines)
	}
	return 0
}

// Add records a line for channel under limit.
func (s *Store) Add(channel string, limit Limit, line Line) {
	key := s.fold(channel)
	l := s.logs[key]
	if l == nil {
		l = &chanLog{name: channel}
		s.logs[key] = l
	}
	l.name, l.limit = channel, limit
	line.At = line.At.Truncate(time.Millisecond)
	// Lines stay in time order even if the clock steps back.
	if n := len(l.lines); n > 0 && line.At.Before(l.lines[n-1].At) {
		line.At = l.lines[n-1].At
	}
	l.lines = append(l.lines, line)
	sz := line.size()
	l.bytes += sz
	s.bytes += sz
	s.trim(l, line.At)
	s.enforceCap()
}

// SetLimit changes a channel's limit (a new +H) and cuts what's over it.
func (s *Store) SetLimit(channel string, limit Limit) {
	if l := s.logs[s.fold(channel)]; l != nil {
		l.limit = limit
		s.trim(l, time.Now())
	}
}

// Drop forgets a channel's history (-H).
func (s *Store) Drop(channel string) {
	key := s.fold(channel)
	if l := s.logs[key]; l != nil {
		s.bytes -= l.bytes
		delete(s.logs, key)
	}
}

// Expire cuts lines older than each channel's age limit.
func (s *Store) Expire(now time.Time) {
	for key, l := range s.logs {
		s.trim(l, now)
		if len(l.lines) == 0 {
			delete(s.logs, key)
		}
	}
}

// trim drops lines over the count or older than the age.
func (s *Store) trim(l *chanLog, now time.Time) {
	cut := 0
	if l.limit.Lines > 0 && len(l.lines) > l.limit.Lines {
		cut = len(l.lines) - l.limit.Lines
	}
	if l.limit.Age > 0 {
		oldest := now.Add(-l.limit.Age)
		for cut < len(l.lines) && l.lines[cut].At.Before(oldest) {
			cut++
		}
	}
	s.cutFront(l, cut)
}

func (s *Store) cutFront(l *chanLog, n int) {
	if n <= 0 {
		return
	}
	var freed int64
	for i := 0; i < n; i++ {
		freed += l.lines[i].size()
	}
	l.lines = append(l.lines[:0:0], l.lines[n:]...)
	l.bytes -= freed
	s.bytes -= freed
}

// enforceCap drops the oldest lines across all channels until the store
// is under 90% of its cap.
func (s *Store) enforceCap() {
	if s.maxBytes <= 0 || s.bytes <= s.maxBytes {
		return
	}
	goal := s.maxBytes * 9 / 10
	for s.bytes > goal {
		var oldest *chanLog
		for _, l := range s.logs {
			if len(l.lines) > 0 && (oldest == nil || l.lines[0].At.Before(oldest.lines[0].At)) {
				oldest = l
			}
		}
		if oldest == nil {
			return
		}
		n := max(1, len(oldest.lines)/10)
		s.cutFront(oldest, n)
	}
}

// Ref points into a channel's history: a msgid, a time, or "*" (latest).
type Ref struct {
	MsgID string
	At    time.Time
	Star  bool
}

// indexes for ref in lines: before is how many lines come before it, after
// is the index of the first line after it. A msgid not found gives ok false.
func indexes(lines []Line, r Ref) (before, after int, ok bool) {
	if r.MsgID != "" {
		for i := range lines {
			if lines[i].MsgID == r.MsgID {
				return i, i + 1, true
			}
		}
		return 0, 0, false
	}
	before = sort.Search(len(lines), func(i int) bool { return !lines[i].At.Before(r.At) })
	after = sort.Search(len(lines), func(i int) bool { return lines[i].At.After(r.At) })
	return before, after, true
}

func (s *Store) lines(channel string) []Line {
	if l := s.logs[s.fold(channel)]; l != nil {
		return l.lines
	}
	return nil
}

func lastN(l []Line, n int) []Line {
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return append([]Line(nil), l...)
}

func firstN(l []Line, n int) []Line {
	if len(l) > n {
		l = l[:n]
	}
	return append([]Line(nil), l...)
}

// Before is CHATHISTORY BEFORE: the last limit lines before ref.
func (s *Store) Before(channel string, ref Ref, limit int) []Line {
	all := s.lines(channel)
	b, _, ok := indexes(all, ref)
	if !ok {
		return nil
	}
	return lastN(all[:b], limit)
}

// After is CHATHISTORY AFTER: the first limit lines after ref.
func (s *Store) After(channel string, ref Ref, limit int) []Line {
	all := s.lines(channel)
	_, a, ok := indexes(all, ref)
	if !ok {
		return nil
	}
	return firstN(all[a:], limit)
}

// Latest is CHATHISTORY LATEST: the last limit lines, after ref unless
// ref is "*".
func (s *Store) Latest(channel string, ref Ref, limit int) []Line {
	all := s.lines(channel)
	if !ref.Star {
		_, a, ok := indexes(all, ref)
		if !ok {
			return nil
		}
		all = all[a:]
	}
	return lastN(all, limit)
}

// Around is CHATHISTORY AROUND: up to limit lines centred on ref.
func (s *Store) Around(channel string, ref Ref, limit int) []Line {
	all := s.lines(channel)
	b, _, ok := indexes(all, ref)
	if !ok {
		return nil
	}
	start := max(0, b-limit/2)
	end := min(len(all), start+limit)
	start = max(0, end-limit)
	return append([]Line(nil), all[start:end]...)
}

// Between is CHATHISTORY BETWEEN: lines strictly between the two refs, at
// most limit, taken from the side of the first ref.
func (s *Store) Between(channel string, from, to Ref, limit int) []Line {
	all := s.lines(channel)
	fb, fa, ok1 := indexes(all, from)
	tb, ta, ok2 := indexes(all, to)
	if !ok1 || !ok2 {
		return nil
	}
	if fa <= tb { // forwards
		return firstN(all[fa:tb], limit)
	}
	if ta <= fb { // backwards
		return lastN(all[ta:fb], limit)
	}
	return nil
}

// Recent is the playback on join: the last n lines no older than since.
func (s *Store) Recent(channel string, n int, since time.Time) []Line {
	all := s.lines(channel)
	i := sort.Search(len(all), func(i int) bool { return !all[i].At.Before(since) })
	return lastN(all[i:], n)
}

// Target is a channel with history in a time range (CHATHISTORY TARGETS).
type Target struct {
	Name   string
	Latest time.Time
}

// Targets lists the channels among names with lines between from and to
// (either order), by their latest line in that range, oldest first.
func (s *Store) Targets(names []string, from, to time.Time, limit int) []Target {
	if to.Before(from) {
		from, to = to, from
	}
	var out []Target
	for _, name := range names {
		all := s.lines(name)
		i := sort.Search(len(all), func(i int) bool { return all[i].At.After(to) })
		if i == 0 || !all[i-1].At.After(from) {
			continue
		}
		out = append(out, Target{Name: s.logs[s.fold(name)].name, Latest: all[i-1].At})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Latest.Before(out[j].Latest) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
