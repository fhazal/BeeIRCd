package server

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
)

// Oper tools other servers have: SAKICK, SAQUIT, CLEARCHAN, NICKLOCK,
// NICKUNLOCK, SETIDLE, CHECK, TLINE and ALLTIME (InspIRCd), TRACE, TSCTL,
// SDESC and JUMPSERVER (UnrealIRCd).

const (
	rplRedir        = "010"
	rplTraceOper    = "204"
	rplTraceUser    = "205"
	rplTraceServer  = "206"
	rplTraceEnd     = "262"
	rplCheck        = "802"
	rplIdleTimeSet  = "944"
	rplNickLockOff  = "945"
	errNickNotLock  = "946"
	rplNickLockOn   = "947"
	errInvalidIdle  = "948"
	maxServerDesc   = 50
	clearChanBanFor = time.Hour
)

func init() {
	for name, cmd := range map[string]command{
		"SAKICK":     {2, false, (*Server).cmdSakick},
		"SAQUIT":     {2, false, (*Server).cmdSaquit},
		"CLEARCHAN":  {1, false, (*Server).cmdClearchan},
		"NICKLOCK":   {2, false, (*Server).cmdNicklock},
		"NICKUNLOCK": {1, false, (*Server).cmdNickunlock},
		"SETIDLE":    {1, false, (*Server).cmdSetidle},
		"TRACE":      {0, false, (*Server).cmdTrace},
		"CHECK":      {1, false, (*Server).cmdCheck},
		"TLINE":      {1, false, (*Server).cmdTline},
		"TSCTL":      {0, false, (*Server).cmdTsctl},
		"ALLTIME":    {0, false, (*Server).cmdAlltime},
		"SDESC":      {1, false, (*Server).cmdSdesc},
		"JUMPSERVER": {0, false, (*Server).cmdJumpserver},
	} {
		commands[name] = cmd
	}
	for name, lines := range map[string][]string{
		"SAKICK":     {"SAKICK <#channel> <nick> [reason]", "Kicks someone from a channel as the server, whatever their status there (opers, flag a)."},
		"SAQUIT":     {"SAQUIT <nick> <reason>", "Makes someone quit with the reason you give, as if they left by themselves (opers, flag a)."},
		"CLEARCHAN":  {"CLEARCHAN <#channel> [KICK|KILL|G|Z] [reason]", "Empties a channel: everyone but you, IRC operators and services is kicked (the default), killed, or banned from the network for an hour by host (G) or IP (Z). Opers, flag a."},
		"NICKLOCK":   {"NICKLOCK <nick> <newnick>", "Changes someone's nick and stops them changing it until NICKUNLOCK or until they reconnect (opers, flag a)."},
		"NICKUNLOCK": {"NICKUNLOCK <nick>", "Lets someone change their nick again after NICKLOCK (opers, flag a)."},
		"SETIDLE":    {"SETIDLE <time>", "Sets your own idle time as WHOIS shows it, like 90 (seconds), 30m or 2h (opers)."},
		"TRACE":      {"TRACE [server]", "Lists the connections of a server: people, opers and linked servers (opers; everyone else sees only themselves)."},
		"CHECK":      {"CHECK <nick|#channel|mask>", "Shows everything the server knows about a person or channel, or who matches a nick!user@host or IP mask (opers)."},
		"TLINE":      {"TLINE <user@host>", "Counts how many people a ban mask would hit, by host and by IP, without banning anyone (opers)."},
		"TSCTL":      {"TSCTL ALLTIME", "Shows the clock of every server on the network, to spot one that is off (opers)."},
		"ALLTIME":    {"ALLTIME", "The same as TSCTL ALLTIME."},
		"SDESC":      {"SDESC <description>", "Changes this server's description (LINKS, WHOIS) until the next restart or rehash (opers, flag a)."},
		"JUMPSERVER": {"JUMPSERVER <server[:port]> [<tls server[:port]>] <NEW|ALL> [reason] | OFF", "Sends people to another server (numeric 010) before maintenance: NEW redirects whoever connects from now on, ALL also everyone connected now except opers. JUMPSERVER alone shows the setting, OFF ends it (opers, flag a)."},
	} {
		helpTopics[name] = lines
	}
}

// needOper checks that c is an IRC operator (any flags).
func (s *Server) needOper(c *Client) bool {
	if c.isOper() {
		return true
	}
	s.reply(c, irc.ErrNoPrivileges, "Permission denied: you're not an IRC operator")
	return false
}

// opUser finds the person an oper command is about, here or on another
// server (not a service).
func (s *Server) opUser(c *Client, nick string) *state.User {
	t := s.userByUIDOrNick(nick)
	switch {
	case t == nil:
		s.reply(c, irc.ErrNoSuchNick, nick, "No such nick/channel")
	case t.Service:
		s.reply(c, irc.ErrNoPrivileges, "Cannot use this command on a network service")
	default:
		return t
	}
	return nil
}

// ---- commands that run on the server of the person they are about ----

// relayCmds are commands only someone's own server can carry out. They
// travel there as ":<uid> CMD ..." and run with a stand-in for the
// sender; the value is which parameter names the person.
var relayCmds = map[string]int{
	"SAJOIN": 0, "SAPART": 0, "SANICK": 0, "SAQUIT": 0, "NICKLOCK": 0, "NICKUNLOCK": 0,
	"REMOVE": 1,
}

// relayOper passes an oper command about someone on another server to
// that server; false when the person is here (or unknown, which the
// command says itself). beeOnly: UnrealIRCd has no such command.
func (s *Server) relayOper(c *Client, m irc.Message, beeOnly bool) bool {
	idx := relayCmds[m.Command]
	if idx >= len(m.Params) {
		return false
	}
	t := s.userByUIDOrNick(m.Params[idx])
	if t == nil || t.Service || s.ours(t.UID) {
		return false
	}
	o := s.linkOf(t.UID)
	switch {
	case o == nil || !o.up || o.closed:
		s.reply(c, irc.ErrNoSuchNick, m.Params[idx], "No such nick/channel")
	case o.bridge != nil && !bridgeCarries[m.Command] || beeOnly && !o.peer.Bee:
		s.notice(c, "*** "+m.Command+" can't reach "+t.Nick+": "+t.Server+" is behind a link that doesn't carry it")
	case o.bridge != nil && m.Command != "SANICK":
		// InspIRCd takes one channel in each SAJOIN or SAPART.
		for _, name := range strings.Split(m.Params[1], ",") {
			if name == "" {
				continue
			}
			line := ":" + c.uid + " " + m.Command + " " + t.UID + " " + name
			if r := m.Param(2); r != "" && m.Command == "SAPART" {
				line += " :" + r
			}
			o.send(line)
		}
		s.snotice("%s used %s on %s (on %s): %s", c.user.Nick, m.Command, t.Nick, t.Server, strings.Join(m.Params[1:], " "))
	default:
		p := append([]string{}, m.Params...)
		p[idx] = t.UID
		o.send(":" + c.uid + " " + m.Command + " " + strings.Join(p[:len(p)-1], " ") + " :" + p[len(p)-1])
		s.snotice("%s used %s on %s (on %s): %s", c.user.Nick, m.Command, t.Nick, t.Server, strings.Join(m.Params[1:], " "))
	}
	return true
}

// relayedIn handles a relayCmds line from a user elsewhere: run here when
// it is about one of ours, sent on when not. false: not such a line (a
// server sent it, like services' own SAJOIN).
func (s *Server) relayedIn(l *linkConn, m irc.Message) bool {
	idx, ok := relayCmds[m.Command]
	if !ok || !isUID(m.Source) || s.net.UserByUID(m.Source) == nil {
		return false
	}
	if !s.beeLink(m.Source) {
		// An UnrealIRCd or InspIRCd oper: their server checked their
		// rights, and we don't know their flags.
		switch m.Command {
		case "SAJOIN", "SAPART":
			return false // joined and parted as before
		case "SANICK":
			s.farSanick(l, m)
			return true
		}
	}
	if idx >= len(m.Params) {
		return true
	}
	t := s.userByUIDOrNick(m.Params[idx])
	if t == nil {
		return true
	}
	if !s.ours(t.UID) {
		if o := s.linkOf(t.UID); o != nil && o != l {
			o.send(m.String())
		}
		return true
	}
	m.Params[idx] = t.Nick
	cmd := commands[m.Command]
	if len(m.Params) < cmd.min {
		return true
	}
	s.runFor(m.Source, func(c *Client) { cmd.run(s, c, m) })
	return true
}

// bridgeCarries are the relayCmds an InspIRCd link carries too.
var bridgeCarries = map[string]bool{"SAJOIN": true, "SAPART": true, "SANICK": true}

// farSanick is SANICK from an oper of another kind of server.
func (s *Server) farSanick(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 {
		return
	}
	t, from := s.userByUIDOrNick(m.Params[0]), s.net.UserByUID(m.Source)
	if t == nil || from == nil {
		return
	}
	if !s.ours(t.UID) {
		if o := s.linkOf(t.UID); o != nil && o != l && (o.peer.Bee || o.bridge != nil) {
			o.send(":" + m.Source + " SANICK " + t.UID + " " + m.Params[1])
		}
		return
	}
	nick, old := m.Params[1], t.Nick
	if !irc.ValidNick(nick, s.cfg.Limits.NickLen) || services.IsReserved(nick) || (!irc.EqualFold(nick, old) && s.net.NickInUse(nick)) {
		host{s}.ServerNotice(from.UID, "*** SANICK: "+old+" can't be renamed to "+nick)
		return
	}
	if err := s.emit(&state.NickChange{UID: t.UID, Nick: nick, TS: time.Now().Unix()}); err != nil {
		return
	}
	s.snotice("%s used SANICK to change %s to %s", from.Nick, old, nick)
}

// ---- SA commands ----

// cmdSakick is SAKICK #chan nick [reason] (oper flag a).
func (s *Server) cmdSakick(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	t := s.opUser(c, m.Params[1])
	if t == nil {
		return
	}
	if ch.Member(t) == nil {
		s.reply(c, irc.ErrUserNotInChannel, t.Nick, ch.Name, "They aren't on that channel")
		return
	}
	reason := irc.Truncate(m.Param(2), s.cfg.Limits.KickLen)
	if reason == "" {
		reason = c.user.Nick
	}
	name, nick := ch.Name, t.Nick
	s.emit(&state.Kick{UID: t.UID, Channel: name, Reason: reason})
	s.snotice("%s used SAKICK to kick %s from %s (%s)", c.user.Nick, nick, name, reason)
}

// cmdSaquit is SAQUIT nick reason (oper flag a).
func (s *Server) cmdSaquit(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	t := s.opUser(c, m.Params[0])
	if t == nil {
		return
	}
	reason := irc.Truncate(m.Params[1], s.cfg.Limits.KickLen)
	if reason == "" {
		s.reply(c, irc.ErrNeedMoreParams, "SAQUIT", "Not enough parameters")
		return
	}
	if tc := s.clientOf(t); tc != nil {
		s.snotice("%s used SAQUIT to make %s quit (%s)", c.user.Nick, t.Nick, reason)
		s.quit(tc, reason)
		return
	}
	if o := s.linkOf(t.UID); o != nil && o.peer.Bee {
		s.relayOper(c, m, true)
		return
	}
	// Other software has no SAQUIT: the nearest thing is a kill.
	s.killUser(t, c.user.Nick, reason)
}

// cmdClearchan is CLEARCHAN #chan [KICK|KILL|G|Z] [reason] (oper flag a).
func (s *Server) cmdClearchan(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	how, args := "KICK", m.Params[1:]
	if len(args) > 0 {
		switch w := strings.ToUpper(args[0]); w {
		case "KICK", "KILL", "G", "Z":
			how, args = w, args[1:]
		}
	}
	reason := irc.Truncate(strings.Join(args, " "), s.cfg.Limits.KickLen)
	if reason == "" {
		reason = "Clearing channel"
	}
	var targets []*state.User
	for _, mem := range ch.Members {
		if u := mem.User; u != c.user && !u.Service && !u.Modes['o'] {
			targets = append(targets, u)
		}
	}
	sort.Slice(targets, func(i, k int) bool { return targets[i].UID < targets[k].UID })
	name, by := ch.Name, c.user.Nick
	s.snotice("%s used CLEARCHAN %s on %s (%d to remove): %s", by, how, name, len(targets), reason)
	for _, t := range targets {
		switch how {
		case "KICK":
			if ch := s.net.Channel(name); ch != nil && ch.Member(t) != nil {
				s.emit(&state.Kick{UID: t.UID, Channel: name, Reason: reason})
			}
			continue
		case "G", "Z":
			if t.IP != "" {
				s.svc.OperBan(how == "Z", t.IP, reason, by, clearChanBanFor)
			}
		}
		if t := s.net.UserByUID(t.UID); t != nil {
			s.killUser(t, by, reason)
		}
	}
	s.notice(c, fmt.Sprintf("*** CLEARCHAN %s: removed %d from %s", how, len(targets), name))
}

// ---- NICKLOCK ----

// cmdNicklock is NICKLOCK nick newnick (oper flag a).
func (s *Server) cmdNicklock(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) || s.relayOper(c, m, true) {
		return
	}
	t := s.opTarget(c, m.Params[0])
	if t == nil {
		return
	}
	nick := m.Params[1]
	if !irc.ValidNick(nick, s.cfg.Limits.NickLen) || services.IsReserved(nick) {
		s.stdReply(c, "FAIL", "NICKLOCK", "INVALID_NICK", "Invalid nickname: '"+nick+"'", "", nick)
		return
	}
	old := t.user.Nick
	if nick != old {
		if !irc.EqualFold(nick, old) && s.net.NickInUse(nick) {
			s.reply(c, irc.ErrNicknameInUse, nick, "Nickname is already in use")
			return
		}
		if err := s.emit(&state.NickChange{UID: t.uid, Nick: nick, TS: time.Now().Unix()}); err != nil {
			s.reply(c, irc.ErrNicknameInUse, nick, "Nickname is already in use")
			return
		}
	}
	t.nickLocked = true
	s.reply(c, rplNickLockOn, nick, "Nickname now locked.")
	s.notice(t, "*** Your nickname is now "+nick+" and locked: you can't change it until an IRC operator unlocks it.")
	s.snotice("%s used NICKLOCK to change and hold the nick of %s to %s", c.user.Nick, old, nick)
}

// cmdNickunlock is NICKUNLOCK nick (oper flag a).
func (s *Server) cmdNickunlock(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) || s.relayOper(c, m, true) {
		return
	}
	t := s.opTarget(c, m.Params[0])
	if t == nil {
		return
	}
	if !t.nickLocked {
		s.reply(c, errNickNotLock, t.user.Nick, "This user's nickname is not locked.")
		return
	}
	t.nickLocked = false
	s.reply(c, rplNickLockOff, t.user.Nick, "Nickname now unlocked.")
	s.notice(t, "*** Your nickname is unlocked: you can change it again.")
	s.snotice("%s used NICKUNLOCK on %s", c.user.Nick, t.user.Nick)
}

// cmdSetidle is SETIDLE <seconds|30m|2h> (opers): your own idle time.
func (s *Server) cmdSetidle(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	var d time.Duration
	if n, err := strconv.Atoi(m.Params[0]); err == nil {
		d = time.Duration(n) * time.Second
	} else if d, err = parseLife(m.Params[0]); err != nil {
		d = -1
	}
	if d < 0 || d > 10*365*24*time.Hour {
		s.reply(c, errInvalidIdle, "Invalid idle time.")
		return
	}
	c.lastMsg = time.Now().Add(-d)
	s.reply(c, rplIdleTimeSet, "Idle time set.")
	s.snotice("%s used SETIDLE to set their idle time to %s", c.user.Nick, d)
}

// ---- TRACE ----

// cmdTrace is TRACE [server].
func (s *Server) cmdTrace(c *Client, m irc.Message) {
	if len(m.Params) > 0 && s.queryElsewhere(c, "TRACE", m.Params[0], "") {
		return
	}
	s.traceHere(c)
}

// traceHere lists this server's connections for c: everything for an
// oper, only themselves for anyone else.
func (s *Server) traceHere(c *Client) {
	now := time.Now()
	line := func(o *Client) {
		u := o.user
		code, kind, class := rplTraceUser, "User", "users"
		if o.isOper() {
			code, kind, class = rplTraceOper, "Oper", orStr(o.operClass, "opers")
		}
		s.replyBare(c, code, kind, class, u.Nick, "["+u.TrueIdent()+"@"+u.RealHost+"]", strconv.FormatInt(int64(now.Sub(o.lastRecv).Seconds()), 10))
	}
	if !c.isOper() {
		if o := s.clients[c.uid]; o != nil && o.user != nil {
			line(o)
		}
		s.reply(c, rplTraceEnd, s.cfg.Server.Name, "End of TRACE")
		return
	}
	var local []*Client
	for _, o := range s.clients {
		if o.user != nil && !o.closed {
			local = append(local, o)
		}
	}
	sort.Slice(local, func(i, k int) bool { return irc.Fold(local[i].user.Nick) < irc.Fold(local[k].user.Nick) })
	for _, o := range local {
		line(o)
	}
	links := s.upLinks(nil)
	sort.Slice(links, func(i, k int) bool { return links[i].peer.Name < links[k].peer.Name })
	for _, o := range links {
		servers, people := 0, 0
		for _, r := range s.links.servers {
			if r.via == o {
				servers++
			}
		}
		for _, u := range s.net.Users() {
			if !u.Service && s.linkOf(u.UID) == o {
				people++
			}
		}
		s.replyBare(c, rplTraceServer, "Serv", "servers", strconv.Itoa(servers)+"S", strconv.Itoa(people)+"C", o.peer.Name, "*!*@"+o.peer.Name,
			strconv.FormatInt(int64(now.Sub(o.since).Seconds()), 10))
	}
	s.reply(c, rplTraceEnd, s.cfg.Server.Name, "End of TRACE")
}

// ---- CHECK and TLINE ----

// cmdCheck is CHECK nick | #channel | mask (opers): numeric 802 lines,
// "key :value", between START and END.
func (s *Server) cmdCheck(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	target := m.Params[0]
	out := func(key, val string) { s.reply(c, rplCheck, key, val) }
	stamp := func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") }
	out("START", target)
	defer out("END", target)
	if irc.IsChannel(target) {
		ch := s.net.Channel(target)
		if ch == nil {
			out("error", "No such channel")
			return
		}
		modes, args := ch.ModeString(true)
		out("createdat", stamp(time.Unix(ch.TS, 0)))
		if ch.Topic != "" {
			out("topic", ch.Topic)
			out("topic_setby", ch.TopicBy)
			out("topic_setat", stamp(ch.TopicAt))
		}
		out("modes", strings.TrimSpace(modes+" "+strings.Join(args, " ")))
		out("membercount", strconv.Itoa(len(ch.Members)))
		var lines []string
		for _, mem := range ch.Members {
			u := mem.User
			lines = append(lines, mem.AllPrefixes()+u.Nick+"!"+u.TrueIdent()+"@"+u.RealHost+" "+u.Realname)
		}
		sort.Strings(lines)
		for _, l := range lines {
			out("member", l)
		}
		return
	}
	if u := s.net.UserByNick(target); u != nil {
		out("nuh", u.Prefix())
		out("realnuh", u.Nick+"!"+u.TrueIdent()+"@"+u.RealHost)
		out("realname", u.Realname)
		out("modes", u.ModeString())
		out("server", u.Server)
		out("uid", u.UID)
		out("signon", stamp(u.SignOn))
		out("nickts", stamp(time.Unix(u.NickTS, 0)))
		if u.IP != "" {
			out("onip", u.IP)
			if s.repOn() {
				out("reputation", strconv.Itoa(s.repScore(u.IP)))
			}
		}
		if u.Country != "" {
			out("country", u.Country)
		}
		if u.ASN != 0 {
			out("asn", strings.TrimSpace("AS"+strconv.FormatUint(uint64(u.ASN), 10)+" "+u.ASOrg))
		}
		if u.Account != "" {
			out("account", u.Account)
		}
		if u.CertFP != "" {
			out("certfp", u.CertFP)
		}
		if u.Away != "" {
			out("awaymsg", u.Away)
		}
		if u.Modes['o'] {
			class := u.OperClass
			if tc := s.clientOf(u); tc != nil {
				class = tc.operClass
			}
			out("opertype", orStr(class, "oper"))
		}
		if len(u.Groups) > 0 {
			out("groups", strings.Join(u.Groups, " "))
		}
		if tc := s.clientOf(u); tc != nil {
			out("onport", strconv.Itoa(tc.port))
			via := "plain"
			switch {
			case tc.websocket && tc.tls:
				via = "websocket (TLS)"
			case tc.websocket:
				via = "websocket"
			case tc.tls:
				via = "TLS"
			}
			if tc.webirc != "" {
				via += ", WEBIRC " + tc.webirc
			}
			out("connection", via)
			out("lastmsg", stamp(tc.lastMsg))
			if tc.nickLocked {
				out("nicklock", "locked")
			}
		}
		var chans []string
		for _, ch := range u.Channels {
			if mem := ch.Member(u); mem != nil {
				chans = append(chans, mem.AllPrefixes()+ch.Name)
			}
		}
		sort.Strings(chans)
		for len(chans) > 0 {
			n := min(10, len(chans))
			out("onchans", strings.Join(chans[:n], " "))
			chans = chans[n:]
		}
		return
	}
	mask := strings.ToLower(target)
	n := 0
	for _, u := range s.net.Users() {
		if u.Service {
			continue
		}
		hit := u.IP != "" && irc.Match(mask, u.IP)
		for _, h := range []string{u.Host, u.RealHost, u.IP} {
			hit = hit || h != "" && (irc.Match(mask, u.Nick+"!"+u.TrueIdent()+"@"+h) || irc.Match(mask, u.TrueIdent()+"@"+h) || irc.Match(mask, h))
		}
		if !hit {
			continue
		}
		if n++; n <= 100 {
			out("match", strconv.Itoa(n)+" "+u.Nick+"!"+u.TrueIdent()+"@"+u.RealHost+" "+orStr(u.IP, "*")+" "+u.Realname)
		}
	}
	out("matches", strconv.Itoa(n))
}

// cmdTline is TLINE user@host (opers): how many people a mask would hit.
func (s *Server) cmdTline(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	mask := strings.ToLower(m.Params[0])
	if !strings.Contains(mask, "@") {
		mask = "*@" + mask
	}
	total, byHost, byIP := 0, 0, 0
	for _, u := range s.net.Users() {
		if u.Service {
			continue
		}
		total++
		switch {
		case irc.MatchUserHost(mask, u.TrueIdent(), u.RealHost, u.Host):
			byHost++
		case u.IP != "" && irc.MatchUserHost(mask, u.TrueIdent(), u.IP):
			byIP++
		}
	}
	hit := byHost + byIP
	if hit == 0 {
		s.notice(c, fmt.Sprintf("*** TLINE: Counted %d user(s). Matched '%s' against nobody.", total, mask))
		return
	}
	s.notice(c, fmt.Sprintf("*** TLINE: Counted %d user(s). Matched '%s' against %d user(s) (%.2f%% of the userbase). %d by hostname and %d by IP address.",
		total, mask, hit, float64(hit)*100/float64(total), byHost, byIP))
}

// ---- TSCTL ALLTIME / ALLTIME ----

func (s *Server) tsLine() string {
	now := time.Now()
	return fmt.Sprintf("*** Server=%s TStime=%d (%s)", s.cfg.Server.Name, now.Unix(), now.UTC().Format("2006-01-02 15:04:05 UTC"))
}

// cmdTsctl is TSCTL ALLTIME (UnrealIRCd).
func (s *Server) cmdTsctl(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	if !strings.EqualFold(m.Param(0), "ALLTIME") {
		s.notice(c, "*** TSCTL: use TSCTL ALLTIME to see the clock of every server")
		return
	}
	s.cmdAlltime(c, m)
}

// cmdAlltime asks every server for its clock; each answers by itself.
func (s *Server) cmdAlltime(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	s.notice(c, s.tsLine())
	s.linkBroadcast(nil, ":"+c.uid+" TSCTL alltime")
}

// remoteTsctl is TSCTL alltime from an oper elsewhere.
func (s *Server) remoteTsctl(l *linkConn, m irc.Message) {
	u := s.net.UserByUID(m.Source)
	if u == nil || !strings.EqualFold(m.Param(0), "alltime") {
		return
	}
	host{s}.ServerNotice(u.UID, s.tsLine())
	s.linkBroadcast(l, m.String())
}

// ---- SDESC ----

// cmdSdesc is SDESC <text> (oper flag a): this server's description,
// until a restart or rehash.
func (s *Server) cmdSdesc(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	text := strings.TrimSpace(strings.Join(m.Params, " "))
	if text == "" {
		s.reply(c, irc.ErrNeedMoreParams, "SDESC", "Not enough parameters")
		return
	}
	if len(text) > maxServerDesc {
		s.notice(c, fmt.Sprintf("*** SDESC: a server description is at most %d characters", maxServerDesc))
		return
	}
	s.cfg.Server.Description = text
	s.linkBroadcast(nil, ":"+c.uid+" SDESC :"+text)
	s.notice(c, "*** The description of "+s.cfg.Server.Name+" is now: "+text)
	s.snotice("Server description for %s is now '%s' (changed by %s)", s.cfg.Server.Name, text, c.user.Nick)
}

// remoteSdesc is SDESC from an oper elsewhere: their server's new
// description.
func (s *Server) remoteSdesc(l *linkConn, m irc.Message) {
	u := s.net.UserByUID(m.Source)
	if u == nil || len(m.Params) == 0 {
		return
	}
	if r := s.links.servers[uidServer(u.UID)]; r != nil {
		r.desc = irc.Truncate(m.Params[len(m.Params)-1], maxServerDesc)
	}
	s.linkBroadcast(l, m.String())
}

// ---- JUMPSERVER ----

// jumpServer is where JUMPSERVER sends people.
type jumpServer struct {
	host, tlsHost string
	port, tlsPort int
	reason        string
}

func (j *jumpServer) String() string {
	out := j.host + ":" + strconv.Itoa(j.port)
	if j.tlsHost != "" {
		out += " (TLS: " + j.tlsHost + ":" + strconv.Itoa(j.tlsPort) + ")"
	}
	return out
}

// jumpAddr reads "server[:port]".
func jumpAddr(v string, defPort int) (string, int, bool) {
	hostPart, port := v, defPort
	if h, p, err := net.SplitHostPort(v); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, false
		}
		hostPart, port = h, n
	}
	if net.ParseIP(hostPart) == nil && !validHostname(hostPart) {
		return "", 0, false
	}
	return hostPart, port, true
}

// cmdJumpserver is JUMPSERVER <server[:port]> [<tls server[:port]>]
// <NEW|ALL> [reason], JUMPSERVER OFF, or alone for the setting (oper
// flag a). It holds until OFF or a restart, on this server only.
func (s *Server) cmdJumpserver(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	args := strings.Fields(strings.Join(m.Params, " "))
	usage := func() {
		s.notice(c, "*** Usage: JUMPSERVER <server[:port]> [<tls server[:port]>] <NEW|ALL> [reason], or JUMPSERVER OFF")
	}
	if len(args) == 0 {
		if s.jump == nil {
			s.notice(c, "*** JumpServer is off: nobody is being redirected.")
		} else {
			s.notice(c, "*** JumpServer is on: new connections are sent to "+s.jump.String()+" ("+s.jump.reason+")")
		}
		return
	}
	if strings.EqualFold(args[0], "OFF") {
		if s.jump == nil {
			s.notice(c, "*** JumpServer is already off.")
			return
		}
		s.jump = nil
		s.snotice("%s turned JUMPSERVER off: people can connect here again", c.user.Nick)
		s.notice(c, "*** JumpServer is now off.")
		return
	}
	j := &jumpServer{}
	var ok bool
	if j.host, j.port, ok = jumpAddr(args[0], 6667); !ok {
		s.notice(c, "*** JUMPSERVER: '"+args[0]+"' is not a server name or server:port")
		return
	}
	args = args[1:]
	if len(args) > 0 && !strings.EqualFold(args[0], "NEW") && !strings.EqualFold(args[0], "ALL") {
		if j.tlsHost, j.tlsPort, ok = jumpAddr(args[0], 6697); !ok {
			s.notice(c, "*** JUMPSERVER: '"+args[0]+"' is not a server name or server:port")
			return
		}
		args = args[1:]
	}
	if len(args) == 0 {
		usage()
		return
	}
	all := strings.EqualFold(args[0], "ALL")
	if !all && !strings.EqualFold(args[0], "NEW") {
		usage()
		return
	}
	j.reason = irc.Truncate(strings.Join(args[1:], " "), s.cfg.Limits.KickLen)
	if j.reason == "" {
		j.reason = "This server is sending everyone to " + j.host
	}
	s.jump = j
	who := "new connections"
	if all {
		who = "everyone (and new connections)"
	}
	s.snotice("%s set JUMPSERVER: %s go to %s (%s)", c.user.Nick, who, j.String(), j.reason)
	s.notice(c, "*** JumpServer is on: "+who+" go to "+j.String()+". JUMPSERVER OFF ends it.")
	if !all {
		return
	}
	var move []*Client
	for _, o := range s.clients {
		if o.user != nil && !o.closed && !o.isOper() {
			move = append(move, o)
		}
	}
	for _, o := range move {
		s.jumpRedirect(o)
	}
}

// jumpRedirect tells c where to go (010) and disconnects it.
func (s *Server) jumpRedirect(c *Client) {
	j := s.jump
	hostName, port := j.host, j.port
	if c.tls && j.tlsHost != "" {
		hostName, port = j.tlsHost, j.tlsPort
	}
	s.reply(c, rplRedir, hostName, strconv.Itoa(port), "Please use this Server/Port instead")
	s.quit(c, j.reason)
}
