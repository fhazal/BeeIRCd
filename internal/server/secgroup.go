package server

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// Security groups (UnrealIRCd's security-group): named sets of people,
// like known-users (logged in, or a reputation score of 25). Each server
// works out the groups of everyone it sees and keeps them on the user,
// for ~G: bans, the new-user throttle, [[restrict]] and [spamfilter].

// groupDepth stops groups that name each other going round forever.
const groupDepth = 8

// inGroup reports whether u is in the group called name.
func (s *Server) inGroup(u *state.User, name string, now time.Time, depth int) bool {
	if depth > groupDepth {
		return false
	}
	if strings.EqualFold(name, config.UnknownUsers) {
		return !s.inGroup(u, "known-users", now, depth+1)
	}
	for _, g := range s.cfg.Groups() {
		if strings.EqualFold(g.Name, name) {
			return !s.groupMatch(u, g.Exclude, now, depth) && s.groupMatch(u, g.GroupMatch, now, depth)
		}
	}
	return false
}

// groupMatch reports whether any item of m covers u.
func (s *Server) groupMatch(u *state.User, m config.GroupMatch, now time.Time, depth int) bool {
	c := s.clientOf(u)
	score := s.repScore(u.IP)
	up := now.Sub(u.SignOn)
	switch {
	case m.Identified && u.Account != "",
		m.WebIRC && u.Gateway != "",
		m.WebSocket && c != nil && c.websocket,
		m.TLS && (u.TLS || u.HasMode('z')),
		m.ReputationScore > 0 && score >= m.ReputationScore,
		m.ReputationBelow > 0 && score < m.ReputationBelow,
		m.ConnectTime.Duration > 0 && up >= m.ConnectTime.Duration,
		m.ConnectBelow.Duration > 0 && up < m.ConnectBelow.Duration,
		c != nil && slices.Contains(m.ServerPort, c.port):
		return true
	}
	for _, mask := range m.Mask {
		if state.MatchUser(mask, u) {
			return true
		}
	}
	if ip := net.ParseIP(u.IP); ip != nil {
		for _, r := range m.IP {
			if n, err := config.ParseCIDR(r); err == nil && n.Contains(ip) {
				return true
			}
		}
	}
	for _, o := range m.SecurityGroup {
		if s.inGroup(u, o, now, depth+1) {
			return true
		}
	}
	return false
}

// groupsOf is every group u is in: known-users or unknown-users first.
func (s *Server) groupsOf(u *state.User, now time.Time) []string {
	if u.Service {
		return nil
	}
	known := s.inGroup(u, "known-users", now, 0)
	out := []string{config.UnknownUsers}
	if known {
		out[0] = "known-users"
	}
	for _, g := range s.cfg.Groups() {
		if !strings.EqualFold(g.Name, "known-users") && s.inGroup(u, g.Name, now, 0) {
			out = append(out, strings.ToLower(g.Name))
		}
	}
	return out
}

// refreshGroups works out u's groups again.
func (s *Server) refreshGroups(u *state.User) {
	if u != nil {
		u.Groups = s.groupsOf(u, time.Now())
	}
}

// groupTick refreshes everyone's groups once a minute (connect_time and
// reputation change as time goes by).
func (s *Server) groupTick(now time.Time) {
	if now.Sub(s.lastGroups) < time.Minute {
		return
	}
	s.lastGroups = now
	for _, u := range s.net.Users() {
		u.Groups = s.groupsOf(u, now)
	}
}

// groupsAfter refreshes the groups of whoever ev changed.
func (s *Server) groupsAfter(ev state.Event) {
	switch e := ev.(type) {
	case *state.UserAdd:
		s.refreshGroups(e.User)
		if e.User.Server != s.cfg.Server.Name && s.netjoin == nil {
			s.throttleRemote(e.User)
		}
	case *state.Login:
		s.refreshGroups(s.net.UserByUID(e.UID))
	case *state.UserMode:
		s.refreshGroups(s.net.UserByUID(e.UID))
	}
}

// inAnyGroup reports whether u is in one of names.
func (s *Server) inAnyGroup(u *state.User, names []string, now time.Time) bool {
	for _, n := range names {
		if s.inGroup(u, n, now, 0) {
			return true
		}
	}
	return false
}

// whoisSecurity adds the reputation and security group lines opers see.
func (s *Server) whoisSecurity(u *state.User, reply func(code string, p ...string)) {
	if u.Service {
		return
	}
	if s.repOn() && u.IP != "" {
		reply(irc.RplWhoisSpecial, u.Nick, "is using an IP with a reputation score of "+strconv.Itoa(s.repScore(u.IP)))
	}
	if gs := s.groupsOf(u, time.Now()); len(gs) > 0 {
		reply(irc.RplWhoisSpecial, u.Nick, "is in security-groups: "+strings.Join(gs, ","))
	}
}

// ---- the new-user throttle ([connthrottle]) ----

type newUserThrottle struct {
	localStart, globalStart time.Time
	local, global           int
	off                     bool // THROTTLE OFF
	refused                 int  // since it last started refusing
	refusing                bool
}

// throttleOff says why the new-user throttle isn't running, "" = it is.
func (s *Server) throttleOff(now time.Time) string {
	t := s.cfg.ConnThrottle
	switch {
	case !t.On():
		return "it is off in the config"
	case s.nut.off:
		return "an oper turned it off (THROTTLE ON turns it back on)"
	case now.Sub(s.created) < t.Delay():
		return "the server started less than " + t.Delay().String() + " ago"
	case s.gathering(t.GatherTime(), now):
		return "reputation is still being gathered (" + t.GatherTime().String() + " first)"
	}
	return ""
}

// throttleNew decides whether a new person may finish connecting: ""
// means yes, else the reason they are closed with.
func (s *Server) throttleNew(u *state.User, now time.Time) string {
	t := s.cfg.ConnThrottle
	if s.throttleOff(now) != "" || s.inAnyGroup(u, t.ExceptGroups(), now) {
		return ""
	}
	ln, lper, lok, gn, gper, gok := t.Limits()
	n := &s.nut
	if lok && now.Sub(n.localStart) >= lper {
		n.localStart, n.local = now, 0
	}
	if gok && now.Sub(n.globalStart) >= gper {
		n.globalStart, n.global = now, 0
	}
	if (lok && n.local >= ln) || (gok && n.global >= gn) {
		n.refused++
		if !n.refusing {
			n.refusing = true
			s.snotice("[ConnThrottle] Connection throttling has been ACTIVATED due to a HIGH CONNECTION RATE.")
			s.snotice("[ConnThrottle] Users with IP addresses that have not been seen before will be rejected above the set connection rate. Known users can still get in.")
		}
		s.snoticeEvery("connthrottle-refused", time.Minute, "[ConnThrottle] %d new connections refused so far", n.refused)
		return t.Message()
	}
	if n.refusing && n.local == 0 && n.global == 0 {
		n.refusing = false
		s.snotice("[ConnThrottle] Connection throttling ended: %d people were refused.", n.refused)
		n.refused = 0
	}
	n.local++
	n.global++
	return ""
}

// throttleRemote counts someone new connecting on another server.
func (s *Server) throttleRemote(u *state.User) {
	now := time.Now()
	t := s.cfg.ConnThrottle
	if s.throttleOff(now) != "" || s.inAnyGroup(u, t.ExceptGroups(), now) {
		return
	}
	_, _, _, _, gper, gok := t.Limits()
	if !gok {
		return
	}
	if now.Sub(s.nut.globalStart) >= gper {
		s.nut.globalStart, s.nut.global = now, 0
	}
	s.nut.global++
}

// cmdThrottle is THROTTLE [STATUS|ON|OFF|RESET] for opers.
func (s *Server) cmdThrottle(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'h') {
		return
	}
	now := time.Now()
	sub := strings.ToUpper(m.Param(0))
	if sub != "" && sub != "STATUS" && !s.needFlag(c, nil, 'g') {
		return
	}
	switch sub {
	case "", "STATUS":
		ln, lper, lok, gn, gper, gok := s.cfg.ConnThrottle.Limits()
		if why := s.throttleOff(now); why != "" {
			s.notice(c, "New-user throttle: not running ("+why+").")
		} else {
			s.notice(c, "New-user throttle: running.")
		}
		rate := func(ok bool, n int, per time.Duration, have int) string {
			if !ok {
				return "off"
			}
			return fmt.Sprintf("%d of %d in %d seconds", have, n, int(per/time.Second))
		}
		s.notice(c, "This server: "+rate(lok, ln, lper, s.nut.local)+". The network: "+rate(gok, gn, gper, s.nut.global)+".")
		s.notice(c, "Never throttled: "+strings.Join(s.cfg.ConnThrottle.ExceptGroups(), ", ")+".")
		if s.nut.refusing {
			s.notice(c, fmt.Sprintf("Refusing now: %d people refused.", s.nut.refused))
		}
	case "OFF":
		s.nut.off = true
		s.snotice("%s turned the new-user throttle off", c.user.Nick)
	case "ON":
		s.nut.off = false
		s.snotice("%s turned the new-user throttle on", c.user.Nick)
	case "RESET":
		s.nut = newUserThrottle{off: s.nut.off}
		s.snotice("%s reset the new-user throttle counts", c.user.Nick)
	default:
		s.notice(c, "THROTTLE [STATUS|ON|OFF|RESET]")
	}
}

// ---- [[restrict]] ----

// restrictWhy says why c may not do kind yet ("" = it may), with r the
// block and up how long c has been connected.
func (s *Server) restrictWhy(c *Client, kind string) (config.Restrict, bool) {
	if c.isOper() || c.user == nil {
		return config.Restrict{}, false
	}
	r, ok := s.cfg.RestrictFor(kind)
	if !ok {
		return r, false
	}
	now := time.Now()
	if r.ConnectTime.Duration > 0 && now.Sub(c.user.SignOn) >= r.ConnectTime.Duration {
		return r, false
	}
	if s.inAnyGroup(c.user, r.Except, now) {
		return r, false
	}
	return r, true
}

func secs(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }

// restrictedCommand reports (and answers) a whole command c may not use yet.
func (s *Server) restrictedCommand(c *Client, cmd string) bool {
	r, stop := s.restrictWhy(c, strings.ToLower(cmd))
	if !stop {
		return false
	}
	if r.ConnectTime.Duration > 0 {
		s.reply(c, irc.ErrUnknownCommand, cmd, "You must be connected for at least "+secs(r.ConnectTime.Duration)+" seconds before you can use this command")
	} else {
		s.reply(c, irc.ErrUnknownCommand, cmd, "This command is disabled by the network administrator")
	}
	return true
}

// restrictedMessage is why c may not send a message (or notice) to a
// channel or a person yet, "" = it may.
func (s *Server) restrictedMessage(c *Client, verb string, toChannel bool) string {
	notice := verb == "NOTICE"
	kind, what, to := "private-message", "message", "user"
	if toChannel {
		kind, to = "channel-message", "channel"
	}
	if notice {
		kind = strings.Replace(kind, "message", "notice", 1)
		what = "notice"
	}
	r, stop := s.restrictWhy(c, kind)
	if !stop {
		return ""
	}
	if r.ConnectTime.Duration > 0 {
		return "You cannot send " + what + "s to " + to + "s until you've been connected for " + secs(r.ConnectTime.Duration) + " seconds or more"
	}
	return "Sending of " + what + "s to " + to + "s been disabled by the network administrators"
}

// restrictedCreate is why c may not make a new channel yet, "" = it may.
func (s *Server) restrictedCreate(c *Client) string {
	r, stop := s.restrictWhy(c, "channel-create")
	if !stop {
		return ""
	}
	if r.ConnectTime.Duration > 0 {
		return "You cannot create new channels until you have been connected for " + secs(r.ConnectTime.Duration) + " seconds or more."
	}
	return "Creation of new channels has been restricted by the network administrator."
}
