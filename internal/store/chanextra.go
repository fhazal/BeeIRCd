package store

import (
	"database/sql"
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Channel data: per-channel LEVELS, entry messages,
// LOG settings and the channel memo box.

// DefaultChanMemoLimit is how many memos a channel box keeps at first.
const DefaultChanMemoLimit = 20

// EntryMsg is one line of a channel's entry message.
type EntryMsg struct {
	ID      int64
	Text    string
	AddedBy string
	AddedAt time.Time
}

// LogSetting is one ChanServ LOG entry: when command is used on the
// channel, tell it by method (MESSAGE, NOTICE or MEMO), to those with
// status (like "@", "" = everyone).
type LogSetting struct {
	Command string
	Method  string
	Status  string
}

const chanExtraSchema = `
CREATE TABLE IF NOT EXISTS chan_levels (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	priv       TEXT NOT NULL,
	level      INTEGER NOT NULL,
	PRIMARY KEY (channel_id, priv)
);
CREATE TABLE IF NOT EXISTS chan_entrymsg (
	id         INTEGER PRIMARY KEY,
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	text       TEXT NOT NULL,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chan_log (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	command    TEXT NOT NULL,
	method     TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (channel_id, command, method)
);
CREATE TABLE IF NOT EXISTS chan_memos (
	id         INTEGER PRIMARY KEY,
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	sender     TEXT NOT NULL,
	sent       INTEGER NOT NULL,
	body       TEXT NOT NULL,
	seen       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chan_memo_ignores (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	nick_key   TEXT NOT NULL,
	nick       TEXT NOT NULL,
	PRIMARY KEY (channel_id, nick_key)
);
`

// migrateChannels adds the newer channel columns and moves the old one-line
// entry message into chan_entrymsg. Both are safe to run on every start.
func migrateChannels(db *sql.DB) error {
	cols := []struct{ name, def string }{
		{"restricted", "INTEGER NOT NULL DEFAULT 0"},
		{"peace", "INTEGER NOT NULL DEFAULT 1"},
		{"securefounder", "INTEGER NOT NULL DEFAULT 1"},
		{"signkick", "INTEGER NOT NULL DEFAULT 1"},
		{"keepmodes", "INTEGER NOT NULL DEFAULT 0"},
		{"persist", "INTEGER NOT NULL DEFAULT 0"},
		{"autoop", "INTEGER NOT NULL DEFAULT 1"},
		{"noexpire", "INTEGER NOT NULL DEFAULT 0"},
		{"bantype", "INTEGER NOT NULL DEFAULT 2"},
		{"last_modes", "TEXT NOT NULL DEFAULT ''"},
		{"memo_limit", "INTEGER NOT NULL DEFAULT 20"},
		{"suspended_by", "TEXT NOT NULL DEFAULT ''"},
		{"suspend_reason", "TEXT NOT NULL DEFAULT ''"},
		{"suspended_at", "INTEGER NOT NULL DEFAULT 0"},
		{"suspend_until", "INTEGER NOT NULL DEFAULT 0"},
		{"mlock_params", "TEXT NOT NULL DEFAULT ''"},
		{"chanstats", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, col := range cols {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('channels') WHERE name = ?", col.name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec("ALTER TABLE channels ADD COLUMN " + col.name + " " + col.def); err != nil {
				return err
			}
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO chan_entrymsg (channel_id, text, added_by, added_at)
		SELECT c.id, c.entrymsg, f.name, c.registered FROM channels c JOIN accounts f ON f.id = c.founder_id
		WHERE c.entrymsg != '' ORDER BY c.id`); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE channels SET entrymsg = '' WHERE entrymsg != ''"); err != nil {
		return err
	}
	return tx.Commit()
}

// loadChanExtra reads the extra channel tables into c.
func loadChanExtra(db *sql.DB, c *cache) error {
	steps := []struct {
		q    string
		scan func(*sql.Rows) error
	}{
		{`SELECT channel_id, priv, level FROM chan_levels`, func(r *sql.Rows) error {
			var ch int64
			var priv string
			var lvl int
			if err := r.Scan(&ch, &priv, &lvl); err != nil {
				return err
			}
			c.setChanLevel(ch, priv, lvl)
			return nil
		}},
		{`SELECT id, channel_id, text, added_by, added_at FROM chan_entrymsg ORDER BY id`, func(r *sql.Rows) error {
			var e EntryMsg
			var ch, at int64
			if err := r.Scan(&e.ID, &ch, &e.Text, &e.AddedBy, &at); err != nil {
				return err
			}
			e.AddedAt = time.Unix(at, 0)
			c.entryMsgs[ch] = append(c.entryMsgs[ch], e)
			return nil
		}},
		{`SELECT channel_id, command, method, status FROM chan_log ORDER BY command, method`, func(r *sql.Rows) error {
			var ch int64
			var l LogSetting
			if err := r.Scan(&ch, &l.Command, &l.Method, &l.Status); err != nil {
				return err
			}
			c.chanLogs[ch] = append(c.chanLogs[ch], l)
			return nil
		}},
		{`SELECT id, channel_id, sender, sent, body, seen FROM chan_memos ORDER BY id`, func(r *sql.Rows) error {
			var m Memo
			var ch, sent int64
			if err := r.Scan(&m.ID, &ch, &m.From, &sent, &m.Text, &m.Read); err != nil {
				return err
			}
			m.Sent = time.Unix(sent, 0)
			c.chanMemos[ch] = append(c.chanMemos[ch], m)
			c.chanMemoOwner[m.ID] = ch
			return nil
		}},
		{`SELECT channel_id, nick_key, nick FROM chan_memo_ignores ORDER BY nick_key`, func(r *sql.Rows) error {
			var ch int64
			var g ignoreRow
			if err := r.Scan(&ch, &g.key, &g.nick); err != nil {
				return err
			}
			c.chanIgnores[ch] = append(c.chanIgnores[ch], g)
			return nil
		}},
	}
	for _, st := range steps {
		if err := eachRow(db, st.q, st.scan); err != nil {
			return err
		}
	}
	return nil
}

func (c *cache) setChanLevel(ch int64, priv string, lvl int) {
	m := c.chanLevels[ch]
	if m == nil {
		m = make(map[string]int)
		c.chanLevels[ch] = m
	}
	m[priv] = lvl
}

// ChanLevels returns the privileges whose level the channel changed from
// the default, as privilege -> level.
func (s *Store) ChanLevels(c *Channel) map[string]int {
	out := make(map[string]int, len(s.c.chanLevels[c.ID]))
	for k, v := range s.c.chanLevels[c.ID] {
		out[k] = v
	}
	return out
}

// SetChanLevel sets the level a privilege needs on c.
func (s *Store) SetChanLevel(c *Channel, priv string, lvl int) error {
	if s.c.channels[c.ID] == nil {
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO chan_levels (channel_id, priv, level) VALUES (?, ?, ?)
		ON CONFLICT (channel_id, priv) DO UPDATE SET level = excluded.level`, c.ID, priv, lvl); err != nil {
		return err
	}
	s.c.setChanLevel(c.ID, priv, lvl)
	return nil
}

// ResetChanLevels puts every privilege of c back to its default.
func (s *Store) ResetChanLevels(c *Channel) error {
	if _, err := s.db.Exec("DELETE FROM chan_levels WHERE channel_id = ?", c.ID); err != nil {
		return err
	}
	delete(s.c.chanLevels, c.ID)
	return nil
}

// EntryMsgs returns c's entry message lines, oldest first.
func (s *Store) EntryMsgs(c *Channel) []EntryMsg {
	return append([]EntryMsg(nil), s.c.entryMsgs[c.ID]...)
}

// AddEntryMsg adds a line to c's entry message.
func (s *Store) AddEntryMsg(c *Channel, text, by string, now time.Time) error {
	if s.c.channels[c.ID] == nil {
		return nil
	}
	res, err := s.db.Exec("INSERT INTO chan_entrymsg (channel_id, text, added_by, added_at) VALUES (?, ?, ?, ?)", c.ID, text, by, now.Unix())
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	s.c.entryMsgs[c.ID] = append(s.c.entryMsgs[c.ID], EntryMsg{ID: id, Text: text, AddedBy: by, AddedAt: sec(now)})
	return nil
}

// SetEntryMsg changes the text of one line.
func (s *Store) SetEntryMsg(c *Channel, id int64, text string) error {
	if _, err := s.db.Exec("UPDATE chan_entrymsg SET text = ? WHERE id = ? AND channel_id = ?", text, id, c.ID); err != nil {
		return err
	}
	for i := range s.c.entryMsgs[c.ID] {
		if s.c.entryMsgs[c.ID][i].ID == id {
			list := append([]EntryMsg(nil), s.c.entryMsgs[c.ID]...)
			list[i].Text = text
			s.c.entryMsgs[c.ID] = list
		}
	}
	return nil
}

// DelEntryMsg removes one line.
func (s *Store) DelEntryMsg(c *Channel, id int64) error {
	if _, err := s.db.Exec("DELETE FROM chan_entrymsg WHERE id = ? AND channel_id = ?", id, c.ID); err != nil {
		return err
	}
	s.c.entryMsgs[c.ID] = without(s.c.entryMsgs[c.ID], func(e EntryMsg) bool { return e.ID == id })
	return nil
}

// ClearEntryMsgs removes every line and returns how many there were.
func (s *Store) ClearEntryMsgs(c *Channel) (int, error) {
	if _, err := s.db.Exec("DELETE FROM chan_entrymsg WHERE channel_id = ?", c.ID); err != nil {
		return 0, err
	}
	n := len(s.c.entryMsgs[c.ID])
	delete(s.c.entryMsgs, c.ID)
	return n, nil
}

// LogSettings returns c's LOG entries by command and method.
func (s *Store) LogSettings(c *Channel) []LogSetting {
	return append([]LogSetting(nil), s.c.chanLogs[c.ID]...)
}

// SetLog adds or changes a LOG entry.
func (s *Store) SetLog(c *Channel, l LogSetting) error {
	if s.c.channels[c.ID] == nil {
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO chan_log (channel_id, command, method, status) VALUES (?, ?, ?, ?)
		ON CONFLICT (channel_id, command, method) DO UPDATE SET status = excluded.status`, c.ID, l.Command, l.Method, l.Status); err != nil {
		return err
	}
	list := without(s.c.chanLogs[c.ID], func(o LogSetting) bool { return o.Command == l.Command && o.Method == l.Method })
	list = append(list, l)
	sort.Slice(list, func(i, j int) bool {
		if list[i].Command != list[j].Command {
			return list[i].Command < list[j].Command
		}
		return list[i].Method < list[j].Method
	})
	s.c.chanLogs[c.ID] = list
	return nil
}

// DelLog removes a LOG entry; false when there was none.
func (s *Store) DelLog(c *Channel, command, method string) (bool, error) {
	gone, err := s.affected("DELETE FROM chan_log WHERE channel_id = ? AND command = ? AND method = ?", c.ID, command, method)
	if err != nil || !gone {
		return gone, err
	}
	s.c.chanLogs[c.ID] = without(s.c.chanLogs[c.ID], func(o LogSetting) bool { return o.Command == command && o.Method == method })
	return true, nil
}

// AddChanMemo puts a memo in c's box.
func (s *Store) AddChanMemo(c *Channel, from, text string, now time.Time) error {
	if s.c.channels[c.ID] == nil {
		return nil
	}
	res, err := s.db.Exec("INSERT INTO chan_memos (channel_id, sender, sent, body) VALUES (?, ?, ?, ?)", c.ID, from, now.Unix(), text)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	s.c.chanMemos[c.ID] = append(s.c.chanMemos[c.ID], Memo{ID: id, From: from, Sent: sec(now), Text: text})
	s.c.chanMemoOwner[id] = c.ID
	return nil
}

// ChanMemos returns the memos in c's box, oldest first.
func (s *Store) ChanMemos(c *Channel) []Memo {
	return append([]Memo(nil), s.c.chanMemos[c.ID]...)
}

// MarkChanMemoRead marks a channel memo read (for everyone who reads the box).
func (s *Store) MarkChanMemoRead(id int64) error {
	if _, err := s.db.Exec("UPDATE chan_memos SET seen = 1 WHERE id = ?", id); err != nil {
		return err
	}
	ch := s.c.chanMemoOwner[id]
	list := append([]Memo(nil), s.c.chanMemos[ch]...)
	for i := range list {
		if list[i].ID == id {
			list[i].Read = true
		}
	}
	if len(list) > 0 {
		s.c.chanMemos[ch] = list
	}
	return nil
}

// DeleteChanMemo deletes a channel memo.
func (s *Store) DeleteChanMemo(id int64) error {
	if _, err := s.db.Exec("DELETE FROM chan_memos WHERE id = ?", id); err != nil {
		return err
	}
	ch, ok := s.c.chanMemoOwner[id]
	if !ok {
		return nil
	}
	delete(s.c.chanMemoOwner, id)
	s.c.chanMemos[ch] = without(s.c.chanMemos[ch], func(m Memo) bool { return m.ID == id })
	return nil
}

// ChanMemoIgnores returns the nicks whose memos c's box drops.
func (s *Store) ChanMemoIgnores(c *Channel) []string {
	var out []string
	for _, g := range s.c.chanIgnores[c.ID] {
		out = append(out, g.nick)
	}
	return out
}

// AddChanMemoIgnore adds nick to c's ignore list; false when it was there.
func (s *Store) AddChanMemoIgnore(c *Channel, nick string) (bool, error) {
	if s.c.channels[c.ID] == nil {
		return false, nil
	}
	key := irc.Fold(nick)
	added, err := s.affected("INSERT OR IGNORE INTO chan_memo_ignores (channel_id, nick_key, nick) VALUES (?, ?, ?)", c.ID, key, nick)
	if err != nil || !added {
		return added, err
	}
	list := append(append([]ignoreRow(nil), s.c.chanIgnores[c.ID]...), ignoreRow{key: key, nick: nick})
	sort.Slice(list, func(i, j int) bool { return list[i].key < list[j].key })
	s.c.chanIgnores[c.ID] = list
	return true, nil
}

// DelChanMemoIgnore removes nick; false when it wasn't there.
func (s *Store) DelChanMemoIgnore(c *Channel, nick string) (bool, error) {
	key := irc.Fold(nick)
	gone, err := s.affected("DELETE FROM chan_memo_ignores WHERE channel_id = ? AND nick_key = ?", c.ID, key)
	if err != nil || !gone {
		return gone, err
	}
	s.c.chanIgnores[c.ID] = without(s.c.chanIgnores[c.ID], func(g ignoreRow) bool { return g.key == key })
	return true, nil
}

// ChanIgnores reports whether c's box drops memos from nick.
func (s *Store) ChanIgnores(c *Channel, nick string) bool {
	key := irc.Fold(nick)
	for _, g := range s.c.chanIgnores[c.ID] {
		if g.key == key {
			return true
		}
	}
	return false
}

// AccountNameByID is the name of account id, "" when there is none.
func (s *Store) AccountNameByID(id int64) string { return s.c.accountName(id) }
