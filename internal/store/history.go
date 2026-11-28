package store

import (
	"database/sql"
	"errors"
	"time"
)

// AdminServ HISTORY / DIFF / UNDO. Every change AdminServ makes
// to beeircd.conf or the oper list is recorded; the last HistoryKeep stay.
// Not cached: it is read only when an admin asks.

// HistoryKeep is how many changes are kept.
const HistoryKeep = 200

const historySchema = `
CREATE TABLE IF NOT EXISTS admin_history (
	id     INTEGER PRIMARY KEY AUTOINCREMENT,
	at     INTEGER NOT NULL,
	by     TEXT NOT NULL,
	kind   TEXT NOT NULL,
	key    TEXT NOT NULL,
	old    TEXT NOT NULL,
	new    TEXT NOT NULL
);
`

// Change is one recorded AdminServ change. Kind is "set", "motd", "omotd", "port",
// "oper" or "class"; Key what it was about (a setting, an oper's account,
// a class); Old and New the values before and after (encoded by kind).
type Change struct {
	ID       int64
	At       time.Time
	By       string
	Kind     string
	Key      string
	Old, New string
}

// AddChange records a change and drops the oldest beyond HistoryKeep.
func (s *Store) AddChange(ch Change) (int64, error) {
	res, err := s.db.Exec("INSERT INTO admin_history (at, by, kind, key, old, new) VALUES (?, ?, ?, ?, ?, ?)",
		ch.At.Unix(), ch.By, ch.Kind, ch.Key, ch.Old, ch.New)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	_, err = s.db.Exec("DELETE FROM admin_history WHERE id <= ?", id-HistoryKeep)
	return id, err
}

// Changes returns the latest n changes, newest first.
func (s *Store) Changes(n int) ([]Change, error) {
	rows, err := s.db.Query("SELECT id, at, by, kind, key, old, new FROM admin_history ORDER BY id DESC LIMIT ?", n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var at int64
		if err := rows.Scan(&c.ID, &at, &c.By, &c.Kind, &c.Key, &c.Old, &c.New); err != nil {
			return nil, err
		}
		c.At = time.Unix(at, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChangeByID returns one change; false when it isn't kept (any more).
func (s *Store) ChangeByID(id int64) (Change, bool, error) {
	var c Change
	var at int64
	err := s.db.QueryRow("SELECT id, at, by, kind, key, old, new FROM admin_history WHERE id = ?", id).
		Scan(&c.ID, &at, &c.By, &c.Kind, &c.Key, &c.Old, &c.New)
	if errors.Is(err, sql.ErrNoRows) {
		return Change{}, false, nil
	}
	c.At = time.Unix(at, 0)
	return c, err == nil, err
}
