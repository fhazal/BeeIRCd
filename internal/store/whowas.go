package store

import (
	"database/sql"
	"time"
)

// WHOWAS history: who used which nick lately, kept across restarts
// (UnrealIRCd's whowasdb). The server holds it in memory and saves it all
// every few minutes and when it stops.

const whowasSchema = `
CREATE TABLE IF NOT EXISTS whowas (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	nick     TEXT NOT NULL,
	ident    TEXT NOT NULL,
	host     TEXT NOT NULL,
	realname TEXT NOT NULL,
	server   TEXT NOT NULL,
	at       INTEGER NOT NULL
);
`

// Whowas is one WHOWAS entry.
type Whowas struct {
	Nick, Ident, Host, Realname, Server string
	At                                  time.Time
}

// Whowas returns every saved entry, oldest first.
func (s *Store) Whowas() ([]Whowas, error) {
	var out []Whowas
	err := eachRow(s.db, "SELECT nick, ident, host, realname, server, at FROM whowas ORDER BY id", func(r *sql.Rows) error {
		var e Whowas
		var at int64
		if err := r.Scan(&e.Nick, &e.Ident, &e.Host, &e.Realname, &e.Server, &at); err != nil {
			return err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
		return nil
	})
	return out, err
}

// SaveWhowas replaces the saved entries with all (oldest first).
func (s *Store) SaveWhowas(all []Whowas) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM whowas"); err != nil {
		return err
	}
	st, err := tx.Prepare("INSERT INTO whowas (nick, ident, host, realname, server, at) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer st.Close()
	for _, e := range all {
		if _, err := st.Exec(e.Nick, e.Ident, e.Host, e.Realname, e.Server, e.At.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
