package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// BotServ data (Anope 2.0.20 BotServ): the bots opers create, which bot a
// registered channel has and its bot options, and NickServ greets.

// Bot is one BotServ bot (Anope's BotInfo for bots made with BOT ADD).
type Bot struct {
	ID       int64
	Nick     string
	Ident    string
	Host     string
	Realname string
	Created  time.Time
	Private  bool // SET PRIVATE: only opers may assign it (Anope's oper_only)
}

// ChanBot is a registered channel's BotServ settings. A channel without a
// row has Anope's defaults: greet and fantasy on (botserv:defaults).
type ChanBot struct {
	BotID          int64 // 0 = no bot assigned
	Greet          bool
	Fantasy        bool
	NoBot          bool // SET NOBOT: no bot may be assigned
	DontKickOps    bool
	DontKickVoices bool
	BanExpire      time.Duration // SET BANEXPIRE, 0 = bot bans stay
}

// DefaultChanBot is a channel's settings before anyone changes them.
func DefaultChanBot() ChanBot { return ChanBot{Greet: true, Fantasy: true} }

const botServSchema = `
CREATE TABLE IF NOT EXISTS bots (
	id       INTEGER PRIMARY KEY,
	nick     TEXT NOT NULL,
	nick_key TEXT NOT NULL UNIQUE,
	ident    TEXT NOT NULL,
	host     TEXT NOT NULL,
	realname TEXT NOT NULL,
	created  INTEGER NOT NULL,
	private  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chan_bot (
	channel_id       INTEGER PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
	bot_id           INTEGER NOT NULL DEFAULT 0,
	greet            INTEGER NOT NULL DEFAULT 1,
	fantasy          INTEGER NOT NULL DEFAULT 1,
	nobot            INTEGER NOT NULL DEFAULT 0,
	dontkickops      INTEGER NOT NULL DEFAULT 0,
	dontkickvoices   INTEGER NOT NULL DEFAULT 0,
	ban_expire       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS acct_greet (
	account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
	greet      TEXT NOT NULL
);
`

// bsCache keeps the BotServ tables in memory, like the rest of the store.
type bsCache struct {
	bots   map[int64]*Bot
	chans  map[int64]ChanBot // by channel ID
	greets map[int64]string  // by account ID
}

func loadBotServ(db *sql.DB) (*bsCache, error) {
	b := &bsCache{bots: map[int64]*Bot{}, chans: map[int64]ChanBot{}, greets: map[int64]string{}}
	rows, err := db.Query(`SELECT id, nick, ident, host, realname, created, private FROM bots`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x Bot
		var created int64
		var priv int
		if err := rows.Scan(&x.ID, &x.Nick, &x.Ident, &x.Host, &x.Realname, &created, &priv); err != nil {
			rows.Close()
			return nil, err
		}
		x.Created, x.Private = time.Unix(created, 0), priv != 0
		b.bots[x.ID] = &x
	}
	rows.Close()
	rows, err = db.Query(`SELECT channel_id, bot_id, greet, fantasy, nobot, dontkickops, dontkickvoices, ban_expire FROM chan_bot`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ch int64
		var cb ChanBot
		var g, f, nb, dko, dkv int
		var exp int64
		if err := rows.Scan(&ch, &cb.BotID, &g, &f, &nb, &dko, &dkv, &exp); err != nil {
			rows.Close()
			return nil, err
		}
		cb.Greet, cb.Fantasy, cb.NoBot, cb.DontKickOps, cb.DontKickVoices = g != 0, f != 0, nb != 0, dko != 0, dkv != 0
		cb.BanExpire = time.Duration(exp) * time.Second
		b.chans[ch] = cb
	}
	rows.Close()
	rows, err = db.Query(`SELECT account_id, greet FROM acct_greet`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var g string
		if err := rows.Scan(&id, &g); err != nil {
			return nil, err
		}
		b.greets[id] = g
	}
	return b, rows.Err()
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Bots lists every bot, by nick.
func (s *Store) Bots() []*Bot {
	out := make([]*Bot, 0, len(s.bs.bots))
	for _, b := range s.bs.bots {
		cp := *b
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Nick) < irc.Fold(out[j].Nick) })
	return out
}

// BotByNick finds a bot, or nil.
func (s *Store) BotByNick(nick string) *Bot {
	k := irc.Fold(nick)
	for _, b := range s.bs.bots {
		if irc.Fold(b.Nick) == k {
			cp := *b
			return &cp
		}
	}
	return nil
}

// BotByID finds a bot, or nil.
func (s *Store) BotByID(id int64) *Bot {
	if b := s.bs.bots[id]; b != nil {
		cp := *b
		return &cp
	}
	return nil
}

// AddBot saves a new bot; ErrExists when the nick is taken.
func (s *Store) AddBot(b *Bot) error {
	if s.BotByNick(b.Nick) != nil {
		return ErrExists
	}
	res, err := s.db.Exec(`INSERT INTO bots (nick, nick_key, ident, host, realname, created, private) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.Nick, irc.Fold(b.Nick), b.Ident, b.Host, b.Realname, b.Created.Unix(), b2i(b.Private))
	if err != nil {
		return err
	}
	b.ID, _ = res.LastInsertId()
	cp := *b
	s.bs.bots[b.ID] = &cp
	return nil
}

// SaveBot writes a changed bot.
func (s *Store) SaveBot(b *Bot) error {
	if s.bs.bots[b.ID] == nil {
		return errors.New("no such bot")
	}
	if _, err := s.db.Exec(`UPDATE bots SET nick = ?, nick_key = ?, ident = ?, host = ?, realname = ?, private = ? WHERE id = ?`,
		b.Nick, irc.Fold(b.Nick), b.Ident, b.Host, b.Realname, b2i(b.Private), b.ID); err != nil {
		return err
	}
	cp := *b
	s.bs.bots[b.ID] = &cp
	return nil
}

// DeleteBot removes a bot and takes it off every channel. It returns the
// IDs of the channels it was assigned to.
func (s *Store) DeleteBot(id int64) ([]int64, error) {
	chans := s.ChannelsWithBot(id)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE chan_bot SET bot_id = 0 WHERE bot_id = ?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM bots WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, ch := range chans {
		cb := s.bs.chans[ch]
		cb.BotID = 0
		s.bs.chans[ch] = cb
	}
	delete(s.bs.bots, id)
	return chans, nil
}

// ChannelsWithBot lists the IDs of the channels bot id is assigned to.
func (s *Store) ChannelsWithBot(id int64) []int64 {
	var out []int64
	for ch, cb := range s.bs.chans {
		if cb.BotID == id {
			out = append(out, ch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// BotAssignment is a registered channel with a bot.
type BotAssignment struct {
	ChannelID int64
	Name      string
	BotID     int64
}

// BotAssignments lists the channels that have a bot, by name.
func (s *Store) BotAssignments() []BotAssignment {
	var out []BotAssignment
	for id, cb := range s.bs.chans {
		if cb.BotID == 0 {
			continue
		}
		if ch := s.c.channels[id]; ch != nil {
			out = append(out, BotAssignment{id, ch.Name, cb.BotID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Name) < irc.Fold(out[j].Name) })
	return out
}

// ChanBot is the BotServ settings of channel c.
func (s *Store) ChanBot(c *Channel) ChanBot {
	if cb, ok := s.bs.chans[c.ID]; ok {
		return cb
	}
	return DefaultChanBot()
}

// SetChanBot saves channel c's BotServ settings.
func (s *Store) SetChanBot(c *Channel, cb ChanBot) error {
	if _, err := s.db.Exec(`INSERT INTO chan_bot (channel_id, bot_id, greet, fantasy, nobot, dontkickops, dontkickvoices, ban_expire)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(channel_id) DO UPDATE SET bot_id = excluded.bot_id, greet = excluded.greet,
		fantasy = excluded.fantasy, nobot = excluded.nobot, dontkickops = excluded.dontkickops,
		dontkickvoices = excluded.dontkickvoices, ban_expire = excluded.ban_expire`,
		c.ID, cb.BotID, b2i(cb.Greet), b2i(cb.Fantasy), b2i(cb.NoBot), b2i(cb.DontKickOps), b2i(cb.DontKickVoices), int64(cb.BanExpire/time.Second)); err != nil {
		return err
	}
	s.bs.chans[c.ID] = cb
	return nil
}

// Greet is account id's NickServ greet ("" = none).
func (s *Store) Greet(id int64) string { return s.bs.greets[id] }

// SetGreet sets or ("" ) clears account id's greet.
func (s *Store) SetGreet(id int64, greet string) error {
	if greet == "" {
		if _, err := s.db.Exec(`DELETE FROM acct_greet WHERE account_id = ?`, id); err != nil {
			return err
		}
		delete(s.bs.greets, id)
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO acct_greet (account_id, greet) VALUES (?, ?) ON CONFLICT(account_id) DO UPDATE SET greet = excluded.greet`, id, greet); err != nil {
		return err
	}
	s.bs.greets[id] = greet
	return nil
}

// bsDropChannel forgets a channel's settings (the row went with it).
func (s *Store) bsDropChannel(id int64) { delete(s.bs.chans, id) }

// bsDropAccount forgets an account's greet (the row went with it).
func (s *Store) bsDropAccount(id int64) { delete(s.bs.greets, id) }
