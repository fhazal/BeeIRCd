package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// BotServ kickers and bad words (Anope 2.0.20 bs_kick and bs_badwords).

// The kickers, in Anope's TTB order.
const (
	KickBolds = iota
	KickColors
	KickReverses
	KickUnderlines
	KickBadwords
	KickCaps
	KickFlood
	KickRepeat
	KickItalics
	KickAmsgs
	KickerCount
)

// Kickers are a channel's kicker settings (Anope's KickerData).
type Kickers struct {
	On          [KickerCount]bool
	TTB         [KickerCount]int // kicks before a ban, 0 = never ban
	CapsMin     int
	CapsPercent int
	FloodLines  int
	FloodSecs   int
	RepeatTimes int
}

// Any reports whether any kicker is on.
func (k Kickers) Any() bool {
	for _, on := range k.On {
		if on {
			return true
		}
	}
	return false
}

// DefaultKickers are the settings before anyone changes them.
func DefaultKickers() Kickers {
	return Kickers{CapsMin: 10, CapsPercent: 25, FloodLines: 6, FloodSecs: 10, RepeatTimes: 3}
}

// Bad word kinds (Anope's BadWordType).
const (
	BadWordAny = iota
	BadWordSingle
	BadWordStart
	BadWordEnd
)

// BadWord is one entry of a channel's bad words list.
type BadWord struct {
	ID   int64
	Word string
	Type int
}

const botKickSchema = `
CREATE TABLE IF NOT EXISTS chan_kick (
	channel_id   INTEGER PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
	kickers      INTEGER NOT NULL DEFAULT 0,
	ttb          TEXT NOT NULL DEFAULT '',
	caps_min     INTEGER NOT NULL DEFAULT 10,
	caps_percent INTEGER NOT NULL DEFAULT 25,
	flood_lines  INTEGER NOT NULL DEFAULT 6,
	flood_secs   INTEGER NOT NULL DEFAULT 10,
	repeat_times INTEGER NOT NULL DEFAULT 3
);
CREATE TABLE IF NOT EXISTS badwords (
	id         INTEGER PRIMARY KEY,
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	word       TEXT NOT NULL,
	type       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS badwords_channel ON badwords(channel_id);
`

func loadKickers(db *sql.DB, b *bsCache) error {
	b.kick = map[int64]Kickers{}
	b.badwords = map[int64][]BadWord{}
	rows, err := db.Query(`SELECT channel_id, kickers, ttb, caps_min, caps_percent, flood_lines, flood_secs, repeat_times FROM chan_kick`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ch, bits int64
		var ttb string
		k := DefaultKickers()
		if err := rows.Scan(&ch, &bits, &ttb, &k.CapsMin, &k.CapsPercent, &k.FloodLines, &k.FloodSecs, &k.RepeatTimes); err != nil {
			rows.Close()
			return err
		}
		for i := 0; i < KickerCount; i++ {
			k.On[i] = bits&(1<<i) != 0
		}
		for i, f := range strings.Split(ttb, ",") {
			if n, err := strconv.Atoi(f); err == nil && i < KickerCount && n > 0 {
				k.TTB[i] = n
			}
		}
		b.kick[ch] = k
	}
	rows.Close()
	rows, err = db.Query(`SELECT id, channel_id, word, type FROM badwords ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var w BadWord
		var ch int64
		if err := rows.Scan(&w.ID, &ch, &w.Word, &w.Type); err != nil {
			return err
		}
		b.badwords[ch] = append(b.badwords[ch], w)
	}
	return rows.Err()
}

// ChanKickers is channel c's kicker settings.
func (s *Store) ChanKickers(c *Channel) Kickers {
	if k, ok := s.bs.kick[c.ID]; ok {
		return k
	}
	return DefaultKickers()
}

// SetChanKickers saves channel c's kicker settings.
func (s *Store) SetChanKickers(c *Channel, k Kickers) error {
	var bits int64
	ttb := make([]string, KickerCount)
	for i := 0; i < KickerCount; i++ {
		if k.On[i] {
			bits |= 1 << i
		}
		ttb[i] = strconv.Itoa(k.TTB[i])
	}
	if _, err := s.db.Exec(`INSERT INTO chan_kick (channel_id, kickers, ttb, caps_min, caps_percent, flood_lines, flood_secs, repeat_times)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(channel_id) DO UPDATE SET kickers = excluded.kickers, ttb = excluded.ttb,
		caps_min = excluded.caps_min, caps_percent = excluded.caps_percent, flood_lines = excluded.flood_lines,
		flood_secs = excluded.flood_secs, repeat_times = excluded.repeat_times`,
		c.ID, bits, strings.Join(ttb, ","), k.CapsMin, k.CapsPercent, k.FloodLines, k.FloodSecs, k.RepeatTimes); err != nil {
		return err
	}
	s.bs.kick[c.ID] = k
	return nil
}

// BadWords is channel c's bad words list, in the order they were added.
func (s *Store) BadWords(c *Channel) []BadWord {
	return append([]BadWord(nil), s.bs.badwords[c.ID]...)
}

// AddBadWord adds a word to channel c's list.
func (s *Store) AddBadWord(c *Channel, word string, typ int) error {
	res, err := s.db.Exec(`INSERT INTO badwords (channel_id, word, type) VALUES (?, ?, ?)`, c.ID, word, typ)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	s.bs.badwords[c.ID] = append(s.bs.badwords[c.ID], BadWord{ID: id, Word: word, Type: typ})
	return nil
}

// DeleteBadWords removes the entries with these IDs from channel c's list.
func (s *Store) DeleteBadWords(c *Channel, ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	gone := map[int64]bool{}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM badwords WHERE id = ? AND channel_id = ?`, id, c.ID); err != nil {
			return err
		}
		gone[id] = true
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	var keep []BadWord
	for _, w := range s.bs.badwords[c.ID] {
		if !gone[w.ID] {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		delete(s.bs.badwords, c.ID)
	} else {
		s.bs.badwords[c.ID] = keep
	}
	return nil
}

// ClearBadWords empties channel c's list.
func (s *Store) ClearBadWords(c *Channel) error {
	if _, err := s.db.Exec(`DELETE FROM badwords WHERE channel_id = ?`, c.ID); err != nil {
		return err
	}
	delete(s.bs.badwords, c.ID)
	return nil
}
