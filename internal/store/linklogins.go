package store

import (
	"database/sql"
	"time"
)

// Logins our services gave to people on other servers (the server's
// link_logins.go), kept so they survive a restart of this server: the
// people's own servers keep their UIDs, so after a restart the next burst
// can be matched against these.

const linkLoginSchema = `
CREATE TABLE IF NOT EXISTS link_logins (
	uid        TEXT PRIMARY KEY,
	account    TEXT NOT NULL,
	account_id INTEGER NOT NULL DEFAULT 0,
	gone       INTEGER NOT NULL DEFAULT 0
);
`

// LinkLogin is one such login. Gone is when the person left in a netsplit
// (or when they were logged in before they were on the network); zero while
// they are on the network.
type LinkLogin struct {
	UID, Account string
	AccountID    int64
	Gone         time.Time
}

// LinkLogins returns every saved login.
func (s *Store) LinkLogins() ([]LinkLogin, error) {
	var out []LinkLogin
	err := eachRow(s.db, "SELECT uid, account, account_id, gone FROM link_logins", func(r *sql.Rows) error {
		var l LinkLogin
		var gone int64
		if err := r.Scan(&l.UID, &l.Account, &l.AccountID, &gone); err != nil {
			return err
		}
		if gone != 0 {
			l.Gone = time.Unix(gone, 0)
		}
		out = append(out, l)
		return nil
	})
	return out, err
}

// SaveLinkLogins applies a batch in one transaction: with replace, the
// table is emptied first; then each login in save is written (replacing the
// one with its UID) and each UID in drop is deleted.
func (s *Store) SaveLinkLogins(save []LinkLogin, drop []string, replace bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if replace {
		if _, err := tx.Exec("DELETE FROM link_logins"); err != nil {
			return err
		}
	}
	if len(drop) > 0 {
		st, err := tx.Prepare("DELETE FROM link_logins WHERE uid = ?")
		if err != nil {
			return err
		}
		defer st.Close()
		for _, uid := range drop {
			if _, err := st.Exec(uid); err != nil {
				return err
			}
		}
	}
	if len(save) > 0 {
		st, err := tx.Prepare("INSERT OR REPLACE INTO link_logins (uid, account, account_id, gone) VALUES (?, ?, ?, ?)")
		if err != nil {
			return err
		}
		defer st.Close()
		for _, l := range save {
			var gone int64
			if !l.Gone.IsZero() {
				gone = l.Gone.Unix()
			}
			if _, err := st.Exec(l.UID, l.Account, l.AccountID, gone); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
