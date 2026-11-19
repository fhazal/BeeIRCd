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

// The log channel ([services] log_channel), exactly like Anope 2.0.20's
// log { target = "#services"; admin = "*"; override = ...; commands = "*";
// users = "connect disconnect nick"; other = "*" }: every service bot sits
// in it with op. Lines are Anope's (src/logger.cpp):
//
//	COMMAND|ADMIN|OVERRIDE: nick!ident@realhost (account) used NAME [on #chan] text
//
// sent by the command's service, only where Anope's command code logs
// (mostly when it worked); USERS: connect/disconnect/nick lines from Global;
// module lines like M_SASL:; and plain lines (access denied, expiry). The
// text never holds a password.

// logChannel is the log channel, "" when there is none.
func (s *Services) logChannel() string { return s.cfg.Services.LogChannel }

// IsLogChannel reports whether name is the log channel.
func (s *Services) IsLogChannel(name string) bool {
	lc := s.logChannel()
	return lc != "" && irc.Fold(lc) == irc.Fold(name)
}

// logBots are the service bots in nick order.
func (s *Services) logBots() []*service {
	bots := make([]*service, 0, len(s.services))
	for _, sv := range s.services {
		bots = append(bots, sv)
	}
	sort.Slice(bots, func(i, j int) bool { return bots[i].user.Nick < bots[j].user.Nick })
	return bots
}

// SyncLogChannel puts every bot in the log channel with op, and takes
// them out of old (the log channel before a change). People who aren't
// opers are kicked: the lines show real hosts.
func (s *Services) SyncLogChannel(old string) {
	if s.off {
		return
	}
	defer s.Flush()
	if old != "" && !s.IsLogChannel(old) {
		if ch := s.net.Channel(old); ch != nil {
			for _, sv := range s.logBots() {
				if ch.Member(sv.user) != nil {
					s.emit(&state.Part{UID: sv.user.UID, Channel: ch.Name, Reason: "No longer the log channel"})
				}
			}
		}
	}
	name := s.logChannel()
	if name == "" {
		return
	}
	for _, sv := range s.logBots() {
		ch := s.net.Channel(name)
		if ch != nil && ch.Member(sv.user) != nil {
			continue
		}
		ev := &state.Join{UID: sv.user.UID, Channel: name, TS: time.Now().Unix()}
		if s.emit(ev) != nil {
			continue
		}
		if ev.Created {
			if ch := s.net.Channel(name); ch != nil {
				s.csMode(ch, []modeChange{{true, 'n', ""}, {true, 's', ""}, {true, 't', ""}})
			}
		}
	}
	ch := s.net.Channel(name)
	if ch == nil {
		return
	}
	if !ch.HasMode('O') {
		s.csMode(ch, []modeChange{{true, 'O', ""}})
	}
	var ops []modeChange
	for _, sv := range s.logBots() {
		if m := ch.Member(sv.user); m != nil && !m.Has(state.RankOp) {
			ops = append(ops, modeChange{true, 'o', sv.user.Nick})
		}
	}
	if len(ops) > 0 {
		s.csMode(ch, ops)
	}
	for _, m := range ch.Members {
		if m.User.Service || s.isOperUser(m.User) {
			continue
		}
		s.svcKick(s.chanServ, ch, m.User, "This channel is for IRC operators (+O)")
	}
}

// logModes re-ops bots someone deopped in the log channel.
func (s *Services) logModes(e *state.ChannelMode) {
	if !s.IsLogChannel(e.Channel) {
		return
	}
	for _, m := range e.Changes {
		if !m.Add && m.Mode == 'o' {
			if u := s.net.UserByNick(m.Arg); u != nil && s.serviceFor(u) != nil {
				s.SyncLogChannel("")
				return
			}
		}
	}
}

// logTo posts text to the log channel as sv.
func (s *Services) logTo(sv *service, text string) {
	name := s.logChannel()
	if name == "" || sv == nil {
		return
	}
	ch := s.net.Channel(name)
	if ch == nil || ch.Member(sv.user) == nil {
		return
	}
	s.host.ChannelMessage(sv.user.UID, ch.Name, 0, true, text)
}

// anopeMask is Anope's User::GetMask(): nick!ident@realhost.
func anopeMask(u *state.User) string { return u.Nick + "!" + u.TrueIdent() + "@" + u.RealHost }

// logUsersOn reports whether USERS lines go to the log channel.
func (s *Services) logUsersOn() bool {
	return s.logChannel() != "" && s.cfg.Services.LogConnectsOn()
}

// LogUserConnect posts Anope's connect line (users.cpp).
func (s *Services) LogUserConnect(u *state.User) {
	if !s.logUsersOn() || u.Service {
		return
	}
	text := "USERS: " + anopeMask(u) + " "
	if u.Host != "" && u.Host != u.RealHost {
		text += "(" + u.Host + ") "
	}
	text += "(" + u.Realname + ") "
	if ip := irc.DisplayIP(u.IP); ip != "" && ip != u.RealHost {
		text += "[" + ip + "] "
	}
	s.logTo(s.global, text+"connected to the network ("+u.Server+")")
}

// LogUserQuit posts Anope's disconnect line (users.cpp).
func (s *Services) LogUserQuit(u *state.User, _ string) {
	if !s.logUsersOn() || u.Service {
		return
	}
	s.logTo(s.global, "USERS: "+anopeMask(u)+" ("+u.Realname+") disconnected from the network ("+u.Server+")")
}

// logNick posts Anope's nick change line (users.cpp).
func (s *Services) logNick(e *state.NickChange) {
	if !s.logUsersOn() || e.Old == "" {
		return
	}
	u := s.net.UserByUID(e.UID)
	if u == nil || u.Service {
		return
	}
	old, _, _ := strings.Cut(e.Old, "!") // Old is the full old mask
	s.logTo(s.global, "USERS: "+old+"!"+u.TrueIdent()+"@"+u.RealHost+" ("+u.Realname+") changed nick to "+e.Nick)
}

// LogSASLExternal posts m_sasl's line for a certificate login (Anope
// 2.0.20 logs SASL only for EXTERNAL). who is "host (ip)".
func (s *Services) LogSASLExternal(who, account, cert string, ok bool) {
	if ok {
		s.logTo(s.nickServ, "M_SASL: "+who+" identified to account "+account+" using SASL EXTERNAL")
		return
	}
	s.logTo(s.nickServ, "M_SASL: "+who+" failed to identify using certificate "+cert+" using SASL EXTERNAL")
}

// Log kinds of a command line.
const (
	logCommand  = "COMMAND"
	logAdmin    = "ADMIN"
	logOverride = "OVERRIDE"
)

// logCmd posts Anope's command line: KIND: mask (account) used NAME [on
// #chan] text, from sv.
func (s *Services) logCmd(c *Client, sv *service, kind, channel, text string) {
	if c != nil {
		c.logged = true
	}
	if s.logChannel() == "" || c == nil || c.user == nil {
		return
	}
	line := kind + ": " + anopeMask(c.user)
	if c.user.Account != "" {
		line += " (" + c.user.Account + ")"
	}
	name := c.cmdName
	if name == "" {
		name = "?"
	}
	line += " used " + name
	if channel != "" {
		line += " on " + channel
	}
	if text != "" {
		line += " " + text
	}
	s.logTo(sv, line)
}

// nsLog logs a NickServ command on account id: COMMAND when it is c's own
// account, ADMIN when it is someone else's (Anope's nc == source.nc test).
func (s *Services) nsLog(c *Client, id int64, format string, a ...any) {
	kind := logCommand
	if id != c.accountID {
		kind = logAdmin
	}
	s.logCmd(c, s.nickServ, kind, "", fmt.Sprintf(format, a...))
}

// nsLogCmd logs a NickServ command as COMMAND.
func (s *Services) nsLogCmd(c *Client, format string, a ...any) {
	s.logCmd(c, s.nickServ, logCommand, "", fmt.Sprintf(format, a...))
}

// csLog logs a ChanServ command on channel; override is an oper acting
// without channel access.
func (s *Services) csLog(c *Client, channel string, override bool, format string, a ...any) {
	kind := logCommand
	if override {
		kind = logOverride
	}
	s.logCmd(c, s.chanServ, kind, channel, fmt.Sprintf(format, a...))
}

// csLogAdmin logs an oper-only ChanServ command (SUSPEND, NOEXPIRE).
func (s *Services) csLogAdmin(c *Client, channel, format string, a ...any) {
	s.logCmd(c, s.chanServ, logAdmin, channel, fmt.Sprintf(format, a...))
}

// msLog logs a MemoServ command; channel is "" for a nick's box.
func (s *Services) msLog(c *Client, channel, format string, a ...any) {
	s.logCmd(c, s.memoServ, logCommand, channel, fmt.Sprintf(format, a...))
}

// osLog logs an OperServ command (always ADMIN in Anope).
func (s *Services) osLog(c *Client, format string, a ...any) {
	s.logCmd(c, s.operServ, logAdmin, "", fmt.Sprintf(format, a...))
}

// LogAdmin logs a command of a bot the server runs (AdminServ) as ADMIN.
func (s *Services) LogAdmin(uid string, bot *state.User, text string) {
	c, sv := s.clients[uid], s.serviceFor(bot)
	if c == nil || sv == nil {
		return
	}
	s.logCmd(c, sv, logAdmin, "", text)
}

// logDenied posts Anope's "Access denied" line (command.cpp) from sv.
func (s *Services) logDenied(c *Client, sv *service) {
	if c == nil || c.user == nil {
		return
	}
	s.logTo(sv, "Access denied for user "+anopeMask(c.user)+" with command "+c.cmdName)
}

// logDeniedUnreg posts Anope's line for an unidentified user using a
// command that needs an account.
func (s *Services) logDeniedUnreg(c *Client, sv *service) {
	if c == nil || c.user == nil {
		return
	}
	s.logTo(sv, "Access denied for unregistered user "+anopeMask(c.user)+" with command "+c.cmdName)
}

// expiredNotice is InspIRCd's server notice for a ban that ran out:
// "Removing an expired G-line on mask (set by nick 2 hours ago): reason".
// Forbids and session exceptions read the same way.
func expiredNotice(e store.Expired, now time.Time) string {
	word := "session exception"
	switch e.What {
	case "netban":
		word = store.BanWord(e.Kind)
	case "forbid":
		word = "FORBID " + strings.ToUpper(e.Kind)
	}
	return expiryLine(word, e.Mask, e.SetBy, e.SetAt, e.Reason, now)
}

// expiryLine is InspIRCd's XLine DisplayExpiry text.
func expiryLine(word, mask, setBy string, setAt time.Time, reason string, now time.Time) string {
	return fmt.Sprintf("Removing an expired %s on %s (set by %s %s ago): %s",
		word, mask, setBy, longDuration(now.Sub(setAt)), reason)
}

// longDuration writes a duration the way InspIRCd's
// Duration::ToLongString does (the same as the server's): every non-zero
// unit, "1 week, 2 days, 3 hours, 5 minutes".
func longDuration(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs <= 0 {
		return "0 seconds"
	}
	var parts []string
	for _, u := range []struct {
		n    int64
		name string
	}{{365 * 24 * 3600, "year"}, {7 * 24 * 3600, "week"}, {24 * 3600, "day"}, {3600, "hour"}, {60, "minute"}, {1, "second"}} {
		if n := secs / u.n; n > 0 {
			name := u.name
			if n != 1 {
				name += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, name))
			secs -= n * u.n
		}
	}
	return strings.Join(parts, ", ")
}

// anopeExpired is Anope's line for something that ran out (operserv.cpp,
// os_forbid, os_session).
func anopeExpired(e store.Expired) string {
	switch e.What {
	case "netban":
		return strings.ToUpper(e.Kind) + " on \x02" + e.Mask + "\x02 has expired"
	case "forbid":
		return "Expiring forbid for " + e.Mask + " type " + strings.ToUpper(e.Kind)
	}
	return "Session exception for " + e.Mask + " has expired."
}

// anopeDuration is Anope::Duration: "1 day, 2 hours, 5 minutes", or
// seconds when under a minute.
func anopeDuration(d time.Duration) string {
	t := int64(d / time.Second)
	years, days, hours, minutes, seconds := t/31536000, (t/86400)%365, (t/3600)%24, (t/60)%60, t%60
	unit := func(n int64, word string) string {
		if n != 1 {
			word += "s"
		}
		return fmt.Sprintf("%d %s", n, word)
	}
	if years == 0 && days == 0 && hours == 0 && minutes == 0 {
		return unit(seconds, "second")
	}
	var parts []string
	for _, p := range []struct {
		n    int64
		word string
	}{{years, "year"}, {days, "day"}, {hours, "hour"}, {minutes, "minute"}} {
		if p.n != 0 {
			parts = append(parts, unit(p.n, p.word))
		}
	}
	return strings.Join(parts, ", ")
}

// anopeTime is Anope::strftime: "Nov 12 10:00:00 2026 UTC (2 days from now)".
func anopeTime(t time.Time) string {
	s := t.UTC().Format("Jan 02 15:04:05 2006 UTC")
	switch now := time.Now(); {
	case t.Before(now):
		return s + " (" + anopeDuration(now.Sub(t)) + " ago)"
	case t.After(now):
		return s + " (" + anopeDuration(t.Sub(now)) + " from now)"
	}
	return s + " (now)"
}
