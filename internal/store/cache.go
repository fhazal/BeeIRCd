package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// cache is the services data in memory. Every read is answered from here;
// every write goes to SQLite first and, once that worked, to the cache
// (write-through). Only the server loop touches it, so it has no locks.
//
// AKILLs, forbids, news and session exceptions are not in it: the server
// keeps its own copy of those.
type cache struct {
	accounts  map[int64]*Account
	nicks     map[string]*nickRow        // by folded nick
	acctNicks map[int64]map[string]bool  // account -> folded nicks
	access    map[int64][]string         // account -> masks, sorted
	memos     map[int64][]Memo           // account -> memos, by ID
	memoOwner map[int64]int64            // memo ID -> account
	ignores   map[int64][]ignoreRow      // account -> ignored nicks, by key
	channels  map[int64]*Channel         // Founder and Successor names are filled in on read
	chanKeys  map[string]int64           // folded name -> channel ID
	levels    map[int64]map[int64]*level // channel -> account -> entry
	akicks    map[int64][]Akick          // channel -> AKICKs, by time and mask
	opers     map[int64]*Oper            // by account ID; Account is filled in on read

	chanLevels    map[int64]map[string]int // channel -> privilege -> level (only changed ones)
	entryMsgs     map[int64][]EntryMsg     // channel -> lines, by ID
	chanLogs      map[int64][]LogSetting   // channel -> LOG entries, by command and method
	chanMemos     map[int64][]Memo         // channel -> memo box, by ID
	chanMemoOwner map[int64]int64          // channel memo ID -> channel
	chanIgnores   map[int64][]ignoreRow    // channel -> ignored nicks, by key

	certs     map[int64][]string     // account -> CERT fingerprints, oldest first
	certOwner map[string]int64       // fingerprint -> account
	ajoins    map[int64][]AjoinEntry // account -> AJOIN list, by ID
	buddies   map[int64][]ignoreRow  // account -> BUDDY nicks, by key

	vhosts map[string]*vhostRow // folded nick -> HostServ vhost
	vreqs  map[string]*vhostRow // folded nick -> pending HostServ request
}

type nickRow struct {
	key, nick  string
	account    int64
	registered int64
}

type ignoreRow struct{ key, nick string }

type level struct {
	level   int
	addedBy string
	addedAt time.Time
	note    string
}

func newCache() *cache {
	return &cache{
		accounts:  make(map[int64]*Account),
		nicks:     make(map[string]*nickRow),
		acctNicks: make(map[int64]map[string]bool),
		access:    make(map[int64][]string),
		memos:     make(map[int64][]Memo),
		memoOwner: make(map[int64]int64),
		ignores:   make(map[int64][]ignoreRow),
		channels:  make(map[int64]*Channel),
		chanKeys:  make(map[string]int64),
		levels:    make(map[int64]map[int64]*level),
		akicks:    make(map[int64][]Akick),
		opers:     make(map[int64]*Oper),

		chanLevels:    make(map[int64]map[string]int),
		entryMsgs:     make(map[int64][]EntryMsg),
		chanLogs:      make(map[int64][]LogSetting),
		chanMemos:     make(map[int64][]Memo),
		chanMemoOwner: make(map[int64]int64),
		chanIgnores:   make(map[int64][]ignoreRow),

		certs:     make(map[int64][]string),
		certOwner: make(map[string]int64),
		ajoins:    make(map[int64][]AjoinEntry),
		buddies:   make(map[int64][]ignoreRow),

		vhosts: make(map[string]*vhostRow),
		vreqs:  make(map[string]*vhostRow),
	}
}

// loadCache reads every cached table from db.
func loadCache(db *sql.DB) (*cache, error) {
	c := newCache()
	steps := []struct {
		q    string
		scan func(*sql.Rows) error
	}{
		{`SELECT ` + accountCols + ` FROM accounts a`, func(r *sql.Rows) error {
			a, err := scanAccount(r)
			if err == nil {
				c.accounts[a.ID] = a
			}
			return err
		}},
		{`SELECT nick_key, nick, account_id, registered FROM nicks`, func(r *sql.Rows) error {
			var n nickRow
			if err := r.Scan(&n.key, &n.nick, &n.account, &n.registered); err != nil {
				return err
			}
			c.addNick(&n)
			return nil
		}},
		{`SELECT account_id, mask FROM access ORDER BY mask`, func(r *sql.Rows) error {
			var id int64
			var m string
			if err := r.Scan(&id, &m); err != nil {
				return err
			}
			c.access[id] = append(c.access[id], m)
			return nil
		}},
		{`SELECT id, account_id, sender, sent, body, seen FROM memos ORDER BY id`, func(r *sql.Rows) error {
			var m Memo
			var id, sent int64
			if err := r.Scan(&m.ID, &id, &m.From, &sent, &m.Text, &m.Read); err != nil {
				return err
			}
			m.Sent = time.Unix(sent, 0)
			c.memos[id] = append(c.memos[id], m)
			c.memoOwner[m.ID] = id
			return nil
		}},
		{`SELECT account_id, nick_key, nick FROM memo_ignores ORDER BY nick_key`, func(r *sql.Rows) error {
			var id int64
			var g ignoreRow
			if err := r.Scan(&id, &g.key, &g.nick); err != nil {
				return err
			}
			c.ignores[id] = append(c.ignores[id], g)
			return nil
		}},
		{`SELECT ` + channelCols + channelFrom, func(r *sql.Rows) error {
			ch, err := scanChannel(r)
			if err == nil {
				ch.Founder, ch.Successor = "", ""
				c.channels[ch.ID] = ch
				c.chanKeys[irc.Fold(ch.Name)] = ch.ID
			}
			return err
		}},
		{`SELECT channel_id, account_id, level, added_by, added_at, note FROM chan_access`, func(r *sql.Rows) error {
			var ch, acct, at int64
			var l level
			if err := r.Scan(&ch, &acct, &l.level, &l.addedBy, &at, &l.note); err != nil {
				return err
			}
			l.addedAt = time.Unix(at, 0)
			c.setLevel(ch, acct, &l)
			return nil
		}},
		{`SELECT channel_id, mask, reason, added_by, added_at FROM chan_akick ORDER BY added_at, mask`, func(r *sql.Rows) error {
			var ch, at int64
			var k Akick
			if err := r.Scan(&ch, &k.Mask, &k.Reason, &k.AddedBy, &at); err != nil {
				return err
			}
			k.AddedAt = time.Unix(at, 0)
			c.akicks[ch] = append(c.akicks[ch], k)
			return nil
		}},
		{`SELECT o.account_id, a.name, o.flags, o.title, o.hosts, o.pass_hash, o.added_by, o.added_at
			FROM opers o JOIN accounts a ON a.id = o.account_id`, func(r *sql.Rows) error {
			o, err := scanOper(r)
			if err == nil {
				o.Account = ""
				if len(o.Hosts) == 0 {
					o.Hosts = nil
				}
				c.opers[o.AccountID] = o
			}
			return err
		}},
	}
	for _, st := range steps {
		if err := eachRow(db, st.q, st.scan); err != nil {
			return nil, err
		}
	}
	if err := loadChanExtra(db, c); err != nil {
		return nil, err
	}
	if err := loadNS156(db, c); err != nil {
		return nil, err
	}
	if err := loadHostServ(db, c); err != nil {
		return nil, err
	}
	if err := loadMS156(db, c); err != nil {
		return nil, err
	}
	return c, nil
}

func eachRow(db *sql.DB, q string, scan func(*sql.Rows) error) error {
	rows, err := db.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c *cache) addNick(n *nickRow) {
	c.nicks[n.key] = n
	set := c.acctNicks[n.account]
	if set == nil {
		set = make(map[string]bool)
		c.acctNicks[n.account] = set
	}
	set[n.key] = true
}

func (c *cache) removeNick(key string) {
	n := c.nicks[key]
	if n == nil {
		return
	}
	delete(c.nicks, key)
	delete(c.acctNicks[n.account], key)
	delete(c.vhosts, key)
	delete(c.vreqs, key)
}

func (c *cache) setLevel(ch, acct int64, l *level) {
	m := c.levels[ch]
	if m == nil {
		m = make(map[int64]*level)
		c.levels[ch] = m
	}
	m[acct] = l
}

// accountName is the name of account id, "" when there is none.
func (c *cache) accountName(id int64) string {
	if a := c.accounts[id]; a != nil {
		return a.Name
	}
	return ""
}

// account returns a copy of account id, or nil.
func (c *cache) account(id int64) *Account {
	a := c.accounts[id]
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

// channel returns a copy of channel id with the names filled in.
func (c *cache) channel(id int64) *Channel {
	ch := c.channels[id]
	if ch == nil {
		return nil
	}
	cp := *ch
	cp.Founder = c.accountName(ch.FounderID)
	cp.Successor = c.accountName(ch.SuccessorID)
	return &cp
}

// oper returns a copy of the oper entry of account id.
func (c *cache) oper(id int64) *Oper {
	o := c.opers[id]
	if o == nil {
		return nil
	}
	cp := *o
	cp.Account = c.accountName(id)
	cp.Hosts = append([]string(nil), o.Hosts...)
	return &cp
}

// dropChannel forgets a channel and everything on it.
func (c *cache) dropChannel(id int64) {
	if ch := c.channels[id]; ch != nil {
		delete(c.chanKeys, irc.Fold(ch.Name))
	}
	delete(c.channels, id)
	delete(c.levels, id)
	delete(c.akicks, id)
	delete(c.chanLevels, id)
	delete(c.entryMsgs, id)
	delete(c.chanLogs, id)
	for _, m := range c.chanMemos[id] {
		delete(c.chanMemoOwner, m.ID)
	}
	delete(c.chanMemos, id)
	delete(c.chanIgnores, id)
}

// dropAccount forgets an account and what hangs off it. Channels it
// founded must be dealt with first.
func (c *cache) dropAccount(id int64) {
	for key := range c.acctNicks[id] {
		delete(c.nicks, key)
		delete(c.vhosts, key)
		delete(c.vreqs, key)
	}
	delete(c.acctNicks, id)
	delete(c.access, id)
	for _, m := range c.memos[id] {
		delete(c.memoOwner, m.ID)
	}
	delete(c.memos, id)
	delete(c.ignores, id)
	delete(c.opers, id)
	for _, fp := range c.certs[id] {
		delete(c.certOwner, fp)
	}
	delete(c.certs, id)
	delete(c.ajoins, id)
	delete(c.buddies, id)
	for _, lv := range c.levels {
		delete(lv, id)
	}
	for _, ch := range c.channels {
		if ch.SuccessorID == id {
			ch.SuccessorID = 0
		}
	}
	delete(c.accounts, id)
}

func sortAkicks(list []Akick) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].AddedAt.Equal(list[j].AddedAt) {
			return list[i].AddedAt.Before(list[j].AddedAt)
		}
		return list[i].Mask < list[j].Mask
	})
}

// sec drops everything below a second, as SQLite keeps Unix seconds.
func sec(t time.Time) time.Time { return time.Unix(t.Unix(), 0) }

// VerifyCache reloads everything from SQLite and reports the first place
// where it differs from the cache. Tests use it after every change.
func (s *Store) VerifyCache() error {
	fresh, err := loadCache(s.db)
	if err != nil {
		return err
	}
	have := s.c.trimmed()
	want := fresh.trimmed()
	parts := []struct {
		name       string
		have, want any
	}{
		{"accounts", have.accounts, want.accounts}, {"nicks", have.nicks, want.nicks},
		{"account nicks", have.acctNicks, want.acctNicks}, {"access", have.access, want.access},
		{"memos", have.memos, want.memos}, {"memo owners", have.memoOwner, want.memoOwner},
		{"memo ignores", have.ignores, want.ignores}, {"channels", have.channels, want.channels},
		{"channel names", have.chanKeys, want.chanKeys}, {"levels", have.levels, want.levels},
		{"akicks", have.akicks, want.akicks}, {"opers", have.opers, want.opers},
		{"channel levels", have.chanLevels, want.chanLevels}, {"entry messages", have.entryMsgs, want.entryMsgs},
		{"channel logs", have.chanLogs, want.chanLogs}, {"channel memos", have.chanMemos, want.chanMemos},
		{"channel memo owners", have.chanMemoOwner, want.chanMemoOwner}, {"channel memo ignores", have.chanIgnores, want.chanIgnores},
		{"certs", have.certs, want.certs}, {"cert owners", have.certOwner, want.certOwner}, {"ajoins", have.ajoins, want.ajoins}, {"buddies", have.buddies, want.buddies},
		{"vhosts", have.vhosts, want.vhosts}, {"vhost requests", have.vreqs, want.vreqs},
	}
	for _, p := range parts {
		if !reflect.DeepEqual(p.have, p.want) {
			return fmt.Errorf("cache differs from the database in %s", p.name)
		}
	}
	return nil
}

// trimmed is a shallow copy without empty per-account or per-channel
// entries, which mean the same as missing ones.
func (c *cache) trimmed() *cache {
	cp := *c
	cp.acctNicks = trimMap(c.acctNicks)
	cp.access = trimMap(c.access)
	cp.memos = trimMap(c.memos)
	cp.ignores = trimMap(c.ignores)
	cp.levels = trimMap(c.levels)
	cp.akicks = trimMap(c.akicks)
	cp.chanLevels = trimMap(c.chanLevels)
	cp.entryMsgs = trimMap(c.entryMsgs)
	cp.chanLogs = trimMap(c.chanLogs)
	cp.chanMemos = trimMap(c.chanMemos)
	cp.chanIgnores = trimMap(c.chanIgnores)
	cp.certs = trimMap(c.certs)
	cp.ajoins = trimMap(c.ajoins)
	cp.buddies = trimMap(c.buddies)
	return &cp
}

func trimMap[V any](m map[int64]V) map[int64]V {
	out := make(map[int64]V, len(m))
	for k, v := range m {
		if reflect.ValueOf(v).Len() > 0 {
			out[k] = v
		}
	}
	return out
}
