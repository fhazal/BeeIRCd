package store

import "time"

// The Hive Panel's "What happened" list. Every sign-in (and every
// refused one) and every change made from the panel is kept here, newest
// last; the last PanelLogKeep stay. AdminServ HISTORY keeps the setting
// changes themselves (with UNDO); this is who did what, from where.

// PanelLogKeep is how many panel events are kept.
const PanelLogKeep = 500

const panelSchema = `
CREATE TABLE IF NOT EXISTS panel_log (
	id   INTEGER PRIMARY KEY AUTOINCREMENT,
	at   INTEGER NOT NULL,
	who  TEXT NOT NULL,
	ip   TEXT NOT NULL,
	what TEXT NOT NULL
);
`

// PanelEvent is one line of the panel's log.
type PanelEvent struct {
	ID   int64
	At   time.Time
	Who  string // the oper's account, or the nick tried for a refused sign-in
	IP   string
	What string
}

// AddPanelEvent records e and drops the oldest beyond PanelLogKeep.
func (s *Store) AddPanelEvent(e PanelEvent) error {
	res, err := s.db.Exec("INSERT INTO panel_log (at, who, ip, what) VALUES (?, ?, ?, ?)", e.At.Unix(), e.Who, e.IP, e.What)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	_, err = s.db.Exec("DELETE FROM panel_log WHERE id <= ?", id-PanelLogKeep)
	return err
}

// PanelEvents returns the latest n events, newest first.
func (s *Store) PanelEvents(n int) ([]PanelEvent, error) {
	rows, err := s.db.Query("SELECT id, at, who, ip, what FROM panel_log ORDER BY id DESC LIMIT ?", n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PanelEvent
	for rows.Next() {
		var e PanelEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Who, &e.IP, &e.What); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
