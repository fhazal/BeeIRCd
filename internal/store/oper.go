package store

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Oper is one oper entry: an account with flags, a title and the host
// masks it may oper up from.
type Oper struct {
	AccountID int64
	Account   string
	Flags     string
	Title     string
	Hosts     []string
	PassHash  string // the oper password (argon2id), "" = not set yet
	AddedBy   string
	AddedAt   time.Time
}

// Forbid stops a nick or channel from being used or registered.
type Forbid struct {
	Name    string
	Reason  string
	SetBy   string
	SetAt   time.Time
	Expires time.Time // zero = until removed
}

// News is one line shown to everyone after the MOTD.
type News struct {
	ID    int64
	Text  string
	SetBy string
	SetAt time.Time
}

// SessionExcept lets an IP mask have more connections than the limit.
type SessionExcept struct {
	Mask    string
	Max     int
	Reason  string
	SetBy   string
	SetAt   time.Time
	Expires time.Time // zero = until removed
}

const operSchema = `
CREATE TABLE IF NOT EXISTS opers (
	account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
	flags      TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	hosts      TEXT NOT NULL DEFAULT '',
	pass_hash  TEXT NOT NULL DEFAULT '',
	added_by   TEXT NOT NULL DEFAULT '',
	added_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS akills (
	mask    TEXT PRIMARY KEY,
	reason  TEXT NOT NULL,
	set_by  TEXT NOT NULL,
	set_at  INTEGER NOT NULL,
	expires INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS forbids (
	kind     TEXT NOT NULL,
	name_key TEXT NOT NULL,
	name     TEXT NOT NULL,
	reason   TEXT NOT NULL,
	set_by   TEXT NOT NULL,
	set_at   INTEGER NOT NULL,
	PRIMARY KEY (kind, name_key)
);
CREATE TABLE IF NOT EXISTS news (
	id     INTEGER PRIMARY KEY,
	body   TEXT NOT NULL,
	set_by TEXT NOT NULL,
	set_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS session_excepts (
	mask   TEXT PRIMARY KEY,
	max    INTEGER NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	set_by TEXT NOT NULL,
	set_at INTEGER NOT NULL
);
`

// Opers returns every oper entry, by account name.
func (s *Store) Opers() ([]Oper, error) {
	var out []Oper
	for id := range s.c.opers {
		out = append(out, *s.c.oper(id))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Account) < strings.ToLower(out[j].Account) })
	return out, nil
}

func scanOper(row interface{ Scan(...any) error }) (*Oper, error) {
	var o Oper
	var hosts string
	var at int64
	err := row.Scan(&o.AccountID, &o.Account, &o.Flags, &o.Title, &hosts, &o.PassHash, &o.AddedBy, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Hosts = strings.Fields(hosts)
	o.AddedAt = time.Unix(at, 0)
	return &o, nil
}

// OperByAccount returns the oper entry of an account, or nil.
func (s *Store) OperByAccount(id int64) (*Oper, error) { return s.c.oper(id), nil }

// SaveOper adds or replaces an oper entry. A replaced entry keeps who added
// it and when.
func (s *Store) SaveOper(o *Oper) error {
	if s.c.accounts[o.AccountID] == nil {
		return errors.New("no such account")
	}
	_, err := s.db.Exec(`INSERT INTO opers (account_id, flags, title, hosts, pass_hash, added_by, added_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET flags = excluded.flags, title = excluded.title, hosts = excluded.hosts, pass_hash = excluded.pass_hash`,
		o.AccountID, o.Flags, o.Title, strings.Join(o.Hosts, " "), o.PassHash, o.AddedBy, o.AddedAt.Unix())
	if err != nil {
		return err
	}
	cp := *o
	cp.Account = ""
	cp.Hosts = strings.Fields(strings.Join(o.Hosts, " "))
	if len(cp.Hosts) == 0 {
		cp.Hosts = nil
	}
	cp.AddedAt = sec(o.AddedAt)
	if have := s.c.opers[o.AccountID]; have != nil {
		cp.AddedBy, cp.AddedAt = have.AddedBy, have.AddedAt
	}
	s.c.opers[o.AccountID] = &cp
	return nil
}

// DeleteOper removes an oper entry; false when there was none.
func (s *Store) DeleteOper(id int64) (bool, error) {
	gone, err := s.affected("DELETE FROM opers WHERE account_id = ?", id)
	if err == nil && gone {
		delete(s.c.opers, id)
	}
	return gone, err
}

// Forbid kinds, as in Anope's OperServ FORBID. Names may hold * and ?.
const (
	ForbidNick     = "nick"     // nobody but opers may use or register it
	ForbidChan     = "chan"     // nobody but opers may join or register it
	ForbidEmail    = "email"    // no account may use a matching email
	ForbidRegister = "register" // may be used, but not registered
)

// ForbidKinds are all kinds, in the order lists show them.
var ForbidKinds = []string{ForbidNick, ForbidChan, ForbidEmail, ForbidRegister}

// Forbids returns the forbidden nicks or channels.
func (s *Store) Forbids(kind string) ([]Forbid, error) {
	rows, err := s.db.Query("SELECT name, reason, set_by, set_at, expires FROM forbids WHERE kind = ? ORDER BY name_key", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Forbid
	for rows.Next() {
		var f Forbid
		var at, exp int64
		if err := rows.Scan(&f.Name, &f.Reason, &f.SetBy, &at, &exp); err != nil {
			return nil, err
		}
		f.SetAt, f.Expires = time.Unix(at, 0), zeroOrTime(exp)
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddForbid forbids a nick or channel; false when it was already.
func (s *Store) AddForbid(kind string, f Forbid) (bool, error) {
	return s.affected("INSERT OR IGNORE INTO forbids (kind, name_key, name, reason, set_by, set_at, expires) VALUES (?, ?, ?, ?, ?, ?, ?)",
		kind, irc.Fold(f.Name), f.Name, f.Reason, f.SetBy, f.SetAt.Unix(), unixOrZero(f.Expires))
}

// DelForbid lifts a forbid; false when there was none.
func (s *Store) DelForbid(kind, name string) (bool, error) {
	return s.affected("DELETE FROM forbids WHERE kind = ? AND name_key = ?", kind, irc.Fold(name))
}

// AllNews returns the news lines, oldest first.
func (s *Store) AllNews() ([]News, error) {
	rows, err := s.db.Query("SELECT id, body, set_by, set_at FROM news ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []News
	for rows.Next() {
		var n News
		var at int64
		if err := rows.Scan(&n.ID, &n.Text, &n.SetBy, &at); err != nil {
			return nil, err
		}
		n.SetAt = time.Unix(at, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNews adds a news line.
func (s *Store) AddNews(text, by string, now time.Time) error {
	_, err := s.db.Exec("INSERT INTO news (body, set_by, set_at) VALUES (?, ?, ?)", text, by, now.Unix())
	return err
}

// DelNews removes a news line by ID.
func (s *Store) DelNews(id int64) (bool, error) {
	return s.affected("DELETE FROM news WHERE id = ?", id)
}

// SessionExcepts returns every session exception.
func (s *Store) SessionExcepts() ([]SessionExcept, error) {
	rows, err := s.db.Query("SELECT mask, max, reason, set_by, set_at, expires FROM session_excepts ORDER BY mask")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionExcept
	for rows.Next() {
		var e SessionExcept
		var at, exp int64
		if err := rows.Scan(&e.Mask, &e.Max, &e.Reason, &e.SetBy, &at, &exp); err != nil {
			return nil, err
		}
		e.SetAt, e.Expires = time.Unix(at, 0), zeroOrTime(exp)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveSessionExcept adds or replaces a session exception.
func (s *Store) SaveSessionExcept(e SessionExcept) error {
	_, err := s.db.Exec(`INSERT INTO session_excepts (mask, max, reason, set_by, set_at, expires) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(mask) DO UPDATE SET max = excluded.max, reason = excluded.reason, set_by = excluded.set_by, set_at = excluded.set_at,
		expires = excluded.expires`,
		e.Mask, e.Max, e.Reason, e.SetBy, e.SetAt.Unix(), unixOrZero(e.Expires))
	return err
}

// DelSessionExcept removes a session exception; false when there was none.
func (s *Store) DelSessionExcept(mask string) (bool, error) {
	return s.affected("DELETE FROM session_excepts WHERE mask = ?", mask)
}

// migrateOpers adds columns that older databases lack.
func migrateOpers(db *sql.DB) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('opers') WHERE name = 'pass_hash'").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := db.Exec("ALTER TABLE opers ADD COLUMN pass_hash TEXT NOT NULL DEFAULT ''")
		return err
	}
	return nil
}

func (s *Store) affected(query string, args ...any) (bool, error) {
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetFounder hands a channel to a new founder, who leaves the access list.
func (s *Store) SetFounder(c *Channel, a *Account) error {
	have := s.c.channels[c.ID]
	if have == nil || s.c.accounts[a.ID] == nil {
		return errors.New("no such channel or account")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM chan_access WHERE channel_id = ? AND account_id = ?", c.ID, a.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE channels SET founder_id = ?, successor_id = CASE WHEN successor_id = ? THEN NULL ELSE successor_id END WHERE id = ?",
		a.ID, a.ID, c.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(s.c.levels[c.ID], a.ID)
	have.FounderID = a.ID
	if have.SuccessorID == a.ID {
		have.SuccessorID = 0
	}
	return nil
}
