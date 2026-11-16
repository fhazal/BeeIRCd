package services

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	maxNews      = 10
	maxAkillList = 200
)

// operServAliases are old names that still work but HELP doesn't show.
var operServAliases = map[string]string{
	"NEWS":   "LOGONNEWS",
	"EXCEPT": "EXCEPTION",
}

func operServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"DEFCON": {"DEFCON [1|2|3|4|5]", "lock the network down in steps",
			"DEFCON alone shows the level; DEFCON <n> sets it (flag a). 5 is normal.\n4  no new nick or channel registrations\n3  also no new memos (opers still may)\n2  also: people who connect now are disconnected\n1  also: services answer IRC operators only\nEveryone gets a Global notice when it changes.", osDefcon},
		"STATS": {"STATS [AKILL | HASH | UPLINK | UPTIME | ALL | RESET]", "people, channels, opers, uptime",
			"STATS alone shows how busy the network is. AKILL counts the network bans,\nHASH the size of the tables, UPLINK the server links, UPTIME people and\nuptime; ALL shows everything. RESET makes the people peak the number online\nnow. Any oper.", osStats},
		"GLOBAL": {"GLOBAL <text>", "a notice to everyone",
			"Sends a notice from Global to everyone on the network.\nExample: /os global The server restarts at 22:00 UTC.", osGlobal},
		"AKILL": {"AKILL ADD|DEL|LIST|VIEW|CLEAR ...", "ban from the whole network",
			"AKILL ADD [+expiry] <user@host|nick> <reason>   expiry like +30m, +12h, +7d;\n    without it the AKILL lasts 30 days, +0 keeps it until removed\nAKILL DEL {<mask> | <num> | <list>}   numbers from LIST, like 2 or 1-3,5\nAKILL LIST [<mask> | <list>]\nAKILL VIEW [<mask> | <list>]   with who set it and when\nAKILL CLEAR\nThe host may be a CIDR range, like *@203.0.113.0/24. A nick bans that\nperson's IP. ~C:country or ~A:network bans a place (GeoIP), like ~C:NL or\n~A:64500; SHUN and EXCEPTION BAN take them too. People who match are\ndisconnected at once. Opers type /GLINE for the same list.\nExample: /os akill add +7d *@203.0.113.* spam bots", osNetBan(store.BanAkill)},
		"ZLINE": {"ZLINE ADD|DEL|LIST|VIEW|CLEAR ...", "ban an IP before anything else",
			"ZLINE ADD [+expiry] <ip|cidr|nick> <reason>\nZLINE DEL|LIST|VIEW|CLEAR like AKILL.\nA ZLINE refuses the connection the moment it opens: no host lookup, no ident,\nno services. For floods and known bad ranges. /ZLINE does the same.\nExample: /os zline add +1d 198.51.100.0/24 flood", osNetBan(store.BanZline)},
		"SNLINE": {"SNLINE ADD|DEL|LIST|VIEW|CLEAR ...", "ban by realname",
			"SNLINE ADD [+expiry] <realname-mask|/regex/|nick> <reason>\nSNLINE DEL|LIST|VIEW|CLEAR like AKILL.\nMatches the realname (gecos). Write spaces in a mask as ?, or use a /regex/\n(no case). A nick takes that person's realname. /RLINE does the same.\nExample: /os snline add *free?bitcoin* spam", osNetBan(store.BanSNLine)},
		"SHUN": {"SHUN ADD|DEL|LIST|VIEW|CLEAR ...", "silence someone without disconnecting",
			"SHUN ADD [+expiry] <user@host|nick> <reason>\nSHUN DEL|LIST|VIEW|CLEAR like AKILL.\nA shunned person stays connected but can only PART, QUIT and answer PINGs;\nevery other command is quietly dropped. /SHUN does the same.\nSpam in messages is caught by /SPAMFILTER instead (rules: /STATS F).", osNetBan(store.BanShun)},
		"SQLINE": {"SQLINE ADD|DEL|LIST|CLEAR ...", "nicks nobody may use",
			"SQLINE ADD [+expiry] <nick-mask> [reason]\nSQLINE DEL <nick-mask>\nSQLINE LIST\nSQLINE CLEAR\nThe same list as FORBID NICK: nobody but opers may use or register a matching\nnick. /QLINE does the same.", osSQLine},
		"KILL": {"KILL <nick> [reason]", "disconnect someone",
			"Disconnects someone, with OperServ as the sender.", osKill},
		"MODE": {"MODE #chan {<modes> | CLEAR [ALL]}", "set channel modes as services",
			"Sets modes on a channel without being an op there. CLEAR takes away every\nmode, ban, exception and invite; CLEAR ALL also everyone's status, except\nIRC operators'. The mode lock comes back by itself.\nExample: /os mode #bchat -i+b *!*@spam.example", osMode},
		"KICK": {"KICK #chan <nick> <reason>", "kick as services", "Kicks someone with OperServ as the sender.", osKick},
		"FORBID": {"FORBID ADD|DEL|LIST ...", "names nobody may use",
			"FORBID ADD {NICK|CHAN|EMAIL|REGISTER} [+expiry] <name> [reason]\nFORBID DEL {NICK|CHAN|EMAIL|REGISTER} <name>\nFORBID LIST [NICK|CHAN|EMAIL|REGISTER]\nNICK: nobody but opers may use or register it. CHAN: nobody but opers may\njoin or register it. EMAIL: no account may use it. REGISTER: may be used, but\nnot registered. Names may hold * and ?, like *@mailinator.com or Guest*.", osForbid},
		"SESSION": {"SESSION [LIST <threshold> | VIEW <ip>]", "connections per IP",
			"SESSION                 the limit and the busiest IPs\nSESSION LIST <n>        every IP with at least n connections\nSESSION VIEW <ip>       how many connections an IP has, and its limit\nChange the limit: /as set session-limit <n>. Exceptions: /os exception", osSession},
		"EXCEPTION": {"EXCEPTION ADD|DEL|LIST|VIEW ...", "IPs allowed more connections",
			"EXCEPTION ADD [+expiry] <ip-mask> <limit> [reason]   CIDR or a host name like\n    *.example.org; limit 0 = unlimited\nEXCEPTION DEL {<ip-mask> | <num> | <list>}\nEXCEPTION LIST [<ip-mask> | <list>]\nEXCEPTION VIEW [<ip-mask> | <list>]   with who set it and when\nEXCEPTION BAN ADD|DEL|LIST|VIEW|CLEAR ...   ELINEs: user@host masks no AKILL,\n    ZLINE, SNLINE or SHUN applies to (and no DNSBL). /ELINE does the same.\nExample: /os exception add 192.168.50.0/24 20 home network", osException},
		"LOGONNEWS": {"LOGONNEWS ADD|DEL|LIST ...", "lines shown after the MOTD",
			"LOGONNEWS ADD <text>\nLOGONNEWS DEL {<num> | ALL}\nLOGONNEWS LIST\nEveryone sees the newest 3 when they connect, from Global.", osNews},
		"OPERNEWS": {"OPERNEWS ADD|DEL|LIST ...", "lines shown to opers when they oper up",
			"OPERNEWS ADD <text>\nOPERNEWS DEL {<num> | ALL}\nOPERNEWS LIST\nOpers see the newest 3 when they oper up, from OperServ.", osNews},
		"RANDOMNEWS": {"RANDOMNEWS ADD|DEL|LIST ...", "one line, in turn, for each connection",
			"RANDOMNEWS ADD <text>\nRANDOMNEWS DEL {<num> | ALL}\nRANDOMNEWS LIST\nEveryone gets one of these when they connect, the next one each time, from\nGlobal.", osNews},
	}
}

func (s *Services) os(c *Client, format string, a ...any) {
	s.svcReply(c, s.operServ, fmt.Sprintf(format, a...))
}

// audit notes who changed what, for the log and opers with +s.
func (s *Services) audit(c *Client, sv *service, format string, a ...any) {
	s.snotice("%s used %s: %s", c.user.Nick, sv.user.Nick, fmt.Sprintf(format, a...))
}

func durWords(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= 2*time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d >= time.Minute:
		return plural(int(d/time.Minute), "minute")
	}
	return plural(int(d/time.Second), "second")
}

func osStats(s *Services, c *Client, args []string) {
	extra := strings.ToUpper(argAt(args, 0))
	s.osLog(c, "%s", extra)
	switch extra {
	case "RESET":
		s.host.ResetPeak()
		s.os(c, "Statistics reset: the peak is now the number of people online.")
		return
	case "", "ALL", "AKILL", "HASH", "UPLINK", "UPTIME":
	default:
		s.os(c, "Unknown STATS option: %s (AKILL, HASH, UPLINK, UPTIME, ALL or RESET)", argAt(args, 0))
		return
	}
	people, opers := 0, 0
	for _, u := range s.net.Users() {
		if u.Service {
			continue
		}
		people++
		if u.HasMode('o') {
			opers++
		}
	}
	hs := s.host.Stats()
	if extra == "" || extra == "ALL" {
		accounts, nicks, memos, err := s.store.Counts()
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		regChans, err := s.store.ChannelCount()
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.os(c, "%s · %s · up %s", s.cfg.Server.Network, s.cfg.Server.Name, durWords(time.Since(hs.Started)))
		s.os(c, "  People     %d now, peak %d", people, hs.PeakUsers)
		s.os(c, "  Opers      %d online", opers)
		s.os(c, "  Channels   %d open, %d registered", s.net.ChannelCount(), regChans)
		s.os(c, "  Accounts   %d (%s), %s", accounts, plural(nicks, "nick"), plural(memos, "memo"))
		s.os(c, "  AKILLs     %d", len(s.liveAkills()))
		if n := len(s.liveBans(store.BanZline)) + len(s.liveBans(store.BanSNLine)) + len(s.liveBans(store.BanShun)) + len(s.liveBans(store.BanEline)); n > 0 {
			s.os(c, "  Other bans ZLINE %d, SNLINE %d, SHUN %d, ELINE %d", len(s.liveBans(store.BanZline)), len(s.liveBans(store.BanSNLine)),
				len(s.liveBans(store.BanShun)), len(s.liveBans(store.BanEline)))
		}
		s.os(c, "  Servers    1")
		s.os(c, "  Loop       %s", hs.Loop)
	}
	if extra == "ALL" {
		nForbid := 0
		for _, kind := range store.ForbidKinds {
			nForbid += len(s.forbids[kind])
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		s.os(c, "  Forbids    %d", nForbid)
		s.os(c, "  Exceptions %d (session limit %d)", len(s.sessionExcepts), s.cfg.Limits.SessionLimit)
		s.os(c, "  News       %s", plural(len(s.news), "line"))
		if hs.DNSBL != "" {
			s.os(c, "  DNSBL hits %s", hs.DNSBL)
		}
		s.os(c, "  Memory     %.1f MiB in use, %d goroutines", float64(mem.HeapAlloc)/(1<<20), runtime.NumGoroutine())
		s.os(c, "  Started    %s", showTime(hs.Started))
	}
	if extra == "AKILL" || extra == "ALL" {
		s.os(c, "Network bans (without +expiry they last %s; ELINEs stay until removed):", durWords(s.cfg.Services.BanExpiry.Duration))
		s.os(c, "  AKILL %d, SNLINE %d, SQLINE %d, ZLINE %d, SHUN %d, ELINE %d", len(s.liveBans(store.BanAkill)), len(s.liveBans(store.BanSNLine)),
			len(s.forbids[store.ForbidNick]), len(s.liveBans(store.BanZline)), len(s.liveBans(store.BanShun)), len(s.liveBans(store.BanEline)))
	}
	if extra == "HASH" || extra == "ALL" {
		accounts, nicks, _, err := s.store.Counts()
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		regChans, err := s.store.ChannelCount()
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.os(c, "Tables (entries): users %d, channels %d, registered channels %d, nicks %d, accounts %d, services records %d",
			len(s.net.Users()), s.net.ChannelCount(), regChans, nicks, accounts, len(s.clients))
	}
	if extra == "UPLINK" || extra == "ALL" {
		if len(hs.Links) == 0 {
			s.os(c, "Uplink: none, %s is not linked to any server.", s.cfg.Server.Name)
		}
		for _, line := range hs.Links {
			s.os(c, "%s", line)
		}
	}
	if extra == "UPTIME" {
		s.os(c, "Current users: %d (%d ops)", people, opers)
		s.os(c, "Maximum users: %d", hs.PeakUsers)
		s.os(c, "Services up %s.", durWords(time.Since(hs.Started)))
	}
}

func osGlobal(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'g') {
		return
	}
	text := strings.Join(args, " ")
	if text == "" {
		s.os(c, "Syntax: GLOBAL <text>")
		return
	}
	n := 0
	for _, oc := range s.clients {
		if !oc.closed {
			s.host.Notice(s.global.user.UID, oc.user.UID, text)
			n++
		}
	}
	s.audit(c, s.operServ, "GLOBAL %s", text)
	s.osLog(c, "")
	s.os(c, "Sent to everyone online (%s).", plural(n, "person"))
}

// parseExpiry reads Anope's "+7d" ("+0" for until removed; a bare number
// is days, as in Anope).
func parseExpiry(arg string) (time.Duration, bool) {
	if !strings.HasPrefix(arg, "+") {
		return 0, false
	}
	if arg == "+0" {
		return 0, true
	}
	num := arg[1:]
	if n, err := strconv.Atoi(num); err == nil && n > 0 {
		num += "d"
	}
	d, err := config.ParseDuration(num)
	if err != nil || d < time.Minute {
		return 0, false
	}
	return d, true
}

func osKill(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	if len(args) < 1 {
		s.os(c, "Syntax: KILL <nick> [reason]")
		return
	}
	reason := strings.Join(args[1:], " ")
	if reason == "" {
		reason = "No reason"
	}
	t := s.net.UserByNick(args[0])
	switch {
	case t == nil:
		s.os(c, "%s isn't online.", args[0])
	case t.Service:
		s.os(c, "%s is a service.", t.Nick)
	default:
		s.osLog(c, "on %s for %s", t.Nick, reason)
		s.killUser(t, "OperServ", reason+" ["+c.user.Nick+"]")
		s.os(c, "%s is disconnected.", t.Nick)
	}
}

func osMode(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 2 || !irc.IsChannel(args[0]) {
		s.os(c, "Syntax: MODE #chan <modes> | MODE #chan CLEAR [ALL]")
		return
	}
	ch := s.net.Channel(args[0])
	if ch == nil {
		s.os(c, "%s is empty right now.", args[0])
		return
	}
	if strings.EqualFold(args[1], "CLEAR") {
		all := strings.EqualFold(argAt(args, 2), "ALL")
		ranks := ""
		if all {
			ranks = "qaohv"
		}
		s.svcMode(s.operServ, ch, clearChanges(ch, ranks, "beI", true, true))
		s.audit(c, s.operServ, "MODE %s CLEAR%s", ch.Name, map[bool]string{true: " ALL"}[all])
		if all {
			s.os(c, "All modes cleared on %s.", ch.Name)
		} else {
			s.os(c, "Non-status modes cleared on %s.", ch.Name)
		}
		return
	}
	s.host.ApplyChannelModes(c.user.UID, s.operServ.user.UID, ch.Name, args[1], args[2:])
	modes, params := ch.ModeString(true)
	s.audit(c, s.operServ, "MODE %s %s", ch.Name, strings.Join(args[1:], " "))
	s.osLog(c, "%s on %s", strings.Join(args[1:], " "), ch.Name)
	s.os(c, "%s is now %s.", ch.Name, strings.TrimSpace(modes+" "+strings.Join(params, " ")))
}

func osKick(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 3 || !irc.IsChannel(args[0]) {
		s.os(c, "Syntax: KICK #chan <nick> <reason>")
		return
	}
	ch := s.net.Channel(args[0])
	t := s.net.UserByNick(args[1])
	if ch == nil || t == nil || ch.Member(t) == nil {
		s.os(c, "%s isn't in %s.", args[1], args[0])
		return
	}
	if t.Service {
		s.os(c, "%s is a service.", t.Nick)
		return
	}
	s.osLog(c, "on %s in %s (%s)", t.Nick, ch.Name, strings.Join(args[2:], " "))
	s.svcKick(s.operServ, ch, t, strings.Join(args[2:], " "))
	s.audit(c, s.operServ, "KICK %s %s", ch.Name, t.Nick)
	s.os(c, "%s is kicked from %s.", t.Nick, ch.Name)
}

// forbidKind reads NICK, CHAN, EMAIL or REGISTER.
func forbidKind(arg string) (string, bool) {
	switch strings.ToUpper(arg) {
	case "NICK":
		return store.ForbidNick, true
	case "CHAN":
		return store.ForbidChan, true
	case "EMAIL":
		return store.ForbidEmail, true
	case "REGISTER":
		return store.ForbidRegister, true
	}
	return "", false
}

func osForbid(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	kind, kindOK := forbidKind(argAt(args, 1))
	name := argAt(args, 2)
	switch {
	case sub == "LIST":
		osForbidList(s, c, argAt(args, 1))
	case (sub == "ADD" || sub == "DEL") && (!kindOK || name == ""):
		s.os(c, "Syntax: FORBID %s {NICK|CHAN|EMAIL|REGISTER} <name>%s", sub, map[bool]string{true: " [reason]"}[sub == "ADD"])
	case sub == "ADD":
		rest := args[2:]
		var life time.Duration
		if strings.HasPrefix(name, "+") {
			d, ok := parseExpiry(name)
			if !ok || len(rest) < 2 {
				s.os(c, "Syntax: FORBID ADD %s [+expiry] <name> [reason], expiry like +30m, +12h, +7d", strings.ToUpper(kind))
				return
			}
			life, rest = d, rest[1:]
		}
		osForbidAdd(s, c, kind, rest[0], strings.Join(rest[1:], " "), life)
	case sub == "DEL":
		gone, err := s.store.DelForbid(kind, name)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if !gone {
			s.os(c, "%s isn't forbidden.", name)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "FORBID DEL %s %s", strings.ToUpper(kind), name)
		if c.cmdName == "SQLINE" {
			s.osLog(c, "to remove %s from the list", name)
		} else {
			s.osLog(c, "to remove forbid on %s of type %s", name, strings.ToUpper(kind))
		}
		s.os(c, "%s can be used again.", name)
	default:
		s.os(c, "Syntax: FORBID ADD|DEL|LIST ... (/os help forbid)")
	}
}

func osForbidAdd(s *Services, c *Client, kind, name, reason string, life time.Duration) {
	wild := strings.ContainsAny(name, "*?")
	switch kind {
	case store.ForbidNick, store.ForbidRegister:
		if irc.IsChannel(name) && kind == store.ForbidRegister {
			break
		}
		if !wild && (!irc.ValidNick(name, s.cfg.Limits.NickLen) || isReserved(name)) {
			s.os(c, "%s isn't a nick that can be used.", name)
			return
		}
	case store.ForbidChan:
		if !irc.IsChannel(name) || !wild && !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
			s.os(c, "%s isn't a valid channel name.", name)
			return
		}
	case store.ForbidEmail:
		if !strings.Contains(name, "@") {
			s.os(c, "An email forbid looks like *@spam.example or bee@example.org.")
			return
		}
	}
	if !wild && (kind == store.ForbidNick || kind == store.ForbidChan) {
		if irc.IsChannel(name) {
			if reg, err := s.store.ChannelByName(name); err == nil && reg != nil {
				s.os(c, "%s is registered. Drop it first: /cs drop %s", reg.Name, reg.Name)
				return
			}
		} else if a, err := s.store.AccountByNick(name); err == nil && a != nil {
			s.os(c, "%s is registered. Drop it first: /ns drop %s", name, name)
			return
		}
	}
	if reason == "" {
		reason = "Not allowed here"
	}
	reason = irc.Truncate(reason, 200)
	f := store.Forbid{Name: name, Reason: reason, SetBy: c.user.Nick, SetAt: time.Now()}
	if life > 0 {
		f.Expires = f.SetAt.Add(life)
	}
	added, err := s.store.AddForbid(kind, f)
	if err != nil {
		s.dbErr(c, s.operServ, err)
		return
	}
	if !added {
		s.os(c, "%s is forbidden already.", name)
		return
	}
	s.reloadOperData()
	how := ""
	if life > 0 {
		how = " for " + durWords(life)
	}
	s.audit(c, s.operServ, "FORBID ADD %s %s%s (%s)", strings.ToUpper(kind), name, how, reason)
	if c.cmdName == "SQLINE" {
		affected, people := 0, 0
		for _, oc := range s.clients {
			if !oc.closed {
				people++
				if irc.Match(name, oc.user.Nick) {
					affected++
				}
			}
		}
		s.osLog(c, "%s", anopeLineAdd(name, reason, life, affected, people))
	} else {
		s.osLog(c, "to add a forbid on %s of type %s", name, strings.ToUpper(kind))
	}
	switch kind {
	case store.ForbidNick:
		for _, oc := range s.clients {
			if !oc.closed && !oc.isOper() && irc.Match(name, oc.user.Nick) {
				guest := s.guestNick()
				s.forceNick(oc, guest)
				s.notice(oc, "*** That nick is forbidden by the network, so you are now "+guest+".")
			}
		}
		s.os(c, "%s is forbidden now.", name)
	case store.ForbidChan:
		kicked := 0
		for _, ch := range s.net.Channels() {
			if !irc.Match(name, ch.Name) {
				continue
			}
			for _, m := range ch.SortedMembers() {
				if oc := s.clientOf(m.User); oc != nil && !oc.isOper() && s.net.Channel(ch.Name) != nil {
					s.svcKick(s.operServ, ch, m.User, "This channel is forbidden: "+reason)
					kicked++
				}
			}
		}
		s.os(c, "%s is forbidden now (%s kicked).", name, plural(kicked, "person"))
	case store.ForbidEmail:
		s.os(c, "No account may use %s now.", name)
	default:
		s.os(c, "%s can't be registered now.", name)
	}
}

func osForbidList(s *Services, c *Client, kindArg string) {
	kinds := store.ForbidKinds
	if kindArg != "" {
		kind, ok := forbidKind(kindArg)
		if !ok {
			s.os(c, "Syntax: FORBID LIST [NICK|CHAN|EMAIL|REGISTER]")
			return
		}
		kinds = []string{kind}
	}
	type row struct {
		kind string
		f    store.Forbid
	}
	var all []row
	width := 4
	for _, kind := range kinds {
		for _, f := range s.forbids[kind] {
			all = append(all, row{kind, f})
			width = max(width, len(f.Name))
		}
	}
	if len(all) == 0 {
		s.os(c, "Nothing is forbidden.")
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return irc.Fold(all[i].f.Name) < irc.Fold(all[j].f.Name) })
	s.os(c, "Forbidden:")
	for _, r := range all {
		left := ""
		if !r.f.Expires.IsZero() {
			left = durWords(time.Until(r.f.Expires)+30*time.Second) + " left · "
		}
		s.os(c, "  %-8s %-*s  by %s, %s · %s%s", strings.ToUpper(r.kind), width, r.f.Name, r.f.SetBy, r.f.SetAt.UTC().Format("2 Jan 2006"), left, r.f.Reason)
	}
	s.os(c, "%s.", plural(len(all), "entry"))
}

// sessionCounts is how many people each IP has on the network.
func (s *Services) sessionCounts() map[string]int {
	counts := map[string]int{}
	for _, oc := range s.clients {
		if !oc.closed {
			counts[oc.ip]++
		}
	}
	return counts
}

func osSession(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	counts := s.sessionCounts()
	type ipCount struct {
		ip string
		n  int
	}
	busiest := func(min int) []ipCount {
		var out []ipCount
		for ip, n := range counts {
			if n >= min {
				out = append(out, ipCount{ip, n})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].n > out[j].n || out[i].n == out[j].n && out[i].ip < out[j].ip })
		return out
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "":
		s.os(c, "Session limit: %d connections per IP (%s). Change it: /as set session-limit <n>",
			s.cfg.Limits.SessionLimit, plural(len(s.sessionExcepts), "exception"))
		busy := busiest(2)
		if len(busy) == 0 {
			s.os(c, "No IP has more than one connection.")
			return
		}
		s.os(c, "Busiest IPs:")
		for i, b := range busy {
			if i == 10 {
				break
			}
			s.os(c, "  %-40s %d of %d", b.ip, b.n, s.sessionLimit(b.ip))
		}
	case "LIST":
		min, err := strconv.Atoi(argAt(args, 1))
		if err != nil || min < 1 {
			s.os(c, "Syntax: SESSION LIST <threshold>, like SESSION LIST 3")
			return
		}
		busy := busiest(min)
		if len(busy) == 0 {
			s.os(c, "No IP has %d or more connections.", min)
			return
		}
		s.os(c, "IPs with %d or more connections:", min)
		for i, b := range busy {
			if i == maxAkillList {
				break
			}
			s.os(c, "  %-40s %d of %d", b.ip, b.n, s.sessionLimit(b.ip))
		}
		s.os(c, "%s.", plural(len(busy), "IP"))
	case "VIEW":
		ip := argAt(args, 1)
		if ip == "" {
			s.os(c, "Syntax: SESSION VIEW <ip>")
			return
		}
		s.os(c, "%s has %d of %d connections.", ip, counts[ip], s.sessionLimit(ip))
	case "LIMIT":
		s.os(c, "The limit is an AdminServ setting now: /as set session-limit <n>")
	case "EXCEPT":
		osException(s, c, args[1:])
	default:
		s.os(c, "Syntax: SESSION [LIST <threshold> | VIEW <ip>]")
	}
}

func osException(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	if sub == "BAN" {
		s.netBanCommand(c, banKinds[store.BanEline], args[1:])
		return
	}
	list := s.sessionExcepts
	switch sub {
	case "ADD":
		rest := args[1:]
		var life time.Duration
		if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
			d, ok := parseExpiry(rest[0])
			if !ok {
				s.os(c, "%s isn't a time. Use +30m, +12h, +7d, or leave it out for until removed.", rest[0])
				return
			}
			life, rest = d, rest[1:]
		}
		mask := strings.ToLower(argAt(rest, 0))
		n, err := strconv.Atoi(argAt(rest, 1))
		if mask == "" || err != nil || n < 0 || n > 10000 || strings.ContainsAny(mask, "@! ") || strings.Contains(mask, "/") && !irc.IsCIDR(mask) {
			s.os(c, "Syntax: EXCEPTION ADD [+expiry] <ip-mask> <limit> [reason], like 192.168.50.* 20")
			return
		}
		e := store.SessionExcept{Mask: mask, Max: n, Reason: irc.Truncate(strings.Join(rest[2:], " "), 200), SetBy: c.user.Nick, SetAt: time.Now()}
		if life > 0 {
			e.Expires = e.SetAt.Add(life)
		}
		if err := s.store.SaveSessionExcept(e); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "EXCEPTION ADD %s %d", mask, n)
		s.osLog(c, "to set the session limit for %s to %d", mask, n)
		if n == 0 {
			s.os(c, "%s may now have any number of connections.", mask)
		} else {
			s.os(c, "%s may now have %d connections.", mask, n)
		}
	case "DEL":
		arg := strings.ToLower(argAt(args, 1))
		if arg == "" {
			s.os(c, "Syntax: EXCEPTION DEL {<ip-mask> | <num> | <list>}")
			return
		}
		masks := []string{arg}
		if nums, isList := numList(arg, len(list)); isList {
			masks = nil
			for _, n := range nums {
				masks = append(masks, list[n-1].Mask)
			}
		}
		var gone []string
		for _, m := range masks {
			ok, err := s.store.DelSessionExcept(m)
			if err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			if ok {
				gone = append(gone, m)
			}
		}
		if len(gone) == 0 {
			s.os(c, "No session exception for %s.", arg)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "EXCEPTION DEL %s", strings.Join(gone, " "))
		for _, m := range gone {
			s.osLog(c, "to remove the session limit exception for %s", m)
		}
		s.os(c, "%s back to the normal limit (%d).", strings.Join(gone, ", ")+map[bool]string{true: " is", false: " are"}[len(gone) == 1], s.cfg.Limits.SessionLimit)
	case "LIST", "VIEW":
		if len(list) == 0 {
			s.os(c, "No session exceptions. Everyone may have %d connections.", s.cfg.Limits.SessionLimit)
			return
		}
		show := pickEntries(argAt(args, 1), len(list), func(i int) string { return list[i].Mask })
		if len(show) == 0 {
			s.os(c, "No session exception matches %s.", argAt(args, 1))
			return
		}
		s.os(c, "Session exceptions:")
		for _, i := range show {
			e := list[i]
			if sub == "VIEW" {
				s.os(c, "  %-3d %-20s %-4s set by %s, %s · %s", i+1, e.Mask, limitText(e.Max), e.SetBy, showTime(e.SetAt), e.Reason)
			} else {
				s.os(c, "  %-3d %-20s %-4s by %s · %s", i+1, e.Mask, limitText(e.Max), e.SetBy, e.Reason)
			}
		}
		s.os(c, "%s.", plural(len(show), "exception"))
	default:
		s.os(c, "Syntax: EXCEPTION ADD|DEL|LIST|VIEW ... (/os help exception)")
	}
}

// newsKinds are the three news lists (Anope's os_news), by command.
var newsKinds = map[string]struct{ kind, word, when string }{
	"LOGONNEWS":  {store.NewsLogon, "logon", "Everyone sees the newest 3 when they connect, from Global."},
	"OPERNEWS":   {store.NewsOper, "oper", "Opers see the newest 3 when they oper up, from OperServ."},
	"RANDOMNEWS": {store.NewsRandom, "random", "Everyone gets one of these when they connect (in turn), from Global."},
}

// newsShown is how many logon and oper news lines people get (Anope's
// newscount).
const newsShown = 3

// newsOf returns the news lines of one kind, oldest first.
func (s *Services) newsOf(kind string) []store.News {
	var out []store.News
	for _, n := range s.news {
		if n.Kind == kind {
			out = append(out, n)
		}
	}
	return out
}

func osNews(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'g') {
		return
	}
	cmd := strings.ToUpper(c.cmdName)
	if real, ok := s.operServ.aliases[cmd]; ok {
		cmd = real
	}
	k, ok := newsKinds[cmd]
	if !ok {
		k = newsKinds["LOGONNEWS"]
		cmd = "LOGONNEWS"
	}
	list := s.newsOf(k.kind)
	low := strings.ToLower(cmd)
	switch strings.ToUpper(argAt(args, 0)) {
	case "ADD":
		text := strings.Join(args[1:], " ")
		if text == "" {
			s.os(c, "Syntax: %s ADD <text>", cmd)
			return
		}
		if len(list) >= maxNews {
			s.os(c, "There are %d %s news lines already, the most. Remove one first: /os %s del <num>", maxNews, k.word, low)
			return
		}
		if err := s.store.AddNews(k.kind, irc.Truncate(text, 300), c.user.Nick, time.Now()); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "%s ADD %s", cmd, text)
		s.osLog(c, "to add a news item")
		s.os(c, "Added new %s news item. %s", k.word, k.when)
	case "DEL":
		arg := argAt(args, 1)
		var picks []store.News
		if strings.EqualFold(arg, "ALL") {
			picks = list
		} else if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(list) {
			picks = []store.News{list[n-1]}
		} else {
			s.os(c, "Syntax: %s DEL {<num> | ALL} (see /os %s list)", cmd, low)
			return
		}
		if len(picks) == 0 {
			s.os(c, "No %s news items to delete.", k.word)
			return
		}
		for _, n := range picks {
			if _, err := s.store.DelNews(n.ID); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			s.audit(c, s.operServ, "%s DEL %s", cmd, n.Text)
		}
		s.reloadOperData()
		if strings.EqualFold(arg, "ALL") {
			s.osLog(c, "to delete all news items")
			s.os(c, "All %s news items deleted (%d).", k.word, len(picks))
		} else {
			s.osLog(c, "to delete a news item")
			s.os(c, "%s news item #%s deleted.", upperFirst(k.word), arg)
		}
	case "LIST", "":
		if len(list) == 0 {
			s.os(c, "There is no %s news.", k.word)
			return
		}
		s.os(c, "%s news items:", upperFirst(k.word))
		for i, n := range list {
			s.os(c, "  %-3d %s  by %s, %s", i+1, n.Text, n.SetBy, n.SetAt.UTC().Format("2 Jan 2006"))
		}
		s.os(c, "%s.", plural(len(list), "line"))
	default:
		s.os(c, "Syntax: %s ADD|DEL|LIST ...", cmd)
	}
}

func upperFirst(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// showNews sends the newest newsShown lines of kind (all for random: one
// in turn) to uid from bot, Anope's way: [Logon News - time] text.
func (s *Services) showNews(uid string, bot *service, kind, label string) {
	list := s.newsOf(kind)
	if len(list) == 0 {
		return
	}
	if kind == store.NewsRandom {
		n := list[s.randNews%len(list)]
		s.randNews = (s.randNews + 1) % len(list)
		s.host.Notice(bot.user.UID, uid, "[\x02"+label+"\x02 - "+showTime(n.SetAt)+"] "+n.Text)
		return
	}
	if len(list) > newsShown {
		list = list[len(list)-newsShown:]
	}
	for _, n := range list {
		s.host.Notice(bot.user.UID, uid, "[\x02"+label+"\x02 - "+showTime(n.SetAt)+"] "+n.Text)
	}
}
