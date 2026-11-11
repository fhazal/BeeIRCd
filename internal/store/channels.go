package store

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Channel access levels, spaced out so new ones fit between. The founder
// isn't stored in the access table. HOP and QOP are used on servers with
// ~&@%+ prefixes.
const (
	LevelNone    = 0
	LevelUOP     = 10
	LevelVOP     = 20
	LevelHOP     = 25
	LevelAOP     = 30
	LevelSOP     = 40
	LevelQOP     = 45
	LevelFounder = 50
)

// LevelName is "SOP", "AOP" and so on.
func LevelName(l int) string {
	switch l {
	case LevelUOP:
		return "UOP"
	case LevelVOP:
		return "VOP"
	case LevelHOP:
		return "HOP"
	case LevelAOP:
		return "AOP"
	case LevelSOP:
		return "SOP"
	case LevelQOP:
		return "QOP"
	case LevelFounder:
		return "Founder"
	}
	return "none"
}

// migrateLevels moves access levels from the first numbering (1 to 5) to
// the spaced one (10 to 50), once.
func migrateLevels(db *sql.DB) error {
	var v string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema'").Scan(&v); err != nil {
		return err
	}
	if v != "1" {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE chan_access SET level = level * 10 WHERE level BETWEEN 1 AND 5"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE meta SET value = '2' WHERE key = 'schema'"); err != nil {
		return err
	}
	return tx.Commit()
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
	Note      string // the optional description given with ADD
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
	note       TEXT NOT NULL DEFAULT '',
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
	key := irc.Fold(name)
	if _, ok := s.c.chanKeys[key]; ok {
		return nil, ErrExists
	}
	res, err := s.db.Exec(`INSERT INTO channels (name, name_key, founder_id, description, registered, last_used) VALUES (?, ?, ?, ?, ?, ?)`,
		name, key, founder.ID, desc, now.Unix(), now.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	// The defaults below are the ones in the schema.
	s.c.channels[id] = &Channel{ID: id, Name: name, FounderID: founder.ID, Description: desc,
		Registered: sec(now), LastUsed: sec(now), MlockOn: "nt", KeepTopic: true, TopicAt: time.Unix(0, 0)}
	s.c.chanKeys[key] = id
	return s.c.channel(id), nil
}

// ChannelByName finds a registered channel, or nil.
func (s *Store) ChannelByName(name string) (*Channel, error) {
	id, ok := s.c.chanKeys[irc.Fold(name)]
	if !ok {
		return nil, nil
	}
	return s.c.channel(id), nil
}

// Channels returns every registered channel by name.
func (s *Store) Channels() ([]*Channel, error) {
	return s.channelsWhere(func(*Channel) bool { return true }), nil
}

// ExpiredChannels returns channels unused since before.
func (s *Store) ExpiredChannels(before time.Time) ([]*Channel, error) {
	return s.channelsWhere(func(c *Channel) bool { return c.LastUsed.Unix() < before.Unix() }), nil
}

func (s *Store) channelsWhere(keep func(*Channel) bool) []*Channel {
	var out []*Channel
	for id, c := range s.c.channels {
		if keep(c) {
			out = append(out, s.c.channel(id))
		}
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Name) < irc.Fold(out[j].Name) })
	return out
}

// SaveChannel writes every setting of c back.
func (s *Store) SaveChannel(c *Channel) error {
	have := s.c.channels[c.ID]
	if have == nil {
		return nil // dropped meanwhile
	}
	var succ any
	if c.SuccessorID != 0 {
		succ = c.SuccessorID
	}
	_, err := s.db.Exec(`UPDATE channels SET founder_id=?, successor_id=?, description=?, last_used=?, mlock_on=?, mlock_off=?,
		mlock_key=?, mlock_limit=?, keeptopic=?, topiclock=?, secure=?, private=?, entrymsg=?, url=?, topic=?, topic_by=?, topic_at=?
		WHERE id=?`,
		c.FounderID, succ, c.Description, c.LastUsed.Unix(), c.MlockOn, c.MlockOff, c.MlockKey, c.MlockLimit,
		c.KeepTopic, c.TopicLock, c.Secure, c.Private, c.EntryMsg, c.URL, c.Topic, c.TopicBy, c.TopicAt.Unix(), c.ID)
	if err != nil {
		return err
	}
	cp := *c
	cp.Name, cp.Registered = have.Name, have.Registered
	cp.Founder, cp.Successor = "", ""
	cp.LastUsed, cp.TopicAt = sec(c.LastUsed), sec(c.TopicAt)
	*have = cp
	return nil
}

// DeleteChannel unregisters a channel with its access list and AKICKs.
func (s *Store) DeleteChannel(id int64) error {
	if _, err := s.db.Exec("DELETE FROM channels WHERE id = ?", id); err != nil {
		return err
	}
	s.c.dropChannel(id)
	return nil
}

// Level is account's level on channel (Founder included).
func (s *Store) Level(c *Channel, accountID int64) (int, error) {
	if accountID == 0 {
		return LevelNone, nil
	}
	if accountID == c.FounderID {
		return LevelFounder, nil
	}
	if l := s.c.levels[c.ID][accountID]; l != nil {
		return l.level, nil
	}
	return LevelNone, nil
}

// SetLevel puts account on the access list at level, with an optional note,
// and returns the level it had before (LevelNone when new).
func (s *Store) SetLevel(c *Channel, a *Account, lvl int, by, note string, now time.Time) (int, error) {
	old, _ := s.Level(c, a.ID)
	if s.c.channels[c.ID] == nil || s.c.accounts[a.ID] == nil {
		return old, nil
	}
	_, err := s.db.Exec(`INSERT INTO chan_access (channel_id, account_id, level, added_by, added_at, note) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (channel_id, account_id) DO UPDATE SET level = excluded.level, added_by = excluded.added_by,
		added_at = excluded.added_at, note = excluded.note`,
		c.ID, a.ID, lvl, by, now.Unix(), note)
	if err != nil {
		return old, err
	}
	s.c.setLevel(c.ID, a.ID, &level{level: lvl, addedBy: by, addedAt: sec(now), note: note})
	return old, nil
}

// RemoveAccess takes account off the access list; false when it wasn't on it.
func (s *Store) RemoveAccess(c *Channel, accountID int64) (bool, error) {
	gone, err := s.affected("DELETE FROM chan_access WHERE channel_id = ? AND account_id = ?", c.ID, accountID)
	if err != nil || !gone {
		return gone, err
	}
	delete(s.c.levels[c.ID], accountID)
	return true, nil
}

// ClearLevel empties one level's list and returns how many were removed.
func (s *Store) ClearLevel(c *Channel, lvl int) (int, error) {
	if _, err := s.db.Exec("DELETE FROM chan_access WHERE channel_id = ? AND level = ?", c.ID, lvl); err != nil {
		return 0, err
	}
	n := 0
	for id, l := range s.c.levels[c.ID] {
		if l.level == lvl {
			delete(s.c.levels[c.ID], id)
			n++
		}
	}
	return n, nil
}

// AccessList returns the access list, highest level first. level 0 means
// every level.
func (s *Store) AccessList(c *Channel, lvl int) ([]AccessEntry, error) {
	var out []AccessEntry
	for id, l := range s.c.levels[c.ID] {
		if lvl != 0 && l.level != lvl {
			continue
		}
		out = append(out, AccessEntry{AccountID: id, Account: s.c.accountName(id), Level: l.level, AddedBy: l.addedBy, AddedAt: l.addedAt, Note: l.note})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		if !a.AddedAt.Equal(b.AddedAt) {
			return a.AddedAt.Before(b.AddedAt)
		}
		return a.Account < b.Account
	})
	return out, nil
}

// Akicks returns a channel's AKICK list, oldest first.
func (s *Store) Akicks(c *Channel) ([]Akick, error) {
	return append([]Akick(nil), s.c.akicks[c.ID]...), nil
}

// AddAkick adds mask; false when it was there.
func (s *Store) AddAkick(c *Channel, mask, reason, by string, now time.Time) (bool, error) {
	if s.c.channels[c.ID] == nil {
		return false, nil
	}
	added, err := s.affected("INSERT OR IGNORE INTO chan_akick (channel_id, mask, reason, added_by, added_at) VALUES (?, ?, ?, ?, ?)",
		c.ID, mask, reason, by, now.Unix())
	if err != nil || !added {
		return added, err
	}
	list := append(append([]Akick(nil), s.c.akicks[c.ID]...), Akick{Mask: mask, Reason: reason, AddedBy: by, AddedAt: sec(now)})
	sortAkicks(list)
	s.c.akicks[c.ID] = list
	return true, nil
}

// DelAkick removes mask; false when it wasn't there.
func (s *Store) DelAkick(c *Channel, mask string) (bool, error) {
	gone, err := s.affected("DELETE FROM chan_akick WHERE channel_id = ? AND mask = ?", c.ID, mask)
	if err != nil || !gone {
		return gone, err
	}
	s.c.akicks[c.ID] = without(s.c.akicks[c.ID], func(k Akick) bool { return k.Mask == mask })
	return true, nil
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
	type owned struct {
		id, succ int64
		name     string
	}
	var list []owned
	for cid, c := range s.c.channels {
		if c.FounderID == id {
			list = append(list, owned{cid, c.SuccessorID, c.Name})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].id < list[j].id })
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var changes []FounderChange
	for _, o := range list {
		if o.succ != 0 && o.succ != id {
			if _, err := tx.Exec("UPDATE channels SET founder_id = ?, successor_id = NULL WHERE id = ?", o.succ, o.id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec("DELETE FROM chan_access WHERE channel_id = ? AND account_id = ?", o.id, o.succ); err != nil {
				return nil, err
			}
			changes = append(changes, FounderChange{Channel: o.name, NewOwner: s.c.accountName(o.succ)})
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
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, o := range list {
		if o.succ != 0 && o.succ != id {
			c := s.c.channels[o.id]
			c.FounderID, c.SuccessorID = o.succ, 0
			delete(s.c.levels[o.id], o.succ)
		} else {
			s.c.dropChannel(o.id)
		}
	}
	s.c.dropAccount(id)
	return changes, nil
}

// ChannelCount is the number of registered channels.
func (s *Store) ChannelCount() (int, error) { return len(s.c.channels), nil }
