package services

import (
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// This file is what the server calls.

// Start creates the service bots. AdminServ is added by the server with
// AddBot.
func (s *Services) Start() { s.startServices() }

// BotCommand is one command of a bot the server adds (AdminServ).
type BotCommand struct {
	Syntax string // shown in HELP, e.g. "SET <setting> <value>"
	Short  string // one line in the HELP list
	Long   string // HELP <command> text; lines split on \n
	Run    func(uid string, args []string)
}

// AddBot adds a service bot whose commands the server runs. need is 'o'
// (opers only), an oper flag letter, or 0 for everyone.
func (s *Services) AddBot(nick, alias, realname, intro string, need byte, cmds map[string]BotCommand) *state.User {
	inner := make(map[string]svcCmd, len(cmds))
	for name, bc := range cmds {
		run := bc.Run
		inner[name] = svcCmd{bc.Syntax, bc.Short, bc.Long, func(_ *Services, c *Client, args []string) { run(c.user.UID, args) }}
	}
	sv := s.addService(nick, alias, realname, intro, inner)
	sv.need = need
	sort.Strings(sv.order)
	return sv.user
}

// IsBot reports whether u is one of the service bots.
func (s *Services) IsBot(u *state.User) bool { return s.serviceFor(u) != nil }

// Message runs a line uid sent to bot (by /msg or an alias like /ns).
func (s *Services) Message(uid string, bot *state.User, text string) {
	c, sv := s.clients[uid], s.serviceFor(bot)
	if c == nil || sv == nil {
		return
	}
	s.toService(c, sv, text)
	s.Flush()
}

// MessageAlias runs /ns, /cs and the like: alias is "ns", "cs", ...
func (s *Services) MessageAlias(uid, alias, text string) {
	for _, sv := range s.services {
		if sv.alias == alias {
			s.Message(uid, sv.user, text)
			return
		}
	}
}

// Reply sends a notice from bot to uid.
func (s *Services) Reply(bot *state.User, uid, text string) {
	s.host.Notice(bot.UID, uid, text)
}

// Audit notes that uid changed something through bot, for the log and
// opers with +s.
func (s *Services) Audit(uid string, bot *state.User, text string) {
	nick := uid
	if u := s.net.UserByUID(uid); u != nil {
		nick = u.Nick
	}
	s.snotice("%s used %s: %s", nick, bot.Nick, text)
}

// Login identifies uid to account a (after SASL, for example).
func (s *Services) Login(uid string, a *store.Account) {
	if c := s.clients[uid]; c != nil {
		s.login(c, a)
		s.Flush()
	}
}

// LockedFor reports how long key (see CheckSecret) is still locked.
func (s *Services) LockedFor(key int64) time.Duration { return s.lockedFor(key) }

// RoundUp says a lockout time in whole minutes.
func RoundUp(d time.Duration) string { return roundUp(d) }

// IsReserved reports whether nick belongs to a service.
func IsReserved(nick string) bool { return isReserved(nick) }

// JoinLevel is uid's ChanServ level on channel before joining (0 when the
// channel isn't registered). UOP and up get past +i and +k.
func (s *Services) JoinLevel(uid, channel string) int {
	if c := s.clients[uid]; c != nil {
		return s.csJoinLevel(c, channel)
	}
	return store.LevelNone
}

// TopicLocked reports whether ChanServ TOPICLOCK stops uid changing the
// topic of ch.
func (s *Services) TopicLocked(uid string, ch *state.Channel) bool {
	if c := s.clients[uid]; c != nil {
		return s.csTopicLocked(c, ch)
	}
	return false
}

// SendNews shows the news lines (after the MOTD).
func (s *Services) SendNews(uid string) {
	for _, n := range s.news {
		s.host.Notice(s.global.user.UID, uid, "[News "+n.SetAt.UTC().Format("2 Jan")+"] "+n.Text)
	}
}

// Forbidden returns the forbid on a nick or channel (kind store.ForbidNick
// or store.ForbidChan).
func (s *Services) Forbidden(kind, name string) (store.Forbid, bool) { return s.forbidden(kind, name) }

// AkillFor returns the AKILL matching ident at any of hosts, or nil.
func (s *Services) AkillFor(ident string, hosts ...string) *store.Akill {
	return s.akillFor(ident, hosts...)
}

// SessionLimit is how many connections ip may have.
func (s *Services) SessionLimit(ip string) int { return s.sessionLimit(ip) }

// LiveAkills counts AKILLs in force, for STATS.
func (s *Services) LiveAkills() int { return len(s.liveAkills()) }

// ArgAt returns args[i], or "".
func ArgAt(args []string, i int) string { return argAt(args, i) }

// OnOff reads ON or OFF.
func OnOff(v string) (bool, bool) { return onOff(v) }

// Plural is "1 nick" or "2 nicks".
func Plural(n int, word string) string { return plural(n, word) }

// RandomCode is a short code people retype to confirm something.
func RandomCode() string { return randomCode() }

// ClearLockout forgets wrong passwords for key.
func (s *Services) ClearLockout(key int64) { delete(s.lockouts, key) }
