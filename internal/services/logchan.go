package services

import (
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// The log channel ([services] log_channel, like Anope's log { target =
// "#services" }): every service bot sits in it with op, and each posts a
// line for every command it runs, with its first reply, so opers can see
// what was used and whether it worked. Oper events (opers up, kills, ban
// hits, things that ran out) come from OperServ; connects and quits too
// when log_connects is on. Passwords and confirm codes are never shown.

// logEntry is a command waiting for its first reply.
type logEntry struct {
	sv   *service
	text string // "nick!ident@host used CMD args"
	hide bool   // the reply holds a secret (a key, a confirm code)
}

// logReplyMax is how much of a reply a log line shows.
const logReplyMax = 200

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

// LogEvent posts an oper event (what goes to +s opers) as OperServ.
// Audit lines are skipped: the command log already shows that command.
func (s *Services) LogEvent(text string) {
	if s.auditing || s.logChannel() == "" {
		return
	}
	s.logTo(s.operServ, "OPER: "+text)
}

// userMask is Anope's "nick!ident@realhost (shown host)" for log lines.
func userMask(u *state.User) string {
	m := u.Nick + "!" + u.Ident + "@" + u.RealHost
	if u.Host != "" && u.Host != u.RealHost {
		m += " (" + u.Host + ")"
	}
	return m
}

// LogUserConnect posts Anope's USERS line for a connect (log_connects).
func (s *Services) LogUserConnect(u *state.User) {
	if !s.cfg.Services.LogConnects || s.logChannel() == "" {
		return
	}
	s.logTo(s.global, "USERS: "+userMask(u)+" ("+u.Realname+") ["+irc.DisplayIP(u.IP)+"] connected to the network ("+u.Server+")")
}

// LogUserQuit posts Anope's USERS line for a quit (log_connects).
func (s *Services) LogUserQuit(u *state.User, reason string) {
	if !s.cfg.Services.LogConnects || s.logChannel() == "" {
		return
	}
	s.logTo(s.global, "USERS: "+userMask(u)+" ("+u.Realname+") disconnected from the network ("+u.Server+") ("+reason+")")
}

// logNick posts Anope's USERS line for a nick change (log_connects).
func (s *Services) logNick(e *state.NickChange) {
	if !s.cfg.Services.LogConnects || s.logChannel() == "" || e.Old == "" {
		return
	}
	u := s.net.UserByUID(e.UID)
	if u == nil || u.Service {
		return
	}
	old, _, _ := strings.Cut(e.Old, "!") // Old is the full old mask
	s.logTo(s.global, "USERS: "+old+"!"+u.Ident+"@"+u.RealHost+" ("+u.Realname+") changed nick to "+e.Nick)
}

// LogSASL posts Anope's M_SASL line as NickServ. why is "" on success.
func (s *Services) LogSASL(host, ip, account, mech, why string) {
	if s.logChannel() == "" {
		return
	}
	who := host + " (" + irc.DisplayIP(ip) + ")"
	if why == "" {
		s.logTo(s.nickServ, "M_SASL: "+who+" identified to account "+account+" using SASL "+mech)
		return
	}
	s.logTo(s.nickServ, "M_SASL: "+who+" failed to identify for account "+account+" using SASL "+mech+" ("+why+")")
}

// logStart remembers a command c sent to sv; its first reply finishes
// the line.
func (s *Services) logStart(c *Client, sv *service, name string, args []string) {
	c.logPending = nil
	if s.logChannel() == "" || name == "HELP" || sv == s.global {
		return
	}
	shown, hide := logArgs(sv, name, args)
	u := c.user
	kind := "COMMAND: "
	if sv.need != 0 || sv == s.operServ {
		kind = "ADMIN: " // OperServ and AdminServ, as Anope
	}
	who := u.Nick + "!" + u.Ident + "@" + u.RealHost
	if u.Account != "" {
		who += " (" + u.Account + ")" // Anope shows the account too
	}
	text := kind + who + " used " + strings.TrimSpace(name+" "+strings.Join(shown, " "))
	c.logPending = &logEntry{sv: sv, text: text, hide: hide}
}

// logReply finishes c's pending line with reply.
func (s *Services) logReply(c *Client, reply string) {
	e := c.logPending
	if e == nil {
		return
	}
	c.logPending = nil
	switch {
	case e.hide:
		reply = "(reply hidden)"
	case reply == "":
		reply = "(no reply)"
	}
	s.logTo(e.sv, e.text+" → "+irc.Truncate(reply, logReplyMax))
}

// logDone runs after a command: a command that said nothing is logged
// now, unless a password check will answer later.
func (s *Services) logDone(c *Client) {
	if c.logPending != nil && !c.pwBusy {
		s.logReply(c, "")
	}
}

// logArgs hides passwords and confirm codes in a command's arguments, and
// says whether its reply has a secret in it.
func logArgs(sv *service, name string, args []string) (shown []string, hideReply bool) {
	shown = append([]string(nil), args...)
	mask := func(i int) {
		if i < len(shown) {
			shown[i] = "****"
		}
	}
	switch sv.user.Nick + " " + name {
	case "NickServ IDENTIFY":
		mask(len(shown) - 1)
	case "NickServ REGISTER":
		mask(0)
	case "NickServ GROUP", "NickServ RECOVER":
		mask(1)
	case "NickServ DROP", "ChanServ DROP":
		mask(1)
		hideReply = len(args) < 2 // without a code, the reply holds the code
	case "ChanServ GETKEY":
		hideReply = true
	case "AdminServ RESTART", "AdminServ DIE":
		hideReply = true
	}
	// SET PASSWORD x, SASET PASSWORD nick x, AS OPER ... PASSWORD x
	for i, a := range shown {
		if strings.EqualFold(a, "PASSWORD") {
			for j := i + 1; j < len(shown); j++ {
				if name == "SASET" && j == i+1 {
					continue // the nick
				}
				mask(j)
			}
			break
		}
	}
	return shown, hideReply
}
