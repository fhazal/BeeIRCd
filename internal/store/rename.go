package store

import (
	"database/sql"

	"github.com/fhazal/beeircd/internal/irc"
)

// RenameChannel moves everything kept under channel old to the name new
// (IRCv3 channel-rename), in one transaction: the registration (with it
// the access list, AKICKs, settings, bot, entry message and log, which all
// hang off the channel's id), read markers, AJOIN entries and last-seen
// lines. A change of case only keeps the keys and sets the new spelling.
// Moving onto a registered name that is another channel's is refused with
// ErrExists.
func (s *Store) RenameChannel(old, new string) error {
	oldKey, newKey := irc.Fold(old), irc.Fold(new)
	id, registered := s.c.chanKeys[oldKey]
	if other, ok := s.c.chanKeys[newKey]; ok && (!registered || other != id) {
		return ErrExists
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if registered {
		if _, err := tx.Exec("UPDATE channels SET name = ?, name_key = ? WHERE id = ?", new, newKey, id); err != nil {
			return err
		}
	}
	if oldKey != newKey {
		// A marker already kept under the new name gives way to the moved one.
		if _, err := tx.Exec("UPDATE OR REPLACE read_markers SET target = ? WHERE target = ?", newKey, oldKey); err != nil {
			return err
		}
	}
	ajoin, err := renameRows(tx, "SELECT id, channel FROM ajoin", "UPDATE ajoin SET channel = ? WHERE id = ?", oldKey, new)
	if err != nil {
		return err
	}
	if _, err := renameRows(tx, "SELECT rowid, channel FROM seen", "UPDATE seen SET channel = ? WHERE rowid = ?", oldKey, new); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if registered {
		delete(s.c.chanKeys, oldKey)
		s.c.chanKeys[newKey] = id
		if ch := s.c.channels[id]; ch != nil {
			ch.Name = new
		}
	}
	if len(ajoin) > 0 {
		moved := make(map[int64]bool, len(ajoin))
		for _, row := range ajoin {
			moved[row] = true
		}
		for acc, list := range s.c.ajoins {
			var cp []AjoinEntry
			for i, e := range list {
				if moved[e.ID] {
					if cp == nil {
						cp = append([]AjoinEntry(nil), list...)
					}
					cp[i].Channel = new
				}
			}
			if cp != nil {
				s.c.ajoins[acc] = cp
			}
		}
	}
	return nil
}

// renameRows sets the channel column to new in each row whose channel folds
// to oldKey (the column holds the name as written, so SQL can't compare
// it), and returns those rows' ids.
func renameRows(tx *sql.Tx, query, update, oldKey, new string) ([]int64, error) {
	var ids []int64
	rows, err := tx.Query(query)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		if name != "" && irc.Fold(name) == oldKey {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(update, new, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
