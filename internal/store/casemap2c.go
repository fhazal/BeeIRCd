package store

import (
	"database/sql"
	"fmt"

	"github.com/fhazal/beeircd/internal/irc"
)

// Names are stored with a folded key (nick_key, name_key, ...) made with
// the casemapping in use (Stage 2c: ascii by default, rfc1459 before).
// When the setting changes, every key is made again from the name next
// to it. Databases from before 2c have no 'casemapping' row: rfc1459.

// foldedKeys are the folded key columns and the name each is made from.
var foldedKeys = []struct{ table, key, from string }{
	{"nicks", "nick_key", "nick"},
	{"buddies", "nick_key", "nick"},
	{"memo_ignores", "nick_key", "nick"},
	{"chan_memo_ignores", "nick_key", "nick"},
	{"seen", "nick_key", "nick"},
	{"forbids", "name_key", "name"},
	{"svc_ignores", "mask_key", "mask"},
	{"channels", "name_key", "name"},
	{"ajoin", "chan_key", "channel"},
}

// nickChildren point at nicks(nick_key) and move with it.
var nickChildren = []string{"vhosts", "vhost_requests"}

func migrateCasemapping(db *sql.DB) (string, error) {
	want := irc.Casemapping()
	var have string
	switch err := db.QueryRow("SELECT value FROM meta WHERE key = 'casemapping'").Scan(&have); {
	case err == sql.ErrNoRows:
		var n int
		if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM nicks) + (SELECT COUNT(*) FROM channels) + (SELECT COUNT(*) FROM forbids) + (SELECT COUNT(*) FROM seen)").Scan(&n); err != nil {
			return "", err
		}
		have = irc.CaseRFC1459
		if n == 0 {
			have = want
		}
	case err != nil:
		return "", err
	}
	if have == want {
		_, err := db.Exec("INSERT INTO meta (key, value) VALUES ('casemapping', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", want)
		return "", err
	}
	rfc := want == irc.CaseRFC1459
	// Children of nicks are moved by hand, so the foreign key check waits
	// until every key is new (it can't be switched inside a transaction).
	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return "", err
	}
	defer db.Exec("PRAGMA foreign_keys = ON")
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	moved := 0
	for _, k := range foldedKeys {
		rows, err := tx.Query(fmt.Sprintf("SELECT rowid, %s, %s FROM %s", k.key, k.from, k.table))
		if err != nil {
			return "", err
		}
		type change struct {
			id       int64
			old, new string
			name     string
		}
		var todo []change
		for rows.Next() {
			var c change
			if err := rows.Scan(&c.id, &c.old, &c.name); err != nil {
				rows.Close()
				return "", err
			}
			if c.old == "" {
				continue
			}
			if c.new = irc.FoldWith(rfc, c.name); c.new != c.old {
				todo = append(todo, c)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		for _, c := range todo {
			if _, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE rowid = ?", k.table, k.key), c.new, c.id); err != nil {
				return "", fmt.Errorf("casemapping %s: %q is the same name as another one in %s with %s (%v); keep the old casemapping or remove one of them first", want, c.name, k.table, want, err)
			}
			if k.table == "nicks" {
				for _, t := range nickChildren {
					if _, err := tx.Exec("UPDATE "+t+" SET nick_key = ? WHERE nick_key = ?", c.new, c.old); err != nil {
						return "", err
					}
				}
			}
			moved++
		}
	}
	if _, err := tx.Exec("INSERT INTO meta (key, value) VALUES ('casemapping', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", want); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	var bad int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&bad); err == nil && bad > 0 {
		return "", fmt.Errorf("casemapping %s: %d rows lost their nick after the move", want, bad)
	}
	return fmt.Sprintf("casemapping changed from %s to %s: %d stored names got new keys", have, want, moved), nil
}
