package services

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Chanstats (Anope's m_chanstats, cs_fantasy_stats and cs_fantasy_top,
// kept in our own database): in channels with ChanServ SET CHANSTATS on,
// what people with NickServ SET CHANSTATS on say and do is counted. STATS,
// GSTATS, TOP, TOP10, GTOP and GTOP10 (and !stats, !top ... through the
// channel's bot) show the counts.

// The smileys Anope's chanstats counts by default (chanstats.example.conf).
var (
	smileysHappy = []string{":)", ":-)", ";)", ";-)", ":D", ":-D", ":P", ":-P"}
	smileysSad   = []string{":(", ":-(", ";(", ";-("}
	smileysOther = []string{":/", ":-/"}
)

// countSmileys counts every place one of the smileys starts in text.
func countSmileys(text string, list []string) int64 {
	var n int64
	for _, sm := range list {
		for i := 0; ; {
			j := strings.Index(text[i:], sm)
			if j < 0 {
				break
			}
			n++
			i += j + 1
		}
	}
	return n
}

// statsWho is the account what uid does is counted for: identified, not a
// service, with NickServ SET CHANSTATS on. 0 when it isn't counted.
func (s *Services) statsWho(uid string) int64 {
	c := s.clients[uid]
	if c == nil || c.closed || c.user == nil || c.user.Service || c.accountID == 0 {
		return 0
	}
	if !s.store.AccountChanStats(c.accountID) {
		return 0
	}
	return c.accountID
}

// statsText counts a PRIVMSG uid sent to ch (ChannelText).
func (s *Services) statsText(uid string, ch *state.Channel, text string) {
	if s.off || ch == nil {
		return
	}
	id := s.store.ChanStatsChannel(ch.Name)
	if id == 0 {
		return
	}
	acct := s.statsWho(uid)
	if acct == 0 {
		return
	}
	action := false
	if t := actionText(text); t != text {
		text, action = t, true
	} else if strings.HasPrefix(text, "\x01") {
		return // a CTCP, not something said
	}
	if !action && s.fantasyLine(ch.Name, text) {
		return
	}
	text = normalizeBuffer(text)
	d := store.ChanStats{Lines: 1, Letters: int64(utf8.RuneCountInString(text))}
	d.Happy = countSmileys(text, smileysHappy)
	d.Sad = countSmileys(text, smileysSad)
	d.Other = countSmileys(text, smileysOther)
	// Smileys are not words (Anope).
	d.Words = max(int64(len(strings.Fields(text)))-d.Smileys(), 0)
	if action {
		d.Actions = 1
	}
	s.store.AddChanStats(id, acct, d, s.now())
}

// fantasyLine says whether text is for the channel's bot (a fantasy
// command, see bsFantasy): those lines aren't counted.
func (s *Services) fantasyLine(name, text string) bool {
	reg, err := s.store.ChannelByName(name)
	if err != nil || reg == nil {
		return false
	}
	bot, cb := s.chanBot(reg)
	if bot == nil || !cb.Fantasy {
		return false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	p0 := normalizeBuffer(fields[0])
	return strings.HasPrefix(p0, fantasyChar) || irc.EqualFold(strings.TrimRight(p0, ":,"), bot.user.Nick)
}

// statsEvent counts kicks, mode changes and topics in channels with
// CHANSTATS on.
func (s *Services) statsEvent(ev state.Event) {
	add := func(channel, uid string, d store.ChanStats) {
		if id := s.store.ChanStatsChannel(channel); id != 0 {
			if acct := s.statsWho(uid); acct != 0 {
				s.store.AddChanStats(id, acct, d, s.now())
			}
		}
	}
	switch e := ev.(type) {
	case *state.Kick:
		add(e.Channel, e.UID, store.ChanStats{Kicked: 1})
		add(e.Channel, e.Source, store.ChanStats{Kicks: 1})
	case *state.ChannelMode:
		if len(e.Changes) > 0 {
			add(e.Channel, e.Source, store.ChanStats{Modes: int64(len(e.Changes))})
		}
	case *state.Topic:
		add(e.Channel, e.Source, store.ChanStats{Topics: 1})
	}
}

// SaveChanStats writes the changed chanstats counts to the database (every
// minute, and at shutdown).
func (s *Services) SaveChanStats() {
	s.statsSaved = time.Now()
	if s.readonly { // READONLY: nothing is saved; it goes out once it is off
		return
	}
	if err := s.store.SaveChanStats(); err != nil {
		s.log.Printf("services database (chanstats): %v", err)
	}
}

// statsTarget is the account STATS and GSTATS show: the nick's, or c's own.
func (s *Services) statsTarget(c *Client, nick string) *store.Account {
	if nick == "" {
		if c.accountID == 0 {
			s.cs(c, "Identify first: /ns identify <password>")
			return nil
		}
		a, _ := s.store.AccountByID(c.accountID)
		return a
	}
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return nil
	}
	if a == nil {
		s.cs(c, "%s isn't registered.", nick)
	}
	return a
}

// statsChannel is the channel STATS and TOP show, if c may see it.
func (s *Services) statsChannel(c *Client, name string) *store.Channel {
	reg, lvl, ok := s.csLookup(c, name)
	if !ok {
		return nil
	}
	// A private channel's counts are for its own people; a fantasy command
	// comes from inside the channel.
	if reg.Private && lvl.level == store.LevelNone && !c.isOper() && s.fantasyBot == nil {
		s.cs(c, "%s is private.", reg.Name)
		return nil
	}
	if !reg.ChanStats {
		s.cs(c, "Chanstats is off for %s.", reg.Name)
		return nil
	}
	return reg
}

// statsShow prints an account's counts for each period.
func (s *Services) statsShow(c *Client, p store.ChanStatsPeriods) {
	var labels [len(p)]string
	labels[store.StatsTotal] = s.tr(c, "All time")
	labels[store.StatsMonth] = s.tr(c, "This month")
	labels[store.StatsWeek] = s.tr(c, "This week")
	labels[store.StatsDay] = s.tr(c, "Today")
	for i, v := range p {
		s.cs(c, "  %-11s letters: %d, words: %d, lines: %d, smileys: %d, actions: %d",
			labels[i], v.Letters, v.Words, v.Lines, v.Smileys(), v.Actions)
	}
	v := p[store.StatsTotal]
	s.cs(c, "  Kicks given: %d, kicked: %d, modes set: %d, topics set: %d", v.Kicks, v.Kicked, v.Modes, v.Topics)
}

func csStats(s *Services, c *Client, args []string) {
	reg := s.statsChannel(c, argAt(args, 0))
	if reg == nil {
		return
	}
	a := s.statsTarget(c, argAt(args, 1))
	if a == nil {
		return
	}
	p, ok := s.store.ChanStatsOf(reg.ID, a.ID, s.now())
	if !ok {
		s.cs(c, "No stats for %s.", a.Name)
		return
	}
	s.cs(c, "Channel stats for %s on %s:", a.Name, reg.Name)
	s.statsShow(c, p)
}

func csGStats(s *Services, c *Client, args []string) {
	a := s.statsTarget(c, argAt(args, 0))
	if a == nil {
		return
	}
	p, ok := s.store.ChanStatsOf(0, a.ID, s.now())
	if !ok {
		s.cs(c, "No stats for %s.", a.Name)
		return
	}
	s.cs(c, "Network stats for %s:", a.Name)
	s.statsShow(c, p)
}

// statsTop prints the top n of a channel (0: network-wide), named name.
func (s *Services) statsTop(c *Client, channelID int64, name string, n int) {
	top := s.store.TopChanStats(channelID, n)
	if len(top) == 0 {
		s.cs(c, "No stats for %s.", name)
		return
	}
	s.cs(c, "Top %d of %s", n, name)
	for i, e := range top {
		v := e.Stats
		s.cs(c, "%2d \x02%-16s\x02 letters: %d, words: %d, lines: %d, smileys: %d, actions: %d",
			i+1, e.Name, v.Letters, v.Words, v.Lines, v.Smileys(), v.Actions)
	}
}

func csTopN(n int) func(*Services, *Client, []string) {
	return func(s *Services, c *Client, args []string) {
		if reg := s.statsChannel(c, argAt(args, 0)); reg != nil {
			s.statsTop(c, reg.ID, reg.Name, n)
		}
	}
}

func csGTopN(n int) func(*Services, *Client, []string) {
	return func(s *Services, c *Client, args []string) {
		s.statsTop(c, 0, s.cfg.Server.Network, n)
	}
}
