package store

import (
	"database/sql"
	"strconv"
	"time"
)

// Reputation scores: one row per IP, how much people from it have chatted
// (UnrealIRCd's reputation module). The server keeps them in memory and
// saves them all every few minutes.

const reputationSchema = `
CREATE TABLE IF NOT EXISTS reputation (
	ip        TEXT PRIMARY KEY,
	score     INTEGER NOT NULL,
	last_seen INTEGER NOT NULL
);
`

// Reputation is one IP's score.
type Reputation struct {
	IP       string
	Score    int
	LastSeen time.Time
}

// Reputations returns every score.
func (s *Store) Reputations() ([]Reputation, error) {
	var out []Reputation
	err := eachRow(s.db, "SELECT ip, score, last_seen FROM reputation", func(r *sql.Rows) error {
		var e Reputation
		var seen int64
		if err := r.Scan(&e.IP, &e.Score, &seen); err != nil {
			return err
		}
		e.LastSeen = time.Unix(seen, 0).UTC()
		out = append(out, e)
		return nil
	})
	return out, err
}

// SaveReputations replaces every score with all.
func (s *Store) SaveReputations(all []Reputation) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM reputation"); err != nil {
		return err
	}
	st, err := tx.Prepare("INSERT INTO reputation (ip, score, last_seen) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer st.Close()
	for _, e := range all {
		if _, err := st.Exec(e.IP, e.Score, e.LastSeen.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReputationSince is when scores started being gathered (zero = never).
func (s *Store) ReputationSince() time.Time {
	var v string
	if err := s.db.QueryRow("SELECT value FROM meta WHERE key = 'reputation_since'").Scan(&v); err != nil {
		return time.Time{}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// SetReputationSince notes when gathering started.
func (s *Store) SetReputationSince(t time.Time) error {
	_, err := s.db.Exec("INSERT INTO meta (key, value) VALUES ('reputation_since', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		strconv.FormatInt(t.Unix(), 10))
	return err
}
