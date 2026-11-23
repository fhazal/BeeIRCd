package store

import (
	"database/sql"
	"errors"
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
CREATE TABLE IF NOT EXISTS spamfilter_removed (
	type       TEXT NOT NULL,
	targets    TEXT NOT NULL,
	action     TEXT NOT NULL,
	match      TEXT NOT NULL,
	set_at     INTEGER NOT NULL,
	removed_at INTEGER NOT NULL,
	keep_until INTEGER NOT NULL,
	PRIMARY KEY (type, targets, action, match)
);
`

// A removed rule that goes over links is remembered for a while (the same
// type, targets, action and match, and when it had been set), so a server
// that was split off at the time can't bring it back in its burst. It is
// forgotten when the rule would have run out, after RemovedBanKeep at the
// latest, and only the newest RemovedBanMax are kept.

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
// the same rule. It returns the rule's id. A remembered removal of the
// same rule is forgotten: this one is newer.
func (s *Store) AddSpamFilter(f spamfilter.Filter) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`INSERT INTO spamfilters (type, match, targets, action, ban_time, reason, set_by, set_at, expires) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(type, targets, action, match) DO UPDATE SET ban_time = excluded.ban_time, reason = excluded.reason,
		set_by = excluded.set_by, set_at = excluded.set_at, expires = excluded.expires RETURNING id`,
		f.Type, f.Match, f.Targets, f.Action, int64(f.BanTime/time.Second), f.Reason, f.SetBy, f.SetAt.Unix(), unixOrZero(f.Expires)).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("DELETE FROM spamfilter_removed WHERE type = ? AND targets = ? AND action = ? AND match = ?", f.Type, f.Targets, f.Action, f.Match); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DelSpamFilter removes rule id; false when there was none. The removal
// is remembered (see RemovedBanKeep) when the rule goes over links.
func (s *Store) DelSpamFilter(id int64) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var f spamfilter.Filter
	var at, exp int64
	err = tx.QueryRow("SELECT type, targets, action, match, set_at, expires FROM spamfilters WHERE id = ?", id).Scan(&f.Type, &f.Targets, &f.Action, &f.Match, &at, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if spamfilter.ActionLetter(f.Action) != 0 {
		now := time.Now()
		keep := now.Add(RemovedBanKeep).Unix()
		if exp > 0 && exp < keep {
			keep = exp
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO spamfilter_removed (type, targets, action, match, set_at, removed_at, keep_until) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			f.Type, f.Targets, f.Action, f.Match, at, now.Unix(), keep); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec("DELETE FROM spamfilters WHERE id = ?", id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RemovedSpamFilter reports when rule f (by type, targets, action and
// match), removed here, had been set, while the removal is remembered.
func (s *Store) RemovedSpamFilter(f spamfilter.Filter, now time.Time) (setAt time.Time, ok bool, err error) {
	var at int64
	err = s.db.QueryRow("SELECT set_at FROM spamfilter_removed WHERE type = ? AND targets = ? AND action = ? AND match = ? AND keep_until > ?",
		f.Type, f.Targets, f.Action, f.Match, now.Unix()).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(at, 0), true, nil
}

// PurgeSpamFilters removes rules that ran out before now and returns them.
func (s *Store) PurgeSpamFilters(now time.Time) ([]spamfilter.Filter, error) {
	all, err := s.SpamFilters()
	if err != nil {
		return nil, err
	}
	// Remembered removals past their time (or past RemovedBanMax) go too.
	if _, err := s.db.Exec("DELETE FROM spamfilter_removed WHERE keep_until <= ?", now.Unix()); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM spamfilter_removed WHERE rowid NOT IN
		(SELECT rowid FROM spamfilter_removed ORDER BY removed_at DESC, rowid DESC LIMIT ?)`, RemovedBanMax); err != nil {
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
