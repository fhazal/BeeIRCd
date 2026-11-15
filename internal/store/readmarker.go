package store

import (
	"database/sql"
	"time"
)

// Read markers (IRCv3 draft/read-marker): how far an account has read in
// each channel or private chat. Keys are case-folded.

const readMarkerSchema = `
CREATE TABLE IF NOT EXISTS read_markers (
	account TEXT NOT NULL,
	target  TEXT NOT NULL,
	at_ms   INTEGER NOT NULL,
	PRIMARY KEY (account, target)
);
`

// ReadMarker is one account's marker for one target.
type ReadMarker struct {
	Account, Target string
	At              time.Time
}

// ReadMarkers returns every marker.
func (s *Store) ReadMarkers() ([]ReadMarker, error) {
	var out []ReadMarker
	err := eachRow(s.db, "SELECT account, target, at_ms FROM read_markers", func(r *sql.Rows) error {
		var m ReadMarker
		var ms int64
		if err := r.Scan(&m.Account, &m.Target, &ms); err != nil {
			return err
		}
		m.At = time.UnixMilli(ms).UTC()
		out = append(out, m)
		return nil
	})
	return out, err
}

// SetReadMarker keeps m unless a later marker is there already.
func (s *Store) SetReadMarker(m ReadMarker) error {
	_, err := s.db.Exec("INSERT INTO read_markers (account, target, at_ms) VALUES (?, ?, ?) "+
		"ON CONFLICT(account, target) DO UPDATE SET at_ms = excluded.at_ms WHERE excluded.at_ms > read_markers.at_ms",
		m.Account, m.Target, m.At.UnixMilli())
	return err
}
