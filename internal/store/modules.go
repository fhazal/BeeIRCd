package store

// What Lua modules keep across restarts: small values under a key, a
// table of their own per module.

const moduleSchema = `
CREATE TABLE IF NOT EXISTS module_data (
	module TEXT NOT NULL,
	key    TEXT NOT NULL,
	value  TEXT NOT NULL,
	PRIMARY KEY (module, key)
);
`

// ModuleData returns everything module has kept.
func (s *Store) ModuleData(module string) (map[string]string, error) {
	out := map[string]string{}
	rows, err := s.db.Query("SELECT key, value FROM module_data WHERE module = ?", module)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ModuleSet keeps value under key for module.
func (s *Store) ModuleSet(module, key, value string) error {
	_, err := s.db.Exec("INSERT INTO module_data (module, key, value) VALUES (?, ?, ?) ON CONFLICT(module, key) DO UPDATE SET value = excluded.value", module, key, value)
	return err
}

// ModuleDelete forgets key of module.
func (s *Store) ModuleDelete(module, key string) error {
	_, err := s.db.Exec("DELETE FROM module_data WHERE module = ? AND key = ?", module, key)
	return err
}
