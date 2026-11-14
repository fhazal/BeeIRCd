package server

import (
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/state"
)

// Hive Panel Spotlight. The server remembers only WHEN each
// person last said something in each channel, never what; entries older
// than talkWindow are dropped. Today's busiest moment is the most local
// people at once since midnight.

const (
	talkWindow = 5 * time.Minute
	talkSweep  = time.Minute // how often old entries are dropped
	newWithin  = 7 * 24 * time.Hour
	maxYears   = 3
)

// roomTalk counts a room's lines in the last 5 minutes (one slot a minute)
// and remembers the order of its last line (the clock can tie).
type roomTalk struct {
	min [5]int64 // the minute each slot counts
	n   [5]int32
	seq uint64
}

// lines is how many lines the room had in the last 5 minutes.
func (r *roomTalk) lines(nowMin int64) int {
	t := 0
	for i := range r.min {
		if r.min[i] > nowMin-5 {
			t += int(r.n[i])
		}
	}
	return t
}

// noteTalk records that uid said something in ch (plain PRIVMSG only).
func (s *Server) noteTalk(ch *state.Channel, uid string) {
	now := time.Now()
	if s.talk == nil {
		s.talk = make(map[*state.Channel]map[string]int64)
		s.talkRoom = make(map[*state.Channel]*roomTalk)
	}
	r := s.talkRoom[ch]
	if r == nil {
		r = &roomTalk{}
		s.talkRoom[ch] = r
	}
	s.talkSeq++
	r.seq = s.talkSeq
	minute := now.Unix() / 60
	if i := minute % 5; r.min[i] != minute {
		r.min[i], r.n[i] = minute, 1
	} else {
		r.n[i]++
	}
	m := s.talk[ch]
	if m == nil {
		m = make(map[string]int64)
		s.talk[ch] = m
	}
	m[uid] = now.UnixNano()
	if now.Sub(s.talkSwept) > talkSweep {
		s.sweepTalk(now)
	}
}

// sweepTalk drops what is older than talkWindow, and channels that are
// gone.
func (s *Server) sweepTalk(now time.Time) {
	s.talkSwept = now
	old := now.Add(-talkWindow).UnixNano()
	for ch, m := range s.talk {
		if s.net.Channel(ch.Name) != ch {
			delete(s.talk, ch)
			delete(s.talkRoom, ch)
			continue
		}
		for uid, t := range m {
			if t < old {
				delete(m, uid)
			}
		}
		if len(m) == 0 {
			delete(s.talk, ch)
			delete(s.talkRoom, ch)
		}
	}
}

// notePeak keeps today's most local people at once.
func (s *Server) notePeak(n int) {
	now := time.Now()
	y, m, d := now.Date()
	if py, pm, pd := s.dayPeakAt.Date(); py != y || pm != m || pd != d {
		s.dayPeak, s.dayPeakAt = 0, time.Time{}
	}
	if n > s.dayPeak {
		s.dayPeak, s.dayPeakAt = n, now
	}
}

// spotlight builds the Spotlight card's stories.
func (s *Server) spotlight(now time.Time) control.Spotlight {
	var sp control.Spotlight
	s.sweepTalk(now)
	// Most people talking; on a tie, the most lines; then the room that
	// talked last.
	var best *state.Channel
	bestN, bestLines, bestSeq := 0, 0, uint64(0)
	nowMin := now.Unix() / 60
	for ch, m := range s.talk {
		if ch.HasMode('s') || ch.HasMode('p') {
			continue // secret and private rooms are never shown
		}
		r := s.talkRoom[ch]
		n, lines := len(m), r.lines(nowMin)
		if n > bestN || n == bestN && (lines > bestLines || lines == bestLines && r.seq > bestSeq) {
			best, bestN, bestLines, bestSeq = ch, n, lines, r.seq
		}
	}
	if best != nil {
		sp.Room = &control.SpotRoom{Name: best.Name, Talkers: bestN, Members: len(best.Members), Topic: best.Topic}
	}
	if y, m, d := s.dayPeakAt.Date(); s.dayPeak > 0 {
		if ny, nm, nd := now.Date(); ny == y && nm == m && nd == d {
			sp.DayPeak = &control.SpotPeak{People: s.dayPeak, At: s.dayPeakAt}
		}
	}
	newest, years := s.store.SpotlightAccounts(now, now.Add(-newWithin), maxYears)
	if newest != nil {
		sp.Newest = &control.SpotMember{Name: newest.Name, Since: newest.When}
	}
	for _, a := range years {
		sp.Anniversaries = append(sp.Anniversaries, control.SpotMember{Name: a.Name, Since: a.When, Years: a.Years})
	}
	sort.SliceStable(sp.Anniversaries, func(i, j int) bool { return sp.Anniversaries[i].Years > sp.Anniversaries[j].Years })
	return sp
}
