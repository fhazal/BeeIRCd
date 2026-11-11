package store

// Step 7b: what `beeircd check` and `beeircd backup` need from the
// services database.

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// Summary is what check reports about a database.
type Summary struct {
	Nicks, Channels, Memos int
}

// Summary counts nicks, registered channels and memos in the open store.
func (s *Store) Summary() Summary {
	return Summary{Nicks: len(s.c.nicks), Channels: len(s.c.channels), Memos: len(s.c.memoOwner)}
}

// CheckFile checks a database no server has open: read only, SQLite's
// quick check, then the counts. A database a running server holds is
// locked; ErrInUse says so.
func CheckFile(path string) (Summary, error) {
	if _, err := os.Stat(path); err != nil {
		return Summary{}, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(500)")
	if err != nil {
		return Summary{}, err
	}
	defer db.Close()
	var res string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&res); err != nil {
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "busy") {
			return Summary{}, ErrInUse
		}
		return Summary{}, err
	}
	if res != "ok" {
		return Summary{}, fmt.Errorf("integrity check: %s", res)
	}
	var sum Summary
	for _, q := range []struct {
		table string
		n     *int
	}{{"nicks", &sum.Nicks}, {"channels", &sum.Channels}, {"memos", &sum.Memos}} {
		if err := db.QueryRow("SELECT COUNT(*) FROM " + q.table).Scan(q.n); err != nil {
			return Summary{}, err
		}
	}
	return sum, nil
}

// BackupTo writes a consistent copy of the open database to path (VACUUM
// INTO, on the server's own connection, so the exclusive lock is no
// problem). path must not exist yet.
func (s *Store) BackupTo(path string) error {
	_, err := s.db.Exec("VACUUM INTO ?", path)
	return err
}
