package store

import (
	"database/sql"
	"time"
)

// Network ban kinds. They share one table and one shape.
const (
	BanAkill  = "akill"  // user@host (host may be CIDR): disconnect, refuse
	BanZline  = "zline"  // IP or CIDR: refused before anything else
	BanSNLine = "snline" // realname mask or /regex/: disconnect, refuse
	BanShun   = "shun"   // user@host: connected, but can't do anything
	BanEline  = "eline"  // user@host: exempt from all of the above
	BanKline  = "kline"  // user@host, this server only (never sent over links)
)

// BanKinds are all kinds, in the order lists show them.
var BanKinds = []string{BanAkill, BanZline, BanSNLine, BanShun, BanEline, BanKline}

// NetBan is one network ban.
type NetBan struct {
	Kind    string
	Mask    string
	Reason  string
	SetBy   string
	SetAt   time.Time
	Expires time.Time // zero = until removed
}

// Live reports whether b is still in force at now.
func (b NetBan) Live(now time.Time) bool { return b.Expires.IsZero() || now.Before(b.Expires) }

// Akill is the old name of an AKILL entry.
type Akill = NetBan

const netbanSchema = `
CREATE TABLE IF NOT EXISTS netbans (
	kind    TEXT NOT NULL,
	mask    TEXT NOT NULL,
	reason  TEXT NOT NULL,
	set_by  TEXT NOT NULL,
	set_at  INTEGER NOT NULL,
	expires INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (kind, mask)
);
`

// migrateNetbans moves AKILLs from the old table and gives forbids and
// session exceptions an expiry. Safe on every start.
func migrateNetbans(db *sql.DB) error {
	for _, col := range []struct{ table, name string }{{"forbids", "expires"}, {"session_excepts", "expires"}} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", col.table, col.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE " + col.table + " ADD COLUMN " + col.name + " INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO netbans (kind, mask, reason, set_by, set_at, expires)
		SELECT 'akill', mask, reason, set_by, set_at, expires FROM akills`); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM akills"); err != nil {
		return err
	}
	return tx.Commit()
}

// NetBans returns every network ban of kind ("" = all), oldest first,
// expired ones included.
func (s *Store) NetBans(kind string) ([]NetBan, error) {
	q := "SELECT kind, mask, reason, set_by, set_at, expires FROM netbans"
	var args []any
	if kind != "" {
		q += " WHERE kind = ?"
		args = append(args, kind)
	}
	rows, err := s.db.Query(q+" ORDER BY set_at, mask", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NetBan
	for rows.Next() {
		var b NetBan
		var at, exp int64
		if err := rows.Scan(&b.Kind, &b.Mask, &b.Reason, &b.SetBy, &at, &exp); err != nil {
			return nil, err
		}
		b.SetAt = time.Unix(at, 0)
		if exp > 0 {
			b.Expires = time.Unix(exp, 0)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// AddNetBan adds or replaces a network ban.
func (s *Store) AddNetBan(b NetBan) error {
	var exp int64
	if !b.Expires.IsZero() {
		exp = b.Expires.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO netbans (kind, mask, reason, set_by, set_at, expires) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, mask) DO UPDATE SET reason = excluded.reason, set_by = excluded.set_by, set_at = excluded.set_at, expires = excluded.expires`,
		b.Kind, b.Mask, b.Reason, b.SetBy, b.SetAt.Unix(), exp)
	return err
}

// DelNetBan removes a network ban; false when there was none.
func (s *Store) DelNetBan(kind, mask string) (bool, error) {
	return s.affected("DELETE FROM netbans WHERE kind = ? AND mask = ?", kind, mask)
}

// Expired is something PurgeExpired removed.
type Expired struct {
	What string // "netban", "forbid" or "exception"
	Kind string // ban kind (BanAkill, ...) or forbid kind; "" for exceptions
	Mask string
}

// PurgeExpired removes network bans, forbids and session exceptions that
// ran out before now and returns what went.
func (s *Store) PurgeExpired(now time.Time) ([]Expired, error) {
	var gone []Expired
	for _, q := range []struct{ what, sel, del string }{
		{"netban", "SELECT kind, mask FROM netbans WHERE expires > 0 AND expires <= ?", "DELETE FROM netbans WHERE expires > 0 AND expires <= ?"},
		{"forbid", "SELECT kind, name FROM forbids WHERE expires > 0 AND expires <= ?", "DELETE FROM forbids WHERE expires > 0 AND expires <= ?"},
		{"exception", "SELECT '', mask FROM session_excepts WHERE expires > 0 AND expires <= ?", "DELETE FROM session_excepts WHERE expires > 0 AND expires <= ?"},
	} {
		rows, err := s.db.Query(q.sel, now.Unix())
		if err != nil {
			return gone, err
		}
		n := len(gone)
		for rows.Next() {
			e := Expired{What: q.what}
			if err := rows.Scan(&e.Kind, &e.Mask); err != nil {
				rows.Close()
				return gone, err
			}
			gone = append(gone, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return gone, err
		}
		if len(gone) == n {
			continue
		}
		if _, err := s.db.Exec(q.del, now.Unix()); err != nil {
			return gone, err
		}
	}
	return gone, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func zeroOrTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
