package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// HostServ. A vhost belongs to a nick (Anope's NickAlias), so
// each grouped nick may have its own; one pending request per nick. Both
// go when the nick (or its account) goes.

const hostServSchema = `
CREATE TABLE IF NOT EXISTS vhosts (
	nick_key   TEXT PRIMARY KEY REFERENCES nicks(nick_key) ON DELETE CASCADE,
	ident      TEXT NOT NULL DEFAULT '',
	host       TEXT NOT NULL,
	creator    TEXT NOT NULL,
	created    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS vhost_requests (
	nick_key   TEXT PRIMARY KEY REFERENCES nicks(nick_key) ON DELETE CASCADE,
	ident      TEXT NOT NULL DEFAULT '',
	host       TEXT NOT NULL,
	requested  INTEGER NOT NULL
);
`

// ErrNotFound is returned for a nick that isn't registered.
var ErrNotFound = errors.New("not registered")

// VHost is a nick's vhost: [Ident@]Host.
type VHost struct {
	Nick    string // the nick it belongs to, as registered
	Ident   string // "" = keep the user's own ident
	Host    string
	Creator string
	Created time.Time
}

// Mask is ident@host, or host without a vident.
func (v VHost) Mask() string {
	if v.Ident != "" {
		return v.Ident + "@" + v.Host
	}
	return v.Host
}

type vhostRow struct {
	ident, host, by string
	at              int64
}

func loadHostServ(db *sql.DB, c *cache) error {
	if err := eachRow(db, "SELECT nick_key, ident, host, creator, created FROM vhosts", func(r *sql.Rows) error {
		var key string
		var v vhostRow
		if err := r.Scan(&key, &v.ident, &v.host, &v.by, &v.at); err != nil {
			return err
		}
		c.vhosts[key] = &v
		return nil
	}); err != nil {
		return err
	}
	return eachRow(db, "SELECT nick_key, ident, host, requested FROM vhost_requests", func(r *sql.Rows) error {
		var key string
		var v vhostRow
		if err := r.Scan(&key, &v.ident, &v.host, &v.at); err != nil {
			return err
		}
		c.vreqs[key] = &v
		return nil
	})
}

func (s *Store) vhostOut(key string, v *vhostRow) VHost {
	out := VHost{Ident: v.ident, Host: v.host, Creator: v.by, Created: time.Unix(v.at, 0)}
	if n := s.c.nicks[key]; n != nil {
		out.Nick = n.nick
	}
	return out
}

// VHostOf returns nick's vhost.
func (s *Store) VHostOf(nick string) (VHost, bool) {
	key := irc.Fold(nick)
	v := s.c.vhosts[key]
	if v == nil {
		return VHost{}, false
	}
	return s.vhostOut(key, v), true
}

// SetVHost gives a registered nick a vhost (replacing any).
func (s *Store) SetVHost(nick, ident, host, by string, now time.Time) error {
	key := irc.Fold(nick)
	if s.c.nicks[key] == nil {
		return ErrNotFound
	}
	if _, err := s.db.Exec("INSERT OR REPLACE INTO vhosts (nick_key, ident, host, creator, created) VALUES (?, ?, ?, ?, ?)",
		key, ident, host, by, now.Unix()); err != nil {
		return err
	}
	s.c.vhosts[key] = &vhostRow{ident: ident, host: host, by: by, at: now.Unix()}
	return nil
}

// DelVHost removes nick's vhost; false when it had none.
func (s *Store) DelVHost(nick string) (bool, error) {
	key := irc.Fold(nick)
	gone, err := s.affected("DELETE FROM vhosts WHERE nick_key = ?", key)
	if err != nil || !gone {
		return gone, err
	}
	delete(s.c.vhosts, key)
	return true, nil
}

// VHosts returns every vhost, by nick.
func (s *Store) VHosts() []VHost {
	keys := make([]string, 0, len(s.c.vhosts))
	for k := range s.c.vhosts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]VHost, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.vhostOut(k, s.c.vhosts[k]))
	}
	return out
}

// RequestVHost records nick's request (replacing an older one).
func (s *Store) RequestVHost(nick, ident, host string, now time.Time) error {
	key := irc.Fold(nick)
	if s.c.nicks[key] == nil {
		return ErrNotFound
	}
	if _, err := s.db.Exec("INSERT OR REPLACE INTO vhost_requests (nick_key, ident, host, requested) VALUES (?, ?, ?, ?)",
		key, ident, host, now.Unix()); err != nil {
		return err
	}
	s.c.vreqs[key] = &vhostRow{ident: ident, host: host, at: now.Unix()}
	return nil
}

// VHostRequest returns nick's pending request (Created = when asked).
func (s *Store) VHostRequest(nick string) (VHost, bool) {
	key := irc.Fold(nick)
	v := s.c.vreqs[key]
	if v == nil {
		return VHost{}, false
	}
	return s.vhostOut(key, v), true
}

// DelVHostRequest drops nick's request; false when there was none.
func (s *Store) DelVHostRequest(nick string) (bool, error) {
	key := irc.Fold(nick)
	gone, err := s.affected("DELETE FROM vhost_requests WHERE nick_key = ?", key)
	if err != nil || !gone {
		return gone, err
	}
	delete(s.c.vreqs, key)
	return true, nil
}

// VHostRequests returns the pending requests, oldest first.
func (s *Store) VHostRequests() []VHost {
	out := make([]VHost, 0, len(s.c.vreqs))
	for k, v := range s.c.vreqs {
		out = append(out, s.vhostOut(k, v))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return irc.Fold(out[i].Nick) < irc.Fold(out[j].Nick)
	})
	return out
}

// GroupNicks returns the nicks of the account nick belongs to.
func (s *Store) GroupNicks(nick string) []string {
	n := s.c.nicks[irc.Fold(nick)]
	if n == nil {
		return nil
	}
	var out []string
	for k := range s.c.acctNicks[n.account] {
		out = append(out, s.c.nicks[k].nick)
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i]) < irc.Fold(out[j]) })
	return out
}
