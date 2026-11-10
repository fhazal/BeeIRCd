// Package store keeps services data (accounts, nicks, memos) in one SQLite
// file. It is pure Go, so no C compiler is needed.
//
// Nicks are stored with their rfc1459-folded form as key, so "Bee" and
// "bee" are the same nick. The server calls the store from its single loop
// goroutine.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

// ErrExists means the nick is already registered.
var ErrExists = errors.New("already registered")

// Account is one NickServ account. Name is the nick it was registered with.
type Account struct {
	ID         int64
	Name       string
	PassHash   string
	Email      string
	Registered time.Time
	LastSeen   time.Time
	LastHost   string
	Enforce    bool
	HideEmail  bool
	HideHost   bool
	MemoNotify bool
	MemoLimit  int
}

// Memo is one MemoServ message.
type Memo struct {
	ID   int64
	From string
	Sent time.Time
	Text string
	Read bool
}

// Store is the open database.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS accounts (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL,
	pass_hash   TEXT NOT NULL,
	email       TEXT NOT NULL DEFAULT '',
	registered  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	last_host   TEXT NOT NULL DEFAULT '',
	enforce     INTEGER NOT NULL DEFAULT 1,
	hide_email  INTEGER NOT NULL DEFAULT 1,
	hide_host   INTEGER NOT NULL DEFAULT 1,
	memo_notify INTEGER NOT NULL DEFAULT 1,
	memo_limit  INTEGER NOT NULL DEFAULT 50
);
CREATE TABLE IF NOT EXISTS nicks (
	nick_key   TEXT PRIMARY KEY,
	nick       TEXT NOT NULL,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	registered INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS nicks_account ON nicks(account_id);
CREATE TABLE IF NOT EXISTS access (
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	mask       TEXT NOT NULL,
	PRIMARY KEY (account_id, mask)
);
CREATE TABLE IF NOT EXISTS memos (
	id         INTEGER PRIMARY KEY,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	sender     TEXT NOT NULL,
	sent       INTEGER NOT NULL,
	body       TEXT NOT NULL,
	seen       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS memos_account ON memos(account_id);
CREATE TABLE IF NOT EXISTS memo_ignores (
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	nick_key   TEXT NOT NULL,
	nick       TEXT NOT NULL,
	PRIMARY KEY (account_id, nick_key)
);
INSERT OR IGNORE INTO meta (key, value) VALUES ('schema', '1');
`

// Open opens (and if needed creates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + channelSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("services database %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Check runs SQLite's own integrity check.
func (s *Store) Check() error {
	var res string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("integrity check: %s", res)
	}
	return nil
}

const accountCols = `a.id, a.name, a.pass_hash, a.email, a.registered, a.last_seen, a.last_host,
	a.enforce, a.hide_email, a.hide_host, a.memo_notify, a.memo_limit`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var reg, seen int64
	err := row.Scan(&a.ID, &a.Name, &a.PassHash, &a.Email, &reg, &seen, &a.LastHost,
		&a.Enforce, &a.HideEmail, &a.HideHost, &a.MemoNotify, &a.MemoLimit)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Registered, a.LastSeen = time.Unix(reg, 0), time.Unix(seen, 0)
	return &a, nil
}

// CreateAccount registers nick as a new account.
func (s *Store) CreateAccount(nick, passHash, email string, now time.Time) (*Account, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM nicks WHERE nick_key = ?", irc.Fold(nick)).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrExists
	}
	res, err := tx.Exec(`INSERT INTO accounts (name, pass_hash, email, registered, last_seen) VALUES (?, ?, ?, ?, ?)`,
		nick, passHash, email, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO nicks (nick_key, nick, account_id, registered) VALUES (?, ?, ?, ?)`,
		irc.Fold(nick), nick, id, now.Unix()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.AccountByID(id)
}

// AccountByNick finds the account owning nick, or nil.
func (s *Store) AccountByNick(nick string) (*Account, error) {
	return scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts a JOIN nicks n ON n.account_id = a.id WHERE n.nick_key = ?`, irc.Fold(nick)))
}

// AccountByID finds an account by ID, or nil.
func (s *Store) AccountByID(id int64) (*Account, error) {
	return scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`, id))
}

// SaveAccount writes every field of a back.
func (s *Store) SaveAccount(a *Account) error {
	_, err := s.db.Exec(`UPDATE accounts SET pass_hash=?, email=?, last_seen=?, last_host=?, enforce=?, hide_email=?,
		hide_host=?, memo_notify=?, memo_limit=? WHERE id=?`,
		a.PassHash, a.Email, a.LastSeen.Unix(), a.LastHost, a.Enforce, a.HideEmail, a.HideHost, a.MemoNotify, a.MemoLimit, a.ID)
	return err
}

// Nicks returns an account's nicks, the account name first.
func (s *Store) Nicks(a *Account) ([]string, error) {
	rows, err := s.db.Query("SELECT nick FROM nicks WHERE account_id = ? ORDER BY registered, nick_key", a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{a.Name}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if !irc.EqualFold(n, a.Name) {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// AddNick groups nick into account a.
func (s *Store) AddNick(a *Account, nick string, now time.Time) error {
	_, err := s.db.Exec("INSERT INTO nicks (nick_key, nick, account_id, registered) VALUES (?, ?, ?, ?)",
		irc.Fold(nick), nick, a.ID, now.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	return err
}

// RemoveNick ungroups nick.
func (s *Store) RemoveNick(nick string) error {
	_, err := s.db.Exec("DELETE FROM nicks WHERE nick_key = ?", irc.Fold(nick))
	return err
}

// NickEntry is one registered nick, for LIST.
type NickEntry struct {
	Nick    string
	Account string
}

// AllNicks returns every registered nick.
func (s *Store) AllNicks() ([]NickEntry, error) {
	rows, err := s.db.Query("SELECT n.nick, a.name FROM nicks n JOIN accounts a ON a.id = n.account_id ORDER BY n.nick_key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NickEntry
	for rows.Next() {
		var e NickEntry
		if err := rows.Scan(&e.Nick, &e.Account); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Access returns the host masks that identify a by themselves.
func (s *Store) Access(a *Account) ([]string, error) {
	return s.strings("SELECT mask FROM access WHERE account_id = ? ORDER BY mask", a.ID)
}

// AddAccess adds mask; false when it was there already.
func (s *Store) AddAccess(a *Account, mask string) (bool, error) {
	res, err := s.db.Exec("INSERT OR IGNORE INTO access (account_id, mask) VALUES (?, ?)", a.ID, mask)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DelAccess removes mask; false when it wasn't there.
func (s *Store) DelAccess(a *Account, mask string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM access WHERE account_id = ? AND mask = ?", a.ID, mask)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Expired returns accounts not seen since before.
func (s *Store) Expired(before time.Time) ([]*Account, error) {
	rows, err := s.db.Query(`SELECT `+accountCols+` FROM accounts a WHERE a.last_seen < ?`, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddMemo stores a memo for a.
func (s *Store) AddMemo(a *Account, from, text string, now time.Time) error {
	_, err := s.db.Exec("INSERT INTO memos (account_id, sender, sent, body) VALUES (?, ?, ?, ?)", a.ID, from, now.Unix(), text)
	return err
}

// Memos returns a's memos, oldest first. Their position (1, 2, ...) is the
// number people type.
func (s *Store) Memos(a *Account) ([]Memo, error) {
	rows, err := s.db.Query("SELECT id, sender, sent, body, seen FROM memos WHERE account_id = ? ORDER BY id", a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memo
	for rows.Next() {
		var m Memo
		var sent int64
		if err := rows.Scan(&m.ID, &m.From, &sent, &m.Text, &m.Read); err != nil {
			return nil, err
		}
		m.Sent = time.Unix(sent, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkRead marks one memo read.
func (s *Store) MarkRead(id int64) error {
	_, err := s.db.Exec("UPDATE memos SET seen = 1 WHERE id = ?", id)
	return err
}

// DeleteMemo deletes one memo.
func (s *Store) DeleteMemo(id int64) error {
	_, err := s.db.Exec("DELETE FROM memos WHERE id = ?", id)
	return err
}

// MemoIgnores returns the nicks whose memos a drops.
func (s *Store) MemoIgnores(a *Account) ([]string, error) {
	return s.strings("SELECT nick FROM memo_ignores WHERE account_id = ? ORDER BY nick_key", a.ID)
}

// AddMemoIgnore adds nick; false when it was there.
func (s *Store) AddMemoIgnore(a *Account, nick string) (bool, error) {
	res, err := s.db.Exec("INSERT OR IGNORE INTO memo_ignores (account_id, nick_key, nick) VALUES (?, ?, ?)", a.ID, irc.Fold(nick), nick)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DelMemoIgnore removes nick; false when it wasn't there.
func (s *Store) DelMemoIgnore(a *Account, nick string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM memo_ignores WHERE account_id = ? AND nick_key = ?", a.ID, irc.Fold(nick))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Ignores reports whether a drops memos from nick.
func (s *Store) Ignores(a *Account, nick string) (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM memo_ignores WHERE account_id = ? AND nick_key = ?", a.ID, irc.Fold(nick)).Scan(&n)
	return n > 0, err
}

// Counts returns the number of accounts, nicks and memos, for status.
func (s *Store) Counts() (accounts, nicks, memos int, err error) {
	err = s.db.QueryRow("SELECT (SELECT COUNT(*) FROM accounts), (SELECT COUNT(*) FROM nicks), (SELECT COUNT(*) FROM memos)").
		Scan(&accounts, &nicks, &memos)
	return
}

func (s *Store) strings(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
