package services

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// This file is what the server calls.

// Start creates the service bots. AdminServ is added by the server with
// AddBot.
func (s *Services) Start() {
	s.startServices()
	if s.off {
		return
	}
	s.startBots()
	s.startPersist()
	s.Flush()
}

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

// Off reports whether services are switched off here ([services]
// enabled = false): another server runs them.
func (s *Services) Off() bool { return s.off }

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

// LoginSASL is Login for SASL after registration: 903 follows 900 straight
// away, before the +r mode and what login brings (IRCv3 sasl).
func (s *Services) LoginSASL(uid string, a *store.Account) {
	if c := s.clients[uid]; c != nil {
		c.saslLogin = true
		s.login(c, a)
		c.saslLogin = false
		s.Flush()
	}
}

// LoginsFull reports whether account id already has max_logins
// connections identified to it (SASL checks it like IDENTIFY does).
func (s *Services) LoginsFull(id int64) bool {
	max := s.cfg.Services.MaxLogins
	return max > 0 && len(s.onlineClients(id)) >= max
}

// LockedFor reports how long key (see CheckSecret) is still locked.
func (s *Services) LockedFor(key int64, src string) time.Duration { return s.lockedFor(key, src) }

// ForgiveWrong takes back the wrong try just counted for key from src: it
// turned out to be the right password for something else (the Hive Panel
// tries the oper password, then the NickServ one). A lock that try set is
// lifted again.
func (s *Services) ForgiveWrong(key int64, src string) {
	l := s.lockouts[lockKey{key, src}]
	switch {
	case l == nil:
	case l.fails > 0:
		l.fails--
	case time.Now().Before(l.until):
		l.until, l.fails = time.Time{}, maxWrongPasswords-1
	}
}

// RoundUp says a lockout time in whole minutes.
func RoundUp(d time.Duration) string { return roundUp(d) }

// IsReserved reports whether nick belongs to a service.
func IsReserved(nick string) bool { return isReserved(nick) }

// JoinLevel is uid's ChanServ level on channel before joining (0 when the
// channel isn't registered). Anyone on the access list gets past +i and +k.
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
	s.showNews(uid, s.global, store.NewsLogon, "Logon News")
	s.showNews(uid, s.global, store.NewsRandom, "Random News")
}

// SendOperNews sends the oper news to someone who just opered up.
func (s *Services) SendOperNews(uid string) {
	s.showNews(uid, s.operServ, store.NewsOper, "Oper News")
}

// Forbidden returns the forbid on a nick or channel (kind store.ForbidNick
// or store.ForbidChan).
func (s *Services) Forbidden(kind, name string) (store.Forbid, bool) { return s.forbidden(kind, name) }

// ConnectBan returns the network ban (ZLINE, AKILL or SNLINE) that
// refuses someone registering with this ident, real host, IP and
// realname, or nil. ELINEs exempt.
func (s *Services) ConnectBan(ident, host, ip, realname string) *store.NetBan {
	return s.connectBan(ident, host, ip, realname)
}

// ZlineFor returns the ZLINE refusing a new connection from ip, or nil.
func (s *Services) ZlineFor(ip string) *store.NetBan {
	if s.exempt("", "", ip) {
		return nil
	}
	return s.firstBan(store.BanZline, "", "", ip, "")
}

// Shunned reports whether a SHUN covers uid.
func (s *Services) Shunned(u *state.User) bool { return len(s.netbans) > 0 && s.shunned(u) }

// BanCounts is how many bans of each kind are in force, for STATS.
func (s *Services) BanCounts() map[string]int {
	out := make(map[string]int)
	for _, kind := range store.BanKinds {
		out[kind] = len(s.liveBans(kind))
	}
	return out
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
func (s *Services) ClearLockout(key int64) { s.clearLockouts(key) }

// ReloadBans rereads network bans, forbids, news and session exceptions
// from the store (after something outside services changed them).
func (s *Services) ReloadBans() { s.reloadOperData() }

// DnsblExempt reports whether ip skips the DNSBL check: an ELINE or a
// session exception covers it.
func (s *Services) DnsblExempt(ip string) bool {
	if s.exempt("", "", ip) {
		return true
	}
	now := time.Now()
	for _, e := range s.allExcepts() {
		if (e.Expires.IsZero() || now.Before(e.Expires)) && irc.MatchHost(e.Mask, ip) {
			return true
		}
	}
	return false
}

// DnsblBan adds the network ban a DNSBL list asks for (kind is
// store.BanZline, BanAkill or BanShun) on ip, set by "DNSBL", unless one
// is there already.
func (s *Services) DnsblBan(kind, ip, reason string, life time.Duration) {
	s.autoBan(kind, ip, reason, "DNSBL", life)
}

// ThrottleBan ZLINEs an IP the connect throttle refused again and again.
func (s *Services) ThrottleBan(ip, reason string, life time.Duration) {
	s.autoBan(store.BanZline, ip, reason, "Throttle", life)
}

func (s *Services) autoBan(kind, ip, reason, setBy string, life time.Duration) {
	mask := ip
	if kind != store.BanZline {
		mask = "*@" + ip
	}
	mask, problem := s.netbanMask(kind, mask)
	if problem != "" {
		return
	}
	for _, b := range s.liveBans(kind) {
		if strings.EqualFold(b.Mask, mask) {
			return
		}
	}
	now := time.Now()
	b := store.NetBan{Kind: kind, Mask: mask, Reason: irc.Truncate(reason, 200), SetBy: setBy, SetAt: now, Expires: now.Add(life)}
	if err := s.store.AddNetBan(b); err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	s.reloadOperData()
}

// LogThrottle posts a connect-throttle ZLINE to the log channel, from
// OperServ, so it shows next to the DNSBL lines.
func (s *Services) LogThrottle(ip string, refusals int, window, life time.Duration) {
	s.logTo(s.operServ, fmt.Sprintf("THROTTLE: Z-lined %s for %d minutes: refused %d times in %d minutes for reconnecting too fast",
		ip, int(life/time.Minute), refusals, int(window/time.Minute)))
}

// LogDNSBL posts Anope's m_dnsbl line to the log channel, from OperServ:
// "M_DNSBL: nick!ident@host (ip) appears in list".
func (s *Services) LogDNSBL(mask, ip, list string) {
	s.logTo(s.operServ, "M_DNSBL: "+mask+" ("+ip+") appears in "+list)
}

// BotNicks lists the service bots in the order people know them.
func (s *Services) BotNicks() []string {
	order := []string{"NickServ", "ChanServ", "MemoServ", "OperServ", "HostServ", "AdminServ"}
	var out []string
	seen := map[string]bool{}
	for _, n := range order {
		if s.services[irc.Fold(n)] != nil {
			out = append(out, n)
			seen[irc.Fold(n)] = true
		}
	}
	var rest []string
	for k, sv := range s.services {
		if !seen[k] {
			rest = append(rest, sv.user.Nick)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}
