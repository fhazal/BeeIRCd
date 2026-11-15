package store

import (
	"database/sql"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// HelpServ: the owner's own help topics and the accounts that get user
// mode +h (helper) when they identify.

const helpServSchema = `
CREATE TABLE IF NOT EXISTS help_topics (
	name_key TEXT PRIMARY KEY,
	name     TEXT NOT NULL,
	body     TEXT NOT NULL,
	set_by   TEXT NOT NULL,
	set_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS helpers (
	account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
	added_by   TEXT NOT NULL,
	added_at   INTEGER NOT NULL
);
`

// HelpTopic is one of the owner's HelpServ answers. Text has one line per
// TOPIC ADD, joined with "\n".
type HelpTopic struct {
	Name  string
	Text  string
	SetBy string
	SetAt time.Time
}

// Helper is an account on the HelpServ helper list.
type Helper struct {
	AccountID int64
	AddedBy   string
	AddedAt   time.Time
}

// HelpTopics returns every topic, by name.
func (s *Store) HelpTopics() ([]HelpTopic, error) {
	var out []HelpTopic
	err := eachRow(s.db, "SELECT name, body, set_by, set_at FROM help_topics ORDER BY name_key", func(r *sql.Rows) error {
		var t HelpTopic
		var at int64
		if err := r.Scan(&t.Name, &t.Text, &t.SetBy, &at); err != nil {
			return err
		}
		t.SetAt = time.Unix(at, 0)
		out = append(out, t)
		return nil
	})
	return out, err
}

// SetHelpTopic adds or replaces a topic.
func (s *Store) SetHelpTopic(t HelpTopic) error {
	_, err := s.db.Exec("INSERT INTO help_topics (name_key, name, body, set_by, set_at) VALUES (?, ?, ?, ?, ?) "+
		"ON CONFLICT(name_key) DO UPDATE SET name = excluded.name, body = excluded.body, set_by = excluded.set_by, set_at = excluded.set_at",
		irc.Fold(t.Name), t.Name, t.Text, t.SetBy, t.SetAt.Unix())
	return err
}

// DelHelpTopic removes a topic; false when there was none.
func (s *Store) DelHelpTopic(name string) (bool, error) {
	return s.affected("DELETE FROM help_topics WHERE name_key = ?", irc.Fold(name))
}

// Helpers returns the helper list.
func (s *Store) Helpers() ([]Helper, error) {
	var out []Helper
	err := eachRow(s.db, "SELECT account_id, added_by, added_at FROM helpers ORDER BY added_at", func(r *sql.Rows) error {
		var h Helper
		var at int64
		if err := r.Scan(&h.AccountID, &h.AddedBy, &at); err != nil {
			return err
		}
		h.AddedAt = time.Unix(at, 0)
		out = append(out, h)
		return nil
	})
	return out, err
}

// AddHelper puts an account on the helper list; false when it was on it.
func (s *Store) AddHelper(h Helper) (bool, error) {
	return s.affected("INSERT OR IGNORE INTO helpers (account_id, added_by, added_at) VALUES (?, ?, ?)", h.AccountID, h.AddedBy, h.AddedAt.Unix())
}

// DelHelper takes an account off the helper list; false when it wasn't on it.
func (s *Store) DelHelper(id int64) (bool, error) {
	return s.affected("DELETE FROM helpers WHERE account_id = ?", id)
}
