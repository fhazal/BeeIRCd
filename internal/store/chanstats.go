package store

import (
	"database/sql"
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Chanstats (Anope's m_chanstats, without MySQL): what people say in
// channels with ChanServ SET CHANSTATS on, counted per channel and account
// and per account network-wide, for all time, this month, this week and
// today. Counts stay in memory; SaveChanStats writes the changed ones (the
// services save them every minute and at shutdown).

const chanStatsSchema = `
CREATE TABLE IF NOT EXISTS chanstats (
	channel_id INTEGER NOT NULL,
	account_id INTEGER NOT NULL,
	period     INTEGER NOT NULL,
	since      INTEGER NOT NULL,
	lines      INTEGER NOT NULL DEFAULT 0,
	letters    INTEGER NOT NULL DEFAULT 0,
	words      INTEGER NOT NULL DEFAULT 0,
	actions    INTEGER NOT NULL DEFAULT 0,
	happy      INTEGER NOT NULL DEFAULT 0,
	sad        INTEGER NOT NULL DEFAULT 0,
	other      INTEGER NOT NULL DEFAULT 0,
	kicks      INTEGER NOT NULL DEFAULT 0,
	kicked     INTEGER NOT NULL DEFAULT 0,
	modes      INTEGER NOT NULL DEFAULT 0,
	topics     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (channel_id, account_id, period)
);
`

// ChanStats are one account's counts in one period.
type ChanStats struct {
	Lines, Letters, Words, Actions int64
	Happy, Sad, Other              int64 // smileys
	Kicks, Kicked                  int64 // kicks given, times kicked
	Modes, Topics                  int64 // mode changes and topics set
}

// Smileys is every smiley, of all kinds.
func (c ChanStats) Smileys() int64 { return c.Happy + c.Sad + c.Other }

func (c *ChanStats) add(d ChanStats) {
	c.Lines += d.Lines
	c.Letters += d.Letters
	c.Words += d.Words
	c.Actions += d.Actions
	c.Happy += d.Happy
	c.Sad += d.Sad
	c.Other += d.Other
	c.Kicks += d.Kicks
	c.Kicked += d.Kicked
	c.Modes += d.Modes
	c.Topics += d.Topics
}

// The chanstats periods, as indexes into ChanStatsPeriods.
const (
	StatsTotal = iota
	StatsMonth
	StatsWeek
	StatsDay
	statsPeriods
)

// ChanStatsPeriods are one account's counts for each period.
type ChanStatsPeriods [statsPeriods]ChanStats

// ChanStatsEntry is one line of a top list.
type ChanStatsEntry struct {
	AccountID int64
	Name      string
	Stats     ChanStats // all time
}

type csRow struct {
	p     ChanStatsPeriods
	since [statsPeriods]int // which month, week or day p is for (statsKeys)
}

type csKey struct{ ch, acct int64 }

type csCache struct {
	rows  map[int64]map[int64]*csRow // channel (0 = network-wide) -> account -> counts
	dirty map[csKey]bool             // changed since the last save
}

// statsKeys names the periods now falls in, in local time: 0 for all time,
// then 202611, the date of this week's Monday (20261123) and today's date.
func statsKeys(now time.Time) [statsPeriods]int {
	t := now.Local()
	y, m, d := t.Date()
	wy, wm, wd := t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Date()
	return [statsPeriods]int{0, y*100 + int(m), wy*10000 + int(wm)*100 + wd, y*10000 + int(m)*100 + d}
}

func loadChanStats(db *sql.DB) (*csCache, error) {
	// Rows of accounts and channels that went without DeleteAccount or
	// DeleteChannel would never be shown again: clear them out.
	if _, err := db.Exec(`DELETE FROM chanstats WHERE account_id NOT IN (SELECT id FROM accounts)
		OR (channel_id != 0 AND channel_id NOT IN (SELECT id FROM channels))`); err != nil {
		return nil, err
	}
	c := &csCache{rows: map[int64]map[int64]*csRow{}, dirty: map[csKey]bool{}}
	err := eachRow(db, `SELECT channel_id, account_id, period, since, lines, letters, words, actions,
		happy, sad, other, kicks, kicked, modes, topics FROM chanstats`, func(r *sql.Rows) error {
		var ch, acct int64
		var period, since int
		var p ChanStats
		if err := r.Scan(&ch, &acct, &period, &since, &p.Lines, &p.Letters, &p.Words, &p.Actions,
			&p.Happy, &p.Sad, &p.Other, &p.Kicks, &p.Kicked, &p.Modes, &p.Topics); err != nil {
			return err
		}
		if period < 0 || period >= statsPeriods {
			return nil
		}
		row := c.row(ch, acct)
		row.p[period], row.since[period] = p, since
		return nil
	})
	return c, err
}

func (c *csCache) row(ch, acct int64) *csRow {
	m := c.rows[ch]
	if m == nil {
		m = map[int64]*csRow{}
		c.rows[ch] = m
	}
	r := m[acct]
	if r == nil {
		r = &csRow{}
		m[acct] = r
	}
	return r
}

func (c *csCache) dropChannel(id int64) { delete(c.rows, id) }

func (c *csCache) dropAccount(id int64) {
	for ch, m := range c.rows {
		delete(m, id)
		if len(m) == 0 {
			delete(c.rows, ch)
		}
	}
}

// ChanStatsChannel is the ID of the registered channel name when it has
// CHANSTATS on, else 0. It is cheap: it runs for every channel message.
func (s *Store) ChanStatsChannel(name string) int64 {
	id := s.c.chanKeys[irc.Fold(name)]
	if ch := s.c.channels[id]; ch != nil && ch.ChanStats {
		return id
	}
	return 0
}

// AccountChanStats says whether account id has CHANSTATS on.
func (s *Store) AccountChanStats(id int64) bool {
	a := s.c.accounts[id]
	return a != nil && a.ChanStats
}

// AddChanStats counts d for an account in a channel and network-wide.
// Periods that ended since the last count start again from zero.
func (s *Store) AddChanStats(channelID, accountID int64, d ChanStats, now time.Time) {
	if s.c.accounts[accountID] == nil || s.c.channels[channelID] == nil {
		return
	}
	keys := statsKeys(now)
	for _, ch := range [2]int64{channelID, 0} {
		r := s.cs.row(ch, accountID)
		for i := range r.p {
			if r.since[i] != keys[i] {
				r.p[i], r.since[i] = ChanStats{}, keys[i]
			}
			r.p[i].add(d)
		}
		s.cs.dirty[csKey{ch, accountID}] = true
	}
}

// ChanStatsOf is an account's counts in a channel (0: network-wide) for
// each period; ok is false when nothing was ever counted.
func (s *Store) ChanStatsOf(channelID, accountID int64, now time.Time) (out ChanStatsPeriods, ok bool) {
	r := s.cs.rows[channelID][accountID]
	if r == nil {
		return out, false
	}
	keys := statsKeys(now)
	for i := range r.p {
		if r.since[i] == keys[i] {
			out[i] = r.p[i]
		}
	}
	return out, true
}

// TopChanStats is the n accounts that wrote the most letters in a channel
// (0: network-wide) of all time, most first, like Anope's TOP.
func (s *Store) TopChanStats(channelID int64, n int) []ChanStatsEntry {
	var out []ChanStatsEntry
	for acct, r := range s.cs.rows[channelID] {
		out = append(out, ChanStatsEntry{AccountID: acct, Name: s.c.accountName(acct), Stats: r.p[StatsTotal]})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Stats, out[j].Stats
		if a.Letters != b.Letters {
			return a.Letters > b.Letters
		}
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		return out[i].AccountID < out[j].AccountID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// SaveChanStats writes the counts that changed since the last save.
func (s *Store) SaveChanStats() error {
	if len(s.cs.dirty) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT INTO chanstats (channel_id, account_id, period, since, lines, letters, words,
		actions, happy, sad, other, kicks, kicked, modes, topics) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (channel_id, account_id, period) DO UPDATE SET since = excluded.since, lines = excluded.lines,
		letters = excluded.letters, words = excluded.words, actions = excluded.actions, happy = excluded.happy,
		sad = excluded.sad, other = excluded.other, kicks = excluded.kicks, kicked = excluded.kicked,
		modes = excluded.modes, topics = excluded.topics`)
	if err != nil {
		return err
	}
	defer st.Close()
	for k := range s.cs.dirty {
		r := s.cs.rows[k.ch][k.acct]
		if r == nil { // dropped since
			continue
		}
		for i, p := range r.p {
			if _, err := st.Exec(k.ch, k.acct, i, r.since[i], p.Lines, p.Letters, p.Words, p.Actions,
				p.Happy, p.Sad, p.Other, p.Kicks, p.Kicked, p.Modes, p.Topics); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cs.dirty = map[csKey]bool{}
	return nil
}

// TopChanStatsIn is the n accounts that wrote the most letters in a channel
// (0: network-wide) in period (StatsTotal, StatsMonth, ...) as of now, most
// first: letters, then lines, then account ID. Accounts with nothing in that
// period are left out; a period that ended counts as zero.
func (s *Store) TopChanStatsIn(channelID int64, period, n int, now time.Time) []ChanStatsEntry {
	if period < 0 || period >= statsPeriods {
		return nil
	}
	key := statsKeys(now)[period]
	var out []ChanStatsEntry
	for acct, r := range s.cs.rows[channelID] {
		if r.since[period] != key || r.p[period] == (ChanStats{}) {
			continue
		}
		out = append(out, ChanStatsEntry{AccountID: acct, Name: s.c.accountName(acct), Stats: r.p[period]})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Stats, out[j].Stats
		if a.Letters != b.Letters {
			return a.Letters > b.Letters
		}
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		return out[i].AccountID < out[j].AccountID
	})
	if n >= 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// ChanStatsTotals is what everyone counted in a channel (0: network-wide)
// wrote together in period as of now.
func (s *Store) ChanStatsTotals(channelID int64, period int, now time.Time) (t ChanStats) {
	if period < 0 || period >= statsPeriods {
		return t
	}
	key := statsKeys(now)[period]
	for _, r := range s.cs.rows[channelID] {
		if r.since[period] == key {
			t.add(r.p[period])
		}
	}
	return t
}
