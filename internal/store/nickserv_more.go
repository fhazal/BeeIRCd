package store

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// NickServ data: certificate fingerprints (CERT),
// the auto join list (AJOIN) and the account columns for SET AUTOOP,
// KEEPMODES, PRIVATE, URL, SASET NOEXPIRE and SUSPEND.

// AjoinEntry is one channel on an account's auto join list.
type AjoinEntry struct {
	ID      int64
	Channel string
	Key     string
}

const nsMoreSchema = `
CREATE TABLE IF NOT EXISTS certs (
	fingerprint TEXT PRIMARY KEY,
	account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	added_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ajoin (
	id         INTEGER PRIMARY KEY,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	channel    TEXT NOT NULL,
	chan_key   TEXT NOT NULL DEFAULT ''
);
`

// nsMoreColumns are the newer account columns, with Anope 2.0.20's
// defaults (nickserv { defaults = "... ns_private ... autoop" }).
var nsMoreColumns = []struct{ name, def string }{
	{"autoop", "INTEGER NOT NULL DEFAULT 1"},
	{"keepmodes", "INTEGER NOT NULL DEFAULT 0"},
	{"last_umodes", "TEXT NOT NULL DEFAULT ''"},
	{"ns_private", "INTEGER NOT NULL DEFAULT 1"},
	{"url", "TEXT NOT NULL DEFAULT ''"},
	{"noexpire", "INTEGER NOT NULL DEFAULT 0"},
	{"suspended_by", "TEXT NOT NULL DEFAULT ''"},
	{"suspend_reason", "TEXT NOT NULL DEFAULT ''"},
	{"suspended_at", "INTEGER NOT NULL DEFAULT 0"},
	{"suspend_until", "INTEGER NOT NULL DEFAULT 0"},
	{"unconfirmed", "INTEGER NOT NULL DEFAULT 0"},
	{"confirm_code", "TEXT NOT NULL DEFAULT ''"},
	{"language", "TEXT NOT NULL DEFAULT ''"},
	{"neverop", "INTEGER NOT NULL DEFAULT 0"},
}

func migrateNSMore(db *sql.DB) error {
	for _, col := range nsMoreColumns {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = ?", col.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE accounts ADD COLUMN " + col.name + " " + col.def); err != nil {
				return err
			}
		}
	}
	return nil
}

// Suspended reports whether an oper suspended the account and it hasn't
// run out at now.
func (a *Account) Suspended(now time.Time) bool {
	return a.SuspendedBy != "" && (a.SuspendUntil.Unix() == 0 || now.Before(a.SuspendUntil))
}

func loadNSMore(db *sql.DB, c *cache) error {
	if err := eachRow(db, "SELECT fingerprint, account_id FROM certs ORDER BY added_at, fingerprint", func(r *sql.Rows) error {
		var fp string
		var id int64
		if err := r.Scan(&fp, &id); err != nil {
			return err
		}
		c.certs[id] = append(c.certs[id], fp)
		c.certOwner[fp] = id
		return nil
	}); err != nil {
		return err
	}
	return eachRow(db, "SELECT id, account_id, channel, chan_key FROM ajoin ORDER BY id", func(r *sql.Rows) error {
		var e AjoinEntry
		var id int64
		if err := r.Scan(&e.ID, &id, &e.Channel, &e.Key); err != nil {
			return err
		}
		c.ajoins[id] = append(c.ajoins[id], e)
		return nil
	})
}

// NormalizeFingerprint is a fingerprint the way certs stores it: lower case
// hex, without colons.
func NormalizeFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(fp, ":", ""))
}

// Certs returns an account's fingerprints, oldest first.
func (s *Store) Certs(a *Account) []string { return append([]string(nil), s.c.certs[a.ID]...) }

// AccountByCert finds the account with fingerprint fp on its CERT list.
func (s *Store) AccountByCert(fp string) *Account {
	id, ok := s.c.certOwner[NormalizeFingerprint(fp)]
	if !ok {
		return nil
	}
	return s.c.account(id)
}

// AddCert puts fp on a's list; false when some account has it already.
func (s *Store) AddCert(a *Account, fp string, now time.Time) (bool, error) {
	fp = NormalizeFingerprint(fp)
	if s.c.accounts[a.ID] == nil {
		return false, nil
	}
	added, err := s.affected("INSERT OR IGNORE INTO certs (fingerprint, account_id, added_at) VALUES (?, ?, ?)", fp, a.ID, now.Unix())
	if err != nil || !added {
		return added, err
	}
	s.c.certs[a.ID] = append(append([]string(nil), s.c.certs[a.ID]...), fp)
	s.c.certOwner[fp] = a.ID
	return true, nil
}

// DelCert takes fp off a's list; false when it wasn't there.
func (s *Store) DelCert(a *Account, fp string) (bool, error) {
	fp = NormalizeFingerprint(fp)
	gone, err := s.affected("DELETE FROM certs WHERE fingerprint = ? AND account_id = ?", fp, a.ID)
	if err != nil || !gone {
		return gone, err
	}
	s.c.certs[a.ID] = without(s.c.certs[a.ID], func(f string) bool { return f == fp })
	delete(s.c.certOwner, fp)
	return true, nil
}

// Ajoins returns an account's auto join list in the order it was added.
func (s *Store) Ajoins(a *Account) []AjoinEntry {
	return append([]AjoinEntry(nil), s.c.ajoins[a.ID]...)
}

// AddAjoin adds a channel to a's auto join list.
func (s *Store) AddAjoin(a *Account, channel, key string) error {
	if s.c.accounts[a.ID] == nil {
		return nil
	}
	res, err := s.db.Exec("INSERT INTO ajoin (account_id, channel, chan_key) VALUES (?, ?, ?)", a.ID, channel, key)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	s.c.ajoins[a.ID] = append(append([]AjoinEntry(nil), s.c.ajoins[a.ID]...), AjoinEntry{ID: id, Channel: channel, Key: key})
	return nil
}

// DelAjoin removes channel from a's auto join list; false when it wasn't on
// it.
func (s *Store) DelAjoin(a *Account, channel string) (bool, error) {
	var id int64
	for _, e := range s.c.ajoins[a.ID] {
		if irc.EqualFold(e.Channel, channel) {
			id = e.ID
		}
	}
	if id == 0 {
		return false, nil
	}
	if _, err := s.db.Exec("DELETE FROM ajoin WHERE id = ?", id); err != nil {
		return false, err
	}
	s.c.ajoins[a.ID] = without(s.c.ajoins[a.ID], func(e AjoinEntry) bool { return e.ID == id })
	return true, nil
}

// SuspendedAccounts returns every account with a suspension (run out or
// not).
func (s *Store) SuspendedAccounts() []*Account {
	var out []*Account
	for id, a := range s.c.accounts {
		if a.SuspendedBy != "" {
			out = append(out, s.c.account(id))
		}
	}
	return out
}

// AccountsByEmail returns the accounts whose email matches pattern (with
// * and ?), by name.
func (s *Store) AccountsByEmail(pattern string) []*Account {
	var out []*Account
	for id, a := range s.c.accounts {
		if a.Email != "" && irc.Match(pattern, a.Email) {
			out = append(out, s.c.account(id))
		}
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Name) < irc.Fold(out[j].Name) })
	return out
}
