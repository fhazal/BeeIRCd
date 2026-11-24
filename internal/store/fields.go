package store

import "maps"

// Extra INFO fields (Anope's ns_set_misc / cs_set_misc): free-text
// settings named in [services] nick_fields and chan_fields, like ICQ or
// TWITTER. Keys are the lower-case field name; rows go with the account
// or channel (ON DELETE CASCADE). A field taken out of the config keeps
// its rows; services just stop showing it.

const fieldsSchema = `
CREATE TABLE IF NOT EXISTS acct_fields (
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (account_id, key)
);
CREATE TABLE IF NOT EXISTS chan_fields (
	channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	key        TEXT NOT NULL,
	value      TEXT NOT NULL,
	PRIMARY KEY (channel_id, key)
);
`

// AccountFields is a copy of account id's extra fields (nil when none).
func (s *Store) AccountFields(id int64) map[string]string {
	return maps.Clone(s.md.acctFields[id])
}

// SetAccountField sets field key on account id; an empty value deletes it.
func (s *Store) SetAccountField(id int64, key, value string) error {
	return s.setMetadata(s.md.acctFields, "acct_fields", "account_id", id, key, value)
}

// ChannelFields is a copy of registered channel id's extra fields (nil
// when none).
func (s *Store) ChannelFields(id int64) map[string]string {
	return maps.Clone(s.md.chanFields[id])
}

// SetChannelField sets field key on channel id; an empty value deletes it.
func (s *Store) SetChannelField(id int64, key, value string) error {
	return s.setMetadata(s.md.chanFields, "chan_fields", "channel_id", id, key, value)
}
