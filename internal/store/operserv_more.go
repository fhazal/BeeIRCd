package store

import (
	"database/sql"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// News kinds, oper info, the services ignore list and SEEN
// data (Anope 2.0.20's os_news, os_info, os_ignore and cs_seen).

// News kinds (OperServ LOGONNEWS, OPERNEWS, RANDOMNEWS).
const (
	NewsLogon  = "logon"
	NewsOper   = "oper"
	NewsRandom = "random"
)

// OperInfo is one oper note on an account or a channel (OperServ INFO).
type OperInfo struct {
	ID      int64
	Text    string
	AddedBy string
	AddedAt time.Time
}

// Ignore is one services ignore (OperServ IGNORE): services drop what a
// matching non-oper says to them.
type Ignore struct {
	Mask    string // nick!user@host
	Reason  string
	SetBy   string
	SetAt   time.Time
	Expires time.Time // Unix 0 = for good
}

// Seen kinds, as Anope's cs_seen.
const (
	SeenConnect  = "connect"
	SeenNickTo   = "nick_to"   // changed nick to Other
	SeenNickFrom = "nick_from" // got this nick from Other
	SeenJoin     = "join"
	SeenPart     = "part"
	SeenQuit     = "quit"
	SeenKick     = "kick"
)

// Seen is the last thing a nick was seen doing (ChanServ SEEN).
type Seen struct {
	Nick    string
	Mask    string // ident@shown host
	Kind    string
	Other   string // the other nick for nick changes
	Channel string
	Message string // quit or kick reason
	Last    time.Time
}

const operMoreSchema = `
CREATE TABLE IF NOT EXISTS oper_info (
	id         INTEGER PRIMARY KEY,
	account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
	channel_id INTEGER REFERENCES channels(id) ON DELETE CASCADE,
	body       TEXT NOT NULL,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS oper_info_account ON oper_info(account_id);
CREATE INDEX IF NOT EXISTS oper_info_channel ON oper_info(channel_id);
CREATE TABLE IF NOT EXISTS svc_ignores (
	mask_key TEXT PRIMARY KEY,
	mask     TEXT NOT NULL,
	reason   TEXT NOT NULL DEFAULT '',
	set_by   TEXT NOT NULL,
	set_at   INTEGER NOT NULL,
	expires  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS seen (
	nick_key TEXT PRIMARY KEY,
	nick     TEXT NOT NULL,
	mask     TEXT NOT NULL,
	kind     TEXT NOT NULL,
	other    TEXT NOT NULL DEFAULT '',
	channel  TEXT NOT NULL DEFAULT '',
	message  TEXT NOT NULL DEFAULT '',
	last     INTEGER NOT NULL
);
`

func migrateOperMore(db *sql.DB) error {
	if _, err := db.Exec(operMoreSchema); err != nil {
		return err
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('news') WHERE name = 'kind'").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec("ALTER TABLE news ADD COLUMN kind TEXT NOT NULL DEFAULT 'logon'"); err != nil {
			return err
		}
	}
	return nil
}

// --- oper info ---

func infoCol(channel bool) string {
	if channel {
		return "channel_id"
	}
	return "account_id"
}

// OperInfos returns the oper notes on an account (channel false) or a
// channel, oldest first.
func (s *Store) OperInfos(channel bool, id int64) ([]OperInfo, error) {
	rows, err := s.db.Query("SELECT id, body, added_by, added_at FROM oper_info WHERE "+infoCol(channel)+" = ? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperInfo
	for rows.Next() {
		var o OperInfo
		var at int64
		if err := rows.Scan(&o.ID, &o.Text, &o.AddedBy, &at); err != nil {
			return nil, err
		}
		o.AddedAt = time.Unix(at, 0)
		out = append(out, o)
	}
	return out, rows.Err()
}

// AddOperInfo adds an oper note.
func (s *Store) AddOperInfo(channel bool, id int64, text, by string, now time.Time) error {
	_, err := s.db.Exec("INSERT INTO oper_info ("+infoCol(channel)+", body, added_by, added_at) VALUES (?, ?, ?, ?)", id, text, by, now.Unix())
	return err
}

// DelOperInfo removes the note with this ID.
func (s *Store) DelOperInfo(noteID int64) (bool, error) {
	return s.affected("DELETE FROM oper_info WHERE id = ?", noteID)
}

// ClearOperInfo removes every note on an account or channel.
func (s *Store) ClearOperInfo(channel bool, id int64) (bool, error) {
	return s.affected("DELETE FROM oper_info WHERE "+infoCol(channel)+" = ?", id)
}

// --- services ignores ---

// SvcIgnores returns the services ignore list, by mask.
func (s *Store) SvcIgnores() ([]Ignore, error) {
	rows, err := s.db.Query("SELECT mask, reason, set_by, set_at, expires FROM svc_ignores ORDER BY mask_key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ignore
	for rows.Next() {
		var g Ignore
		var at, exp int64
		if err := rows.Scan(&g.Mask, &g.Reason, &g.SetBy, &at, &exp); err != nil {
			return nil, err
		}
		g.SetAt, g.Expires = time.Unix(at, 0), time.Unix(exp, 0)
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetSvcIgnore adds an ignore, or replaces the one on the same mask.
func (s *Store) SetSvcIgnore(g Ignore) error {
	exp := int64(0)
	if !g.Expires.IsZero() {
		exp = g.Expires.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO svc_ignores (mask_key, mask, reason, set_by, set_at, expires) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(mask_key) DO UPDATE SET mask=excluded.mask, reason=excluded.reason, set_by=excluded.set_by,
		set_at=excluded.set_at, expires=excluded.expires`, irc.Fold(g.Mask), g.Mask, g.Reason, g.SetBy, g.SetAt.Unix(), exp)
	return err
}

// DelSvcIgnore removes the ignore on mask.
func (s *Store) DelSvcIgnore(mask string) (bool, error) {
	return s.affected("DELETE FROM svc_ignores WHERE mask_key = ?", irc.Fold(mask))
}

// ClearSvcIgnores empties the list.
func (s *Store) ClearSvcIgnores() error {
	_, err := s.db.Exec("DELETE FROM svc_ignores")
	return err
}

// --- seen ---

// AllSeen returns the SEEN data.
func (s *Store) AllSeen() ([]Seen, error) {
	rows, err := s.db.Query("SELECT nick, mask, kind, other, channel, message, last FROM seen")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Seen
	for rows.Next() {
		var e Seen
		var last int64
		if err := rows.Scan(&e.Nick, &e.Mask, &e.Kind, &e.Other, &e.Channel, &e.Message, &last); err != nil {
			return nil, err
		}
		e.Last = time.Unix(last, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SaveSeen writes changed SEEN entries and removes gone ones in one
// transaction. Services keep SEEN in memory and call this now and then:
// it changes on every join, part and nick change, too often to write each
// one on the server loop.
func (s *Store) SaveSeen(changed []Seen, gone []string) error {
	if len(changed) == 0 && len(gone) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	up, err := tx.Prepare(`INSERT INTO seen (nick_key, nick, mask, kind, other, channel, message, last) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(nick_key) DO UPDATE SET nick=excluded.nick, mask=excluded.mask, kind=excluded.kind, other=excluded.other,
		channel=excluded.channel, message=excluded.message, last=excluded.last`)
	if err != nil {
		return err
	}
	defer up.Close()
	for _, e := range changed {
		if _, err := up.Exec(irc.Fold(e.Nick), e.Nick, e.Mask, e.Kind, e.Other, e.Channel, e.Message, e.Last.Unix()); err != nil {
			return err
		}
	}
	for _, key := range gone {
		if _, err := tx.Exec("DELETE FROM seen WHERE nick_key = ?", key); err != nil {
			return err
		}
	}
	return tx.Commit()
}
