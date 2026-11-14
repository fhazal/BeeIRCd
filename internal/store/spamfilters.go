package store

import (
	"time"

	"github.com/fhazal/beeircd/internal/spamfilter"
)

// Spam filter rules (after 1.0). One row per rule; the same rule on every
// linked server is the same (type, targets, action, match).
const spamfilterSchema = `
CREATE TABLE IF NOT EXISTS spamfilters (
	id       INTEGER PRIMARY KEY,
	type     TEXT NOT NULL,
	match    TEXT NOT NULL,
	targets  TEXT NOT NULL,
	action   TEXT NOT NULL,
	ban_time INTEGER NOT NULL DEFAULT 0,
	reason   TEXT NOT NULL,
	set_by   TEXT NOT NULL,
	set_at   INTEGER NOT NULL,
	expires  INTEGER NOT NULL DEFAULT 0,
	UNIQUE (type, targets, action, match)
);
`

// SpamFilters returns every rule, oldest first, expired ones included.
func (s *Store) SpamFilters() ([]spamfilter.Filter, error) {
	rows, err := s.db.Query("SELECT id, type, match, targets, action, ban_time, reason, set_by, set_at, expires FROM spamfilters ORDER BY set_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []spamfilter.Filter
	for rows.Next() {
		var f spamfilter.Filter
		var ban, at, exp int64
		if err := rows.Scan(&f.ID, &f.Type, &f.Match, &f.Targets, &f.Action, &ban, &f.Reason, &f.SetBy, &at, &exp); err != nil {
			return nil, err
		}
		f.BanTime, f.SetAt, f.Expires = time.Duration(ban)*time.Second, time.Unix(at, 0), zeroOrTime(exp)
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddSpamFilter adds a rule, or replaces the reason, times and setter of
// the same rule. It returns the rule's id.
func (s *Store) AddSpamFilter(f spamfilter.Filter) (int64, error) {
	var id int64
	err := s.db.QueryRow(`INSERT INTO spamfilters (type, match, targets, action, ban_time, reason, set_by, set_at, expires) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(type, targets, action, match) DO UPDATE SET ban_time = excluded.ban_time, reason = excluded.reason,
		set_by = excluded.set_by, set_at = excluded.set_at, expires = excluded.expires RETURNING id`,
		f.Type, f.Match, f.Targets, f.Action, int64(f.BanTime/time.Second), f.Reason, f.SetBy, f.SetAt.Unix(), unixOrZero(f.Expires)).Scan(&id)
	return id, err
}

// DelSpamFilter removes rule id; false when there was none.
func (s *Store) DelSpamFilter(id int64) (bool, error) {
	return s.affected("DELETE FROM spamfilters WHERE id = ?", id)
}

// PurgeSpamFilters removes rules that ran out before now and returns them.
func (s *Store) PurgeSpamFilters(now time.Time) ([]spamfilter.Filter, error) {
	all, err := s.SpamFilters()
	if err != nil {
		return nil, err
	}
	var gone []spamfilter.Filter
	for _, f := range all {
		if !f.Live(now) {
			gone = append(gone, f)
		}
	}
	if len(gone) > 0 {
		if _, err := s.db.Exec("DELETE FROM spamfilters WHERE expires > 0 AND expires <= ?", now.Unix()); err != nil {
			return nil, err
		}
	}
	return gone, nil
}
