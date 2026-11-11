package store

import (
	"database/sql"
	"sort"

	"github.com/fhazal/beeircd/internal/irc"
)

// Step 1.5.6b: MemoServ SET NOTIFY {ON|LOGON|NEW|OFF} and MAIL, the
// 2.0.20 memo limit (20) and NickServ BUDDY (a ConferenceRoom extra).
//
// MemoNotify is Anope's MEMO_RECEIVE (told when a memo arrives),
// MemoSignon its MEMO_SIGNON (told when you log on), MemoMail MEMO_MAIL.

// DefaultMemoLimit is a new account's memo limit (Anope's maxmemos).
const DefaultMemoLimit = 20

const ms156Schema = `
CREATE TABLE IF NOT EXISTS buddies (
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	nick_key   TEXT NOT NULL,
	nick       TEXT NOT NULL,
	PRIMARY KEY (account_id, nick_key)
);
`

func migrateMS156(db *sql.DB) error {
	for _, col := range []struct{ name, def string }{
		{"memo_signon", "INTEGER NOT NULL DEFAULT 1"},
		{"memo_mail", "INTEGER NOT NULL DEFAULT 0"},
	} {
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
	// Once: accounts still on the old default limit (50) move to Anope's 20.
	var done int
	if err := db.QueryRow("SELECT COUNT(*) FROM meta WHERE key = 'memo_limit_20'").Scan(&done); err != nil {
		return err
	}
	if done == 0 {
		if _, err := db.Exec("UPDATE accounts SET memo_limit = 20 WHERE memo_limit = 50"); err != nil {
			return err
		}
		if _, err := db.Exec("INSERT INTO meta (key, value) VALUES ('memo_limit_20', '1')"); err != nil {
			return err
		}
	}
	return nil
}

func loadMS156(db *sql.DB, c *cache) error {
	return eachRow(db, "SELECT account_id, nick_key, nick FROM buddies ORDER BY nick_key", func(r *sql.Rows) error {
		var id int64
		var g ignoreRow
		if err := r.Scan(&id, &g.key, &g.nick); err != nil {
			return err
		}
		c.buddies[id] = append(c.buddies[id], g)
		return nil
	})
}

// Buddies returns the nicks on a's BUDDY list, sorted.
func (s *Store) Buddies(a *Account) []string {
	var out []string
	for _, g := range s.c.buddies[a.ID] {
		out = append(out, g.nick)
	}
	return out
}

// AddBuddy puts nick on a's list; false when it was there.
func (s *Store) AddBuddy(a *Account, nick string) (bool, error) {
	if s.c.accounts[a.ID] == nil {
		return false, nil
	}
	key := irc.Fold(nick)
	added, err := s.affected("INSERT OR IGNORE INTO buddies (account_id, nick_key, nick) VALUES (?, ?, ?)", a.ID, key, nick)
	if err != nil || !added {
		return added, err
	}
	list := append(append([]ignoreRow(nil), s.c.buddies[a.ID]...), ignoreRow{key: key, nick: nick})
	sort.Slice(list, func(i, j int) bool { return list[i].key < list[j].key })
	s.c.buddies[a.ID] = list
	return true, nil
}

// DelBuddy takes nick off a's list; false when it wasn't there.
func (s *Store) DelBuddy(a *Account, nick string) (bool, error) {
	key := irc.Fold(nick)
	gone, err := s.affected("DELETE FROM buddies WHERE account_id = ? AND nick_key = ?", a.ID, key)
	if err != nil || !gone {
		return gone, err
	}
	s.c.buddies[a.ID] = without(s.c.buddies[a.ID], func(g ignoreRow) bool { return g.key == key })
	return true, nil
}

// AllAccounts returns every account, by ID.
func (s *Store) AllAccounts() []*Account {
	out := make([]*Account, 0, len(s.c.accounts))
	for id := range s.c.accounts {
		out = append(out, s.c.account(id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BuddyOf returns the accounts with nick on their BUDDY list.
func (s *Store) BuddyOf(nick string) []int64 {
	key := irc.Fold(nick)
	var out []int64
	for id, list := range s.c.buddies {
		for _, g := range list {
			if g.key == key {
				out = append(out, id)
				break
			}
		}
	}
	return out
}
