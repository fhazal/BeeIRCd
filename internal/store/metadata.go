package store

import (
	"database/sql"
	"maps"
)

// IRCv3 draft/metadata-2: the keys (avatar, color, status, ...) a
// logged-in user keeps on their account and a registered channel keeps
// with ChanServ. Rows go with the account or channel (ON DELETE CASCADE).

const metadataSchema = `
CREATE TABLE IF NOT EXISTS acct_metadata (
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (account_id, key)
);
CREATE TABLE IF NOT EXISTS chan_metadata (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (channel_id, key)
);
`

// mdCache keeps both tables in memory, like the rest of the store.
type mdCache struct {
	accounts map[int64]map[string]string
	channels map[int64]map[string]string
	// acctFields and chanFields are the extra INFO fields (fields.go).
	acctFields map[int64]map[string]string
	chanFields map[int64]map[string]string
}

func loadMetadata(db *sql.DB) (*mdCache, error) {
	m := &mdCache{accounts: map[int64]map[string]string{}, channels: map[int64]map[string]string{},
		acctFields: map[int64]map[string]string{}, chanFields: map[int64]map[string]string{}}
	load := func(q string, into map[int64]map[string]string) error {
		return eachRow(db, q, func(r *sql.Rows) error {
			var id int64
			var k, v string
			if err := r.Scan(&id, &k, &v); err != nil {
				return err
			}
			if into[id] == nil {
				into[id] = map[string]string{}
			}
			into[id][k] = v
			return nil
		})
	}
	if err := load(`SELECT account_id, key, value FROM acct_metadata`, m.accounts); err != nil {
		return nil, err
	}
	if err := load(`SELECT channel_id, key, value FROM chan_metadata`, m.channels); err != nil {
		return nil, err
	}
	if err := load(`SELECT account_id, key, value FROM acct_fields`, m.acctFields); err != nil {
		return nil, err
	}
	if err := load(`SELECT channel_id, key, value FROM chan_fields`, m.chanFields); err != nil {
		return nil, err
	}
	return m, nil
}

// AccountMetadata is a copy of account id's keys (nil when none).
func (s *Store) AccountMetadata(id int64) map[string]string {
	return maps.Clone(s.md.accounts[id])
}

// SetAccountMetadata sets key on account id; an empty value deletes it.
func (s *Store) SetAccountMetadata(id int64, key, value string) error {
	return s.setMetadata(s.md.accounts, "acct_metadata", "account_id", id, key, value)
}

// ClearAccountMetadata deletes all of account id's keys.
func (s *Store) ClearAccountMetadata(id int64) error {
	return s.clearMetadata(s.md.accounts, "acct_metadata", "account_id", id)
}

// ChannelMetadata is a copy of registered channel id's keys (nil when none).
func (s *Store) ChannelMetadata(id int64) map[string]string {
	return maps.Clone(s.md.channels[id])
}

// SetChannelMetadata sets key on channel id; an empty value deletes it.
func (s *Store) SetChannelMetadata(id int64, key, value string) error {
	return s.setMetadata(s.md.channels, "chan_metadata", "channel_id", id, key, value)
}

// ClearChannelMetadata deletes all of channel id's keys.
func (s *Store) ClearChannelMetadata(id int64) error {
	return s.clearMetadata(s.md.channels, "chan_metadata", "channel_id", id)
}

func (s *Store) setMetadata(cache map[int64]map[string]string, table, col string, id int64, key, value string) error {
	if value == "" {
		if _, err := s.db.Exec(`DELETE FROM `+table+` WHERE `+col+` = ? AND key = ?`, id, key); err != nil {
			return err
		}
		if m := cache[id]; m != nil {
			delete(m, key)
			if len(m) == 0 {
				delete(cache, id)
			}
		}
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO `+table+` (`+col+`, key, value) VALUES (?, ?, ?) ON CONFLICT(`+col+`, key) DO UPDATE SET value = excluded.value`, id, key, value); err != nil {
		return err
	}
	if cache[id] == nil {
		cache[id] = map[string]string{}
	}
	cache[id][key] = value
	return nil
}

func (s *Store) clearMetadata(cache map[int64]map[string]string, table, col string, id int64) error {
	if _, err := s.db.Exec(`DELETE FROM `+table+` WHERE `+col+` = ?`, id); err != nil {
		return err
	}
	delete(cache, id)
	return nil
}
