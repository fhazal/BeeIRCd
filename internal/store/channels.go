package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Channel access levels. The founder isn't stored in the access table.
const (
	LevelNone    = 0
	LevelUOP     = 1
	LevelVOP     = 2
	LevelAOP     = 3
	LevelSOP     = 4
	LevelFounder = 5
)

// LevelName is "SOP", "AOP" and so on.
func LevelName(l int) string {
	return [...]string{"none", "UOP", "VOP", "AOP", "SOP", "Founder"}[l]
}

// Channel is a ChanServ-registered channel.
type Channel struct {
	ID          int64
	Name        string
	FounderID   int64
	Founder     string // account name
	SuccessorID int64  // 0 = none
	Successor   string
	Description string
	Registered  time.Time
	LastUsed    time.Time
	MlockOn     string // flag modes kept on, like "nt"
	MlockOff    string // flag modes kept off
	MlockKey    string
	MlockLimit  int
	KeepTopic   bool
	TopicLock   bool
	Secure      bool
	Private     bool
	EntryMsg    string
	URL         string
	Topic       string
	TopicBy     string
	TopicAt     time.Time
}

// AccessEntry is one person on a channel's access list.
type AccessEntry struct {
	AccountID int64
	Account   string
	Level     int
	AddedBy   string
	AddedAt   time.Time
}

// Akick is one AKICK mask.
type Akick struct {
	Mask    string
	Reason  string
	AddedBy string
	AddedAt time.Time
}

const channelSchema = `
CREATE TABLE IF NOT EXISTS channels (
	id           INTEGER PRIMARY KEY,
	name         TEXT NOT NULL,
	name_key     TEXT NOT NULL UNIQUE,
	founder_id   INTEGER NOT NULL REFERENCES accounts(id),
	successor_id INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
	description  TEXT NOT NULL DEFAULT '',
	registered   INTEGER NOT NULL,
	last_used    INTEGER NOT NULL,
	mlock_on     TEXT NOT NULL DEFAULT 'nt',
	mlock_off    TEXT NOT NULL DEFAULT '',
	mlock_key    TEXT NOT NULL DEFAULT '',
	mlock_limit  INTEGER NOT NULL DEFAULT 0,
	keeptopic    INTEGER NOT NULL DEFAULT 1,
	topiclock    INTEGER NOT NULL DEFAULT 0,
	secure       INTEGER NOT NULL DEFAULT 0,
	private      INTEGER NOT NULL DEFAULT 0,
	entrymsg     TEXT NOT NULL DEFAULT '',
	url          TEXT NOT NULL DEFAULT '',
	topic        TEXT NOT NULL DEFAULT '',
	topic_by     TEXT NOT NULL DEFAULT '',
	topic_at     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chan_access (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	level      INTEGER NOT NULL,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL,
	PRIMARY KEY (channel_id, account_id)
);
CREATE TABLE IF NOT EXISTS chan_akick (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	mask       TEXT NOT NULL,
	reason     TEXT NOT NULL,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL,
	PRIMARY KEY (channel_id, mask)
);
`

const channelCols = `c.id, c.name, c.founder_id, f.name, COALESCE(c.successor_id, 0), COALESCE(s.name, ''),
	c.description, c.registered, c.last_used, c.mlock_on, c.mlock_off, c.mlock_key, c.mlock_limit,
	c.keeptopic, c.topiclock, c.secure, c.private, c.entrymsg, c.url, c.topic, c.topic_by, c.topic_at`

const channelFrom = ` FROM channels c JOIN accounts f ON f.id = c.founder_id LEFT JOIN accounts s ON s.id = c.successor_id`

func scanChannel(row interface{ Scan(...any) error }) (*Channel, error) {
	var c Channel
	var reg, used, topicAt int64
	err := row.Scan(&c.ID, &c.Name, &c.FounderID, &c.Founder, &c.SuccessorID, &c.Successor,
		&c.Description, &reg, &used, &c.MlockOn, &c.MlockOff, &c.MlockKey, &c.MlockLimit,
		&c.KeepTopic, &c.TopicLock, &c.Secure, &c.Private, &c.EntryMsg, &c.URL, &c.Topic, &c.TopicBy, &topicAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Registered, c.LastUsed, c.TopicAt = time.Unix(reg, 0), time.Unix(used, 0), time.Unix(topicAt, 0)
	return &c, nil
}

// CreateChannel registers name to founder.
func (s *Store) CreateChannel(name string, founder *Account, desc string, now time.Time) (*Channel, error) {
	_, err := s.db.Exec(`INSERT INTO channels (name, name_key, founder_id, description, registered, last_used) VALUES (?, ?, ?, ?, ?, ?)`,
		name, irc.Fold(name), founder.ID, desc, now.Unix(), now.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return s.ChannelByName(name)
}

// ChannelByName finds a registered channel, or nil.
func (s *Store) ChannelByName(name string) (*Channel, error) {
	return scanChannel(s.db.QueryRow(`SELECT `+channelCols+channelFrom+` WHERE c.name_key = ?`, irc.Fold(name)))
}

// Channels returns every registered channel by name.
func (s *Store) Channels() ([]*Channel, error) {
	return s.channels(`SELECT ` + channelCols + channelFrom + ` ORDER BY c.name_key`)
}

// ExpiredChannels returns channels unused since before.
func (s *Store) ExpiredChannels(before time.Time) ([]*Channel, error) {
	return s.channels(`SELECT `+channelCols+channelFrom+` WHERE c.last_used < ?`, before.Unix())
}

func (s *Store) channels(q string, args ...any) ([]*Channel, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveChannel writes every setting of c back.
func (s *Store) SaveChannel(c *Channel) error {
	var succ any
	if c.SuccessorID != 0 {
		succ = c.SuccessorID
	}
	_, err := s.db.Exec(`UPDATE channels SET founder_id=?, successor_id=?, description=?, last_used=?, mlock_on=?, mlock_off=?,
		mlock_key=?, mlock_limit=?, keeptopic=?, topiclock=?, secure=?, private=?, entrymsg=?, url=?, topic=?, topic_by=?, topic_at=?
		WHERE id=?`,
		c.FounderID, succ, c.Description, c.LastUsed.Unix(), c.MlockOn, c.MlockOff, c.MlockKey, c.MlockLimit,
		c.KeepTopic, c.TopicLock, c.Secure, c.Private, c.EntryMsg, c.URL, c.Topic, c.TopicBy, c.TopicAt.Unix(), c.ID)
	return err
}

// DeleteChannel unregisters a channel with its access list and AKICKs.
func (s *Store) DeleteChannel(id int64) error {
	_, err := s.db.Exec("DELETE FROM channels WHERE id = ?", id)
	return err
}

// Level is account's level on channel (Founder included).
func (s *Store) Level(c *Channel, accountID int64) (int, error) {
	if accountID == 0 {
		return LevelNone, nil
	}
	if accountID == c.FounderID {
		return LevelFounder, nil
	}
	var l int
	err := s.db.QueryRow("SELECT level FROM chan_access WHERE channel_id = ? AND account_id = ?", c.ID, accountID).Scan(&l)
	if errors.Is(err, sql.ErrNoRows) {
		return LevelNone, nil
	}
	return l, err
}

// SetLevel puts account on the access list at level and returns the level
// it had before (LevelNone when new).
func (s *Store) SetLevel(c *Channel, a *Account, level int, by string, now time.Time) (int, error) {
	old, err := s.Level(c, a.ID)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(`INSERT INTO chan_access (channel_id, account_id, level, added_by, added_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (channel_id, account_id) DO UPDATE SET level = excluded.level, added_by = excluded.added_by, added_at = excluded.added_at`,
		c.ID, a.ID, level, by, now.Unix())
	return old, err
}

// RemoveAccess takes account off the access list; false when it wasn't on it.
func (s *Store) RemoveAccess(c *Channel, accountID int64) (bool, error) {
	res, err := s.db.Exec("DELETE FROM chan_access WHERE channel_id = ? AND account_id = ?", c.ID, accountID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClearLevel empties one level's list and returns how many were removed.
func (s *Store) ClearLevel(c *Channel, level int) (int, error) {
	res, err := s.db.Exec("DELETE FROM chan_access WHERE channel_id = ? AND level = ?", c.ID, level)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AccessList returns the access list, highest level first. level 0 means
// every level.
func (s *Store) AccessList(c *Channel, level int) ([]AccessEntry, error) {
	q := `SELECT a.id, a.name, x.level, x.added_by, x.added_at FROM chan_access x JOIN accounts a ON a.id = x.account_id
		WHERE x.channel_id = ?`
	args := []any{c.ID}
	if level != 0 {
		q += " AND x.level = ?"
		args = append(args, level)
	}
	rows, err := s.db.Query(q+" ORDER BY x.level DESC, x.added_at, a.name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessEntry
	for rows.Next() {
		var e AccessEntry
		var at int64
		if err := rows.Scan(&e.AccountID, &e.Account, &e.Level, &e.AddedBy, &at); err != nil {
			return nil, err
		}
		e.AddedAt = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Akicks returns a channel's AKICK list, oldest first.
func (s *Store) Akicks(c *Channel) ([]Akick, error) {
	rows, err := s.db.Query("SELECT mask, reason, added_by, added_at FROM chan_akick WHERE channel_id = ? ORDER BY added_at, mask", c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Akick
	for rows.Next() {
		var k Akick
		var at int64
		if err := rows.Scan(&k.Mask, &k.Reason, &k.AddedBy, &at); err != nil {
			return nil, err
		}
		k.AddedAt = time.Unix(at, 0)
		out = append(out, k)
	}
	return out, rows.Err()
}

// AddAkick adds mask; false when it was there.
func (s *Store) AddAkick(c *Channel, mask, reason, by string, now time.Time) (bool, error) {
	res, err := s.db.Exec("INSERT OR IGNORE INTO chan_akick (channel_id, mask, reason, added_by, added_at) VALUES (?, ?, ?, ?, ?)",
		c.ID, mask, reason, by, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DelAkick removes mask; false when it wasn't there.
func (s *Store) DelAkick(c *Channel, mask string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM chan_akick WHERE channel_id = ? AND mask = ?", c.ID, mask)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FounderChange says what happened to a channel when its founder's account
// went away.
type FounderChange struct {
	Channel  string
	NewOwner string // "" when the channel was dropped
}

// DeleteAccount removes an account with its nicks, access lists and memos.
// Channels it founded go to their successor, or are dropped without one.
func (s *Store) DeleteAccount(id int64) ([]FounderChange, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT c.id, c.name, COALESCE(c.successor_id, 0), COALESCE(a.name, '') FROM channels c
		LEFT JOIN accounts a ON a.id = c.successor_id WHERE c.founder_id = ?`, id)
	if err != nil {
		return nil, err
	}
	type owned struct {
		id, succ int64
		name     string
		succName string
	}
	var list []owned
	for rows.Next() {
		var o owned
		if err := rows.Scan(&o.id, &o.name, &o.succ, &o.succName); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, o)
	}
	rows.Close()
	var changes []FounderChange
	for _, o := range list {
		if o.succ != 0 && o.succ != id {
			if _, err := tx.Exec("UPDATE channels SET founder_id = ?, successor_id = NULL WHERE id = ?", o.succ, o.id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec("DELETE FROM chan_access WHERE channel_id = ? AND account_id = ?", o.id, o.succ); err != nil {
				return nil, err
			}
			changes = append(changes, FounderChange{Channel: o.name, NewOwner: o.succName})
			continue
		}
		if _, err := tx.Exec("DELETE FROM channels WHERE id = ?", o.id); err != nil {
			return nil, err
		}
		changes = append(changes, FounderChange{Channel: o.name})
	}
	if _, err := tx.Exec("DELETE FROM accounts WHERE id = ?", id); err != nil {
		return nil, err
	}
	return changes, tx.Commit()
}

// ChannelCount is the number of registered channels.
func (s *Store) ChannelCount() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM channels").Scan(&n)
	return n, err
}
