// Package store keeps services data (accounts, nicks, memos, channels,
// opers) in one SQLite file. It is pure Go, so no C compiler is needed.
//
// Everything is also kept in memory (see cache.go): reads never touch the
// disk, writes go to SQLite first and then to memory. Read methods return
// copies, so callers may change what they get and pass it to a Save method.
//
// Nicks are stored with their rfc1459-folded form as key, so "Bee" and
// "bee" are the same nick. The server calls the store from its single loop
// goroutine.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

// ErrExists means the nick is already registered.
var ErrExists = errors.New("already registered")

// ErrInUse means another BeeIRCd has the database open.
var ErrInUse = errors.New("another BeeIRCd is using this database")

// Account is one NickServ account. Name is the nick it was registered with.
type Account struct {
	ID         int64
	Name       string
	PassHash   string
	Email      string
	Registered time.Time
	LastSeen   time.Time
	LastHost   string
	Enforce    bool // SET KILL on (kill protection)
	Secure     bool // SET SECURE: the access list never stands in for the password
	HideEmail  bool
	HideHost   bool // HIDE MASK
	HideStatus bool // HIDE STATUS: don't show whether they're online
	HideQuit   bool // HIDE QUIT: don't show the last quit message
	LastQuit   string
	Protect    int // SET KILL: KillOn (the server's identify_within), KillQuick or KillImmed
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

// Store is the open database and its in-memory copy.
type Store struct {
	db    *sql.DB
	c     *cache
	notes []string // what the migrations changed, for the server log
}

// SET KILL modes, kept in Account.Protect.
const (
	KillOn    = 0  // the server's identify_within (60 s by default)
	KillQuick = 20 // seconds
	KillImmed = -1 // renamed at once, no warning
)

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
	memo_limit  INTEGER NOT NULL DEFAULT 50,
	hide_status INTEGER NOT NULL DEFAULT 0,
	hide_quit   INTEGER NOT NULL DEFAULT 0,
	last_quit   TEXT NOT NULL DEFAULT '',
	protect_delay INTEGER NOT NULL DEFAULT 0,
	secure      INTEGER NOT NULL DEFAULT 1
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

// Open opens (and if needed creates) the database at path and loads it
// into memory.
//
// synchronous(NORMAL) with WAL never loses a commit when BeeIRCd itself
// crashes; only a power cut can drop the last moments of writes. It keeps
// writes well under a millisecond, which matters because they run on the
// server loop.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(1000)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) {
		db.Close()
		return nil, fmt.Errorf("services database %s: %w", path, err)
	}
	// One BeeIRCd per database: the data lives in memory, so a second
	// server on the same file would overwrite the first one's changes.
	// Exclusive locking holds the lock until Close.
	if _, err := db.Exec("PRAGMA locking_mode = EXCLUSIVE; BEGIN IMMEDIATE; COMMIT"); err != nil {
		if strings.Contains(err.Error(), "locked") || strings.Contains(err.Error(), "busy") {
			return fail(ErrInUse)
		}
		return fail(err)
	}
	if _, err := db.Exec(schema + channelSchema + operSchema + chanExtraSchema + netbanSchema); err != nil {
		return fail(err)
	}
	if err := migrateLevels(db); err != nil {
		return fail(err)
	}
	if err := migrateOpers(db); err != nil {
		return fail(err)
	}
	if err := migrateAccounts(db); err != nil {
		return fail(err)
	}
	notes, err := migrate2020(db)
	if err != nil {
		return fail(err)
	}
	if err := migrateChannels(db); err != nil {
		return fail(err)
	}
	if err := migrateNetbans(db); err != nil {
		return fail(err)
	}
	if err := migrate155b(db); err != nil {
		return fail(err)
	}
	c, err := loadCache(db)
	if err != nil {
		return fail(err)
	}
	return &Store{db: db, c: c, notes: notes}, nil
}

// Notes says what opening the database changed in it (old settings that
// moved), for the server log. Empty most of the time.
func (s *Store) Notes() []string { return s.notes }

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
	a.enforce, a.hide_email, a.hide_host, a.memo_notify, a.memo_limit,
	a.hide_status, a.hide_quit, a.last_quit, a.protect_delay, a.secure`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	var reg, seen int64
	err := row.Scan(&a.ID, &a.Name, &a.PassHash, &a.Email, &reg, &seen, &a.LastHost,
		&a.Enforce, &a.HideEmail, &a.HideHost, &a.MemoNotify, &a.MemoLimit,
		&a.HideStatus, &a.HideQuit, &a.LastQuit, &a.Protect, &a.Secure)
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
	key := irc.Fold(nick)
	if s.c.nicks[key] != nil {
		return nil, ErrExists
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO accounts (name, pass_hash, email, registered, last_seen) VALUES (?, ?, ?, ?, ?)`,
		nick, passHash, email, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO nicks (nick_key, nick, account_id, registered) VALUES (?, ?, ?, ?)`,
		key, nick, id, now.Unix()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrExists
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// The defaults below are the ones in the schema.
	s.c.accounts[id] = &Account{ID: id, Name: nick, PassHash: passHash, Email: email,
		Registered: sec(now), LastSeen: sec(now), Enforce: true, Secure: true, HideEmail: true, HideHost: true,
		MemoNotify: true, MemoLimit: 50}
	s.c.addNick(&nickRow{key: key, nick: nick, account: id, registered: now.Unix()})
	return s.c.account(id), nil
}

// AccountByNick finds the account owning nick, or nil.
func (s *Store) AccountByNick(nick string) (*Account, error) {
	n := s.c.nicks[irc.Fold(nick)]
	if n == nil {
		return nil, nil
	}
	return s.c.account(n.account), nil
}

// AccountByID finds an account by ID, or nil.
func (s *Store) AccountByID(id int64) (*Account, error) { return s.c.account(id), nil }

// SaveAccount writes every changeable field of a back.
func (s *Store) SaveAccount(a *Account) error {
	have := s.c.accounts[a.ID]
	if have == nil {
		return nil // dropped meanwhile; the database has nothing to update either
	}
	_, err := s.db.Exec(`UPDATE accounts SET pass_hash=?, email=?, last_seen=?, last_host=?, enforce=?, hide_email=?,
		hide_host=?, memo_notify=?, memo_limit=?, hide_status=?, hide_quit=?, last_quit=?, protect_delay=?, secure=? WHERE id=?`,
		a.PassHash, a.Email, a.LastSeen.Unix(), a.LastHost, a.Enforce, a.HideEmail, a.HideHost, a.MemoNotify, a.MemoLimit,
		a.HideStatus, a.HideQuit, a.LastQuit, a.Protect, a.Secure, a.ID)
	if err != nil {
		return err
	}
	have.PassHash, have.Email, have.LastSeen, have.LastHost = a.PassHash, a.Email, sec(a.LastSeen), a.LastHost
	have.Enforce, have.HideEmail, have.HideHost = a.Enforce, a.HideEmail, a.HideHost
	have.MemoNotify, have.MemoLimit = a.MemoNotify, a.MemoLimit
	have.HideStatus, have.HideQuit, have.LastQuit, have.Protect = a.HideStatus, a.HideQuit, a.LastQuit, a.Protect
	have.Secure = a.Secure
	return nil
}

// Nicks returns an account's nicks, the account name first.
func (s *Store) Nicks(a *Account) ([]string, error) {
	var rows []*nickRow
	for key := range s.c.acctNicks[a.ID] {
		rows = append(rows, s.c.nicks[key])
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].registered != rows[j].registered {
			return rows[i].registered < rows[j].registered
		}
		return rows[i].key < rows[j].key
	})
	out := []string{a.Name}
	for _, n := range rows {
		if !irc.EqualFold(n.nick, a.Name) {
			out = append(out, n.nick)
		}
	}
	return out, nil
}

// AddNick groups nick into account a.
func (s *Store) AddNick(a *Account, nick string, now time.Time) error {
	key := irc.Fold(nick)
	if s.c.nicks[key] != nil {
		return ErrExists
	}
	_, err := s.db.Exec("INSERT INTO nicks (nick_key, nick, account_id, registered) VALUES (?, ?, ?, ?)",
		key, nick, a.ID, now.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrExists
	}
	if err != nil {
		return err
	}
	s.c.addNick(&nickRow{key: key, nick: nick, account: a.ID, registered: now.Unix()})
	return nil
}

// RemoveNick ungroups nick.
func (s *Store) RemoveNick(nick string) error {
	key := irc.Fold(nick)
	if _, err := s.db.Exec("DELETE FROM nicks WHERE nick_key = ?", key); err != nil {
		return err
	}
	s.c.removeNick(key)
	return nil
}

// NickEntry is one registered nick, for LIST.
type NickEntry struct {
	Nick    string
	Account string
}

// AllNicks returns every registered nick.
func (s *Store) AllNicks() ([]NickEntry, error) {
	keys := make([]string, 0, len(s.c.nicks))
	for k := range s.c.nicks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]NickEntry, 0, len(keys))
	for _, k := range keys {
		n := s.c.nicks[k]
		out = append(out, NickEntry{Nick: n.nick, Account: s.c.accountName(n.account)})
	}
	return out, nil
}

// RenameAccount gives account id a new name, one of its own nicks (NS DROP
// of the account's name, as Anope moves the display nick).
func (s *Store) RenameAccount(id int64, name string) error {
	a := s.c.accounts[id]
	if a == nil {
		return nil
	}
	if _, err := s.db.Exec("UPDATE accounts SET name = ? WHERE id = ?", name, id); err != nil {
		return err
	}
	a.Name = name
	return nil
}

// Access returns the account's NickServ ACCESS list: user@host masks that
// mark a connection as recognised (Anope 2.0.20).
func (s *Store) Access(a *Account) ([]string, error) {
	return append([]string(nil), s.c.access[a.ID]...), nil
}

// AddAccess adds mask; false when it was there already.
func (s *Store) AddAccess(a *Account, mask string) (bool, error) {
	added, err := s.affected("INSERT OR IGNORE INTO access (account_id, mask) VALUES (?, ?)", a.ID, mask)
	if err != nil || !added {
		return added, err
	}
	list := append(s.c.access[a.ID], mask)
	sort.Strings(list)
	s.c.access[a.ID] = list
	return true, nil
}

// DelAccess removes mask; false when it wasn't there.
func (s *Store) DelAccess(a *Account, mask string) (bool, error) {
	gone, err := s.affected("DELETE FROM access WHERE account_id = ? AND mask = ?", a.ID, mask)
	if err != nil || !gone {
		return gone, err
	}
	s.c.access[a.ID] = without(s.c.access[a.ID], func(m string) bool { return m == mask })
	return true, nil
}

// Expired returns accounts not seen since before.
func (s *Store) Expired(before time.Time) ([]*Account, error) {
	var out []*Account
	for id, a := range s.c.accounts {
		if a.LastSeen.Unix() < before.Unix() {
			out = append(out, s.c.account(id))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// AddMemo stores a memo for a.
func (s *Store) AddMemo(a *Account, from, text string, now time.Time) error {
	if s.c.accounts[a.ID] == nil {
		return nil
	}
	res, err := s.db.Exec("INSERT INTO memos (account_id, sender, sent, body) VALUES (?, ?, ?, ?)", a.ID, from, now.Unix(), text)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	s.c.memos[a.ID] = append(s.c.memos[a.ID], Memo{ID: id, From: from, Sent: sec(now), Text: text})
	s.c.memoOwner[id] = a.ID
	return nil
}

// Memos returns a's memos, oldest first. Their position (1, 2, ...) is the
// number people type.
func (s *Store) Memos(a *Account) ([]Memo, error) {
	return append([]Memo(nil), s.c.memos[a.ID]...), nil
}

// MarkRead marks one memo read.
func (s *Store) MarkRead(id int64) error {
	if _, err := s.db.Exec("UPDATE memos SET seen = 1 WHERE id = ?", id); err != nil {
		return err
	}
	list := s.c.memos[s.c.memoOwner[id]]
	for i := range list {
		if list[i].ID == id {
			list[i].Read = true
		}
	}
	return nil
}

// DeleteMemo deletes one memo.
func (s *Store) DeleteMemo(id int64) error {
	if _, err := s.db.Exec("DELETE FROM memos WHERE id = ?", id); err != nil {
		return err
	}
	owner, ok := s.c.memoOwner[id]
	if !ok {
		return nil
	}
	delete(s.c.memoOwner, id)
	s.c.memos[owner] = without(s.c.memos[owner], func(m Memo) bool { return m.ID == id })
	return nil
}

// MemoIgnores returns the nicks whose memos a drops.
func (s *Store) MemoIgnores(a *Account) ([]string, error) {
	var out []string
	for _, g := range s.c.ignores[a.ID] {
		out = append(out, g.nick)
	}
	return out, nil
}

// AddMemoIgnore adds nick; false when it was there.
func (s *Store) AddMemoIgnore(a *Account, nick string) (bool, error) {
	key := irc.Fold(nick)
	added, err := s.affected("INSERT OR IGNORE INTO memo_ignores (account_id, nick_key, nick) VALUES (?, ?, ?)", a.ID, key, nick)
	if err != nil || !added {
		return added, err
	}
	list := append(s.c.ignores[a.ID], ignoreRow{key: key, nick: nick})
	sort.Slice(list, func(i, j int) bool { return list[i].key < list[j].key })
	s.c.ignores[a.ID] = list
	return true, nil
}

// DelMemoIgnore removes nick; false when it wasn't there.
func (s *Store) DelMemoIgnore(a *Account, nick string) (bool, error) {
	key := irc.Fold(nick)
	gone, err := s.affected("DELETE FROM memo_ignores WHERE account_id = ? AND nick_key = ?", a.ID, key)
	if err != nil || !gone {
		return gone, err
	}
	s.c.ignores[a.ID] = without(s.c.ignores[a.ID], func(g ignoreRow) bool { return g.key == key })
	return true, nil
}

// Ignores reports whether a drops memos from nick.
func (s *Store) Ignores(a *Account, nick string) (bool, error) {
	key := irc.Fold(nick)
	for _, g := range s.c.ignores[a.ID] {
		if g.key == key {
			return true, nil
		}
	}
	return false, nil
}

// Counts returns the number of accounts, nicks and memos, for status.
func (s *Store) Counts() (accounts, nicks, memos int, err error) {
	return len(s.c.accounts), len(s.c.nicks), len(s.c.memoOwner), nil
}

// without returns list minus the items drop matches, reusing no memory
// callers might still hold.
func without[T any](list []T, drop func(T) bool) []T {
	var out []T
	for _, v := range list {
		if !drop(v) {
			out = append(out, v)
		}
	}
	return out
}

// migrateAccounts adds the columns of Stage 1.5.3 to older databases.
func migrateAccounts(db *sql.DB) error {
	for _, col := range []struct{ table, name, def string }{
		{"accounts", "hide_status", "INTEGER NOT NULL DEFAULT 0"},
		{"accounts", "hide_quit", "INTEGER NOT NULL DEFAULT 0"},
		{"accounts", "last_quit", "TEXT NOT NULL DEFAULT ''"},
		{"accounts", "protect_delay", "INTEGER NOT NULL DEFAULT 0"},
		{"accounts", "secure", "INTEGER NOT NULL DEFAULT 1"},
		{"channels", "cs_secure", "INTEGER NOT NULL DEFAULT 1"},
		{"chan_access", "note", "TEXT NOT NULL DEFAULT ''"},
	} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", col.table, col.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE " + col.table + " ADD COLUMN " + col.name + " " + col.def); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrate2020 moves settings to their Anope 2.0.20 shape (Stage 1.5.5,
// "services follow Anope 2.0.20"):
//   - SET PROTECT delays become SET KILL modes: 0 stays ON, anything up to
//     30 s becomes QUICK (20 s), longer becomes ON.
//   - The UNBANME privilege is gone (2.0.20 has only UNBAN); channels that
//     had changed its level lose that setting, and the notes name them.
func migrate2020(db *sql.DB) ([]string, error) {
	if _, err := db.Exec(`UPDATE accounts SET protect_delay = CASE
		WHEN protect_delay > 0 AND protect_delay <= 30 THEN 20 ELSE 0 END
		WHERE protect_delay NOT IN (0, 20, -1)`); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT c.name, l.level FROM chan_levels l JOIN channels c ON c.id = l.channel_id
		WHERE l.priv = 'UNBANME' ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	var notes []string
	for rows.Next() {
		var name string
		var lvl int
		if err := rows.Scan(&name, &lvl); err != nil {
			rows.Close()
			return nil, err
		}
		notes = append(notes, fmt.Sprintf("%s: LEVELS UNBANME %d removed (Anope 2.0.20 has only UNBAN)", name, lvl))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := db.Exec("DELETE FROM chan_levels WHERE priv = 'UNBANME'"); err != nil {
		return nil, err
	}
	return notes, nil
}

// SplitNick moves nick out of its account into a new account of its own,
// with the same password and email (Anope's UNGROUP). The old account must
// keep at least one nick; the caller renames it when nick was its name.
func (s *Store) SplitNick(nick string, now time.Time) (*Account, error) {
	key := irc.Fold(nick)
	n := s.c.nicks[key]
	if n == nil {
		return nil, nil
	}
	old := s.c.accounts[n.account]
	if old == nil {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO accounts (name, pass_hash, email, registered, last_seen) VALUES (?, ?, ?, ?, ?)`,
		n.nick, old.PassHash, old.Email, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec("UPDATE nicks SET account_id = ? WHERE nick_key = ?", id, key); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// The defaults below are the ones in the schema.
	s.c.accounts[id] = &Account{ID: id, Name: n.nick, PassHash: old.PassHash, Email: old.Email,
		Registered: sec(now), LastSeen: sec(now), Enforce: true, Secure: true, HideEmail: true, HideHost: true,
		MemoNotify: true, MemoLimit: 50}
	row := *n
	s.c.removeNick(key)
	row.account = id
	s.c.addNick(&row)
	return s.c.account(id), nil
}
