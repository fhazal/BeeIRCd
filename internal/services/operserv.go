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
		"STATS": {"STATS [ALL]", "people, channels, opers, uptime",
			"Shows how busy the network is. STATS ALL adds the details. Any oper.", osStats},
		"GLOBAL": {"GLOBAL <text>", "a notice to everyone",
			"Sends a notice from Global to everyone on the network.\nExample: /os global The server restarts at 22:00 UTC.", osGlobal},
		"AKILL": {"AKILL ADD|DEL|LIST|VIEW|CLEAR ...", "ban from the whole network",
			"AKILL ADD [+expiry] <user@host|nick> <reason>   expiry like +30m, +12h, +7d;\n    without it the AKILL stays until removed\nAKILL DEL {<mask> | <num> | <list>}   numbers from LIST, like 2 or 1-3,5\nAKILL LIST [<mask> | <list>]\nAKILL VIEW [<mask> | <list>]   with who set it and when\nAKILL CLEAR\nA nick bans that person's IP. People who match are disconnected at once.\nExample: /os akill add +7d *@203.0.113.* spam bots", osAkill},
		"KILL": {"KILL <nick> [reason]", "disconnect someone",
			"Disconnects someone, with OperServ as the sender.", osKill},
		"MODE": {"MODE #chan <modes>", "set channel modes as services",
			"Sets modes on a channel without being an op there.\nExample: /os mode #bchat -i+b *!*@spam.example", osMode},
		"KICK": {"KICK #chan <nick> <reason>", "kick as services", "Kicks someone with OperServ as the sender.", osKick},
		"FORBID": {"FORBID ADD|DEL|LIST ...", "names nobody may use",
			"FORBID ADD {NICK|CHAN|EMAIL|REGISTER} <name> [reason]\nFORBID DEL {NICK|CHAN|EMAIL|REGISTER} <name>\nFORBID LIST [NICK|CHAN|EMAIL|REGISTER]\nNICK: nobody but opers may use or register it. CHAN: nobody but opers may\njoin or register it. EMAIL: no account may use it. REGISTER: may be used, but\nnot registered. Names may hold * and ?, like *@mailinator.com or Guest*.", osForbid},
		"SESSION": {"SESSION [LIST <threshold> | VIEW <ip>]", "connections per IP",
			"SESSION                 the limit and the busiest IPs\nSESSION LIST <n>        every IP with at least n connections\nSESSION VIEW <ip>       how many connections an IP has, and its limit\nChange the limit: /as set session-limit <n>. Exceptions: /os exception", osSession},
		"EXCEPTION": {"EXCEPTION ADD|DEL|LIST|VIEW ...", "IPs allowed more connections",
			"EXCEPTION ADD <ip-mask> <limit> [reason]\nEXCEPTION DEL {<ip-mask> | <num> | <list>}\nEXCEPTION LIST [<ip-mask> | <list>]\nEXCEPTION VIEW [<ip-mask> | <list>]   with who set it and when\nExample: /os exception add 192.168.50.* 20 home network", osException},
		"LOGONNEWS": {"LOGONNEWS ADD|DEL|LIST ...", "lines shown after the MOTD",
			"LOGONNEWS ADD <text>\nLOGONNEWS DEL {<num> | ALL}\nLOGONNEWS LIST\nEveryone sees the news when they connect, from Global.", osNews},
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
	hs := s.host.Stats()
	s.os(c, "%s · %s · up %s", s.cfg.Server.Network, s.cfg.Server.Name, durWords(time.Since(hs.Started)))
	s.os(c, "  People     %d now, peak %d", people, hs.PeakUsers)
	s.os(c, "  Opers      %d online", opers)
	s.os(c, "  Channels   %d open, %d registered", s.net.ChannelCount(), regChans)
	s.os(c, "  Accounts   %d (%s), %s", accounts, plural(nicks, "nick"), plural(memos, "memo"))
	s.os(c, "  AKILLs     %d", len(s.liveAkills()))
	s.os(c, "  Servers    1")
	s.os(c, "  Loop       %s", hs.Loop)
	if !strings.EqualFold(argAt(args, 0), "ALL") {
		return
	}
	nForbid := 0
	for _, kind := range store.ForbidKinds {
		nForbid += len(s.forbids[kind])
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	s.os(c, "  Forbids    %d", nForbid)
	s.os(c, "  Exceptions %d (session limit %d)", len(s.sessionExcepts), s.cfg.Limits.SessionLimit)
	s.os(c, "  News       %s", plural(len(s.news), "line"))
	s.os(c, "  Memory     %.1f MiB in use, %d goroutines", float64(mem.HeapAlloc)/(1<<20), runtime.NumGoroutine())
	s.os(c, "  Started    %s", showTime(hs.Started))
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
	s.os(c, "Sent to everyone online (%s).", plural(n, "person"))
}

// liveAkills are the AKILLs that haven't run out.
func (s *Services) liveAkills() []store.Akill {
	now := time.Now()
	var out []store.Akill
	for _, k := range s.akills {
		if k.Expires.IsZero() || now.Before(k.Expires) {
			out = append(out, k)
		}
	}
	return out
}

// akillMask turns a nick, host or user@host into a user@host mask.
func (s *Services) akillMask(arg string) (string, string) {
	if !strings.Contains(arg, "@") {
		if u := s.net.UserByNick(arg); u != nil && !u.Service && u.IP != "" {
			return "*@" + irc.DisplayIP(u.IP), ""
		}
		if irc.ValidNick(arg, s.cfg.Limits.NickLen) && !strings.ContainsAny(arg, ".*?:") {
			return "", arg + " isn't online. Give a mask like *@203.0.113.5 instead."
		}
		arg = "*@" + arg
	}
	user, host, _ := strings.Cut(arg, "@")
	if user == "" || host == "" || strings.ContainsAny(arg, "! ,") || strings.Count(arg, "@") > 1 {
		return "", "A mask looks like *@203.0.113.* or ~bot@*.example.net."
	}
	if strings.Trim(host, "*?.:") == "" {
		return "", "That mask matches everyone. Give a narrower one."
	}
	return strings.ToLower(arg), ""
}

// parseExpiry reads Anope's "+7d" (and "+0" for until removed).
func parseExpiry(arg string) (time.Duration, bool) {
	if !strings.HasPrefix(arg, "+") {
		return 0, false
	}
	if arg == "+0" {
		return 0, true
	}
	d, err := config.ParseDuration(arg[1:])
	if err != nil || d < time.Minute {
		return 0, false
	}
	return d, true
}

func osAkill(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	live := s.liveAkills()
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
		if len(rest) < 2 {
			s.os(c, "Syntax: AKILL ADD [+expiry] <user@host|nick> <reason>")
			return
		}
		mask, problem := s.akillMask(rest[0])
		if problem != "" {
			s.os(c, "%s", problem)
			return
		}
		u := c.user
		if irc.Match(mask, u.Ident+"@"+u.RealHost) || irc.Match(mask, u.Ident+"@"+u.IP) || irc.Match(mask, u.Ident+"@"+u.Host) {
			s.os(c, "%s matches you. Give a narrower one.", mask)
			return
		}
		var hits []*Client
		people := 0
		for _, oc := range s.clients {
			if oc.closed {
				continue
			}
			people++
			ou := oc.user
			if irc.Match(mask, ou.Ident+"@"+ou.RealHost) || irc.Match(mask, ou.Ident+"@"+ou.IP) || irc.Match(mask, ou.Ident+"@"+ou.Host) {
				hits = append(hits, oc)
			}
		}
		if len(hits) > 1 && len(hits) == people {
			s.os(c, "%s matches everyone online. Give a narrower one.", mask)
			return
		}
		now := time.Now()
		k := store.Akill{Mask: mask, Reason: irc.Truncate(strings.Join(rest[1:], " "), 200), SetBy: u.Nick, SetAt: now}
		if life > 0 {
			k.Expires = now.Add(life)
		}
		if err := s.store.AddAkill(k); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		how := "until removed"
		if life > 0 {
			how = "for " + durWords(life)
		}
		s.audit(c, s.operServ, "AKILL ADD %s %s (%s)", mask, how, k.Reason)
		for _, oc := range hits {
			s.reply(oc, irc.ErrYoureBannedCreep, "You are banned from this network: "+k.Reason)
			s.quit(oc, "Banned: "+k.Reason)
		}
		s.os(c, "AKILL added: %s %s (%s). %s removed.", mask, how, k.Reason, plural(len(hits), "person"))
	case "DEL":
		arg := argAt(args, 1)
		if arg == "" {
			s.os(c, "Syntax: AKILL DEL {<mask> | <num> | <list>}")
			return
		}
		var masks []string
		if nums, isList := numList(arg, len(live)); isList {
			for _, n := range nums {
				masks = append(masks, live[n-1].Mask)
			}
		} else {
			m := strings.ToLower(arg)
			if !strings.Contains(m, "@") {
				m = "*@" + m
			}
			masks = []string{m}
		}
		var gone []string
		for _, m := range masks {
			ok, err := s.store.DelAkill(m)
			if err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			if ok {
				gone = append(gone, m)
			}
		}
		if len(gone) == 0 {
			s.os(c, "No AKILL on %s. See them all: /os akill list", arg)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "AKILL DEL %s", strings.Join(gone, " "))
		if len(gone) == 1 {
			s.os(c, "AKILL on %s removed.", gone[0])
		} else {
			s.os(c, "%s removed: %s.", plural(len(gone), "AKILL"), strings.Join(gone, ", "))
		}
	case "LIST", "VIEW":
		show := pickEntries(argAt(args, 1), len(live), func(i int) string { return live[i].Mask })
		if len(show) == 0 {
			s.os(c, "No AKILLs.")
			return
		}
		s.os(c, "AKILLs:")
		for n, i := range show {
			if n == maxAkillList {
				break
			}
			k := live[i]
			left := "until removed"
			if !k.Expires.IsZero() {
				left = durWords(time.Until(k.Expires)+30*time.Second) + " left"
			}
			if sub == "VIEW" {
				s.os(c, "  %-3d %s  set by %s, %s · %s · %s", i+1, k.Mask, k.SetBy, showTime(k.SetAt), left, k.Reason)
			} else {
				s.os(c, "  %-3d %s  by %s · %s · %s", i+1, k.Mask, k.SetBy, left, k.Reason)
			}
		}
		s.os(c, "%s.", plural(len(show), "AKILL"))
	case "CLEAR":
		for _, k := range s.akills {
			if _, err := s.store.DelAkill(k.Mask); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
		}
		n := len(live)
		s.reloadOperData()
		s.audit(c, s.operServ, "AKILL CLEAR (%d)", n)
		s.os(c, "Every AKILL is removed (%d).", n)
	default:
		s.os(c, "Syntax: AKILL ADD|DEL|LIST|VIEW|CLEAR ... (/os help akill)")
	}
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
		s.killUser(t, "OperServ", reason+" ["+c.user.Nick+"]")
		s.os(c, "%s is disconnected.", t.Nick)
	}
}

func osMode(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 2 || !irc.IsChannel(args[0]) {
		s.os(c, "Syntax: MODE #chan <modes>")
		return
	}
	ch := s.net.Channel(args[0])
	if ch == nil {
		s.os(c, "%s is empty right now.", args[0])
		return
	}
	s.host.ApplyChannelModes(c.user.UID, s.operServ.user.UID, ch.Name, args[1], args[2:])
	modes, params := ch.ModeString(true)
	s.audit(c, s.operServ, "MODE %s %s", ch.Name, strings.Join(args[1:], " "))
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
		osForbidAdd(s, c, kind, name, strings.Join(args[3:], " "))
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
		s.os(c, "%s can be used again.", name)
	default:
		s.os(c, "Syntax: FORBID ADD|DEL|LIST ... (/os help forbid)")
	}
}

func osForbidAdd(s *Services, c *Client, kind, name, reason string) {
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
	added, err := s.store.AddForbid(kind, store.Forbid{Name: name, Reason: reason, SetBy: c.user.Nick, SetAt: time.Now()})
	if err != nil {
		s.dbErr(c, s.operServ, err)
		return
	}
	if !added {
		s.os(c, "%s is forbidden already.", name)
		return
	}
	s.reloadOperData()
	s.audit(c, s.operServ, "FORBID ADD %s %s (%s)", strings.ToUpper(kind), name, reason)
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
		s.os(c, "  %-8s %-*s  by %s, %s · %s", strings.ToUpper(r.kind), width, r.f.Name, r.f.SetBy, r.f.SetAt.UTC().Format("2 Jan 2006"), r.f.Reason)
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
	list := s.sessionExcepts
	switch sub {
	case "ADD":
		mask := strings.ToLower(argAt(args, 1))
		n, err := strconv.Atoi(argAt(args, 2))
		if mask == "" || err != nil || n < 1 || n > 10000 || strings.ContainsAny(mask, "@! ") {
			s.os(c, "Syntax: EXCEPTION ADD <ip-mask> <limit> [reason], like 192.168.50.* 20")
			return
		}
		e := store.SessionExcept{Mask: mask, Max: n, Reason: irc.Truncate(strings.Join(args[3:], " "), 200), SetBy: c.user.Nick, SetAt: time.Now()}
		if err := s.store.SaveSessionExcept(e); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "EXCEPTION ADD %s %d", mask, n)
		s.os(c, "%s may now have %d connections.", mask, n)
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
				s.os(c, "  %-3d %-20s %-4d set by %s, %s · %s", i+1, e.Mask, e.Max, e.SetBy, showTime(e.SetAt), e.Reason)
			} else {
				s.os(c, "  %-3d %-20s %-4d by %s · %s", i+1, e.Mask, e.Max, e.SetBy, e.Reason)
			}
		}
		s.os(c, "%s.", plural(len(show), "exception"))
	default:
		s.os(c, "Syntax: EXCEPTION ADD|DEL|LIST|VIEW ... (/os help exception)")
	}
}

func osNews(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'g') {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "ADD":
		text := strings.Join(args[1:], " ")
		if text == "" {
			s.os(c, "Syntax: LOGONNEWS ADD <text>")
			return
		}
		if len(s.news) >= maxNews {
			s.os(c, "There are %d news lines already, the most. Remove one first: /os logonnews del <num>", maxNews)
			return
		}
		if err := s.store.AddNews(irc.Truncate(text, 300), c.user.Nick, time.Now()); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "LOGONNEWS ADD %s", text)
		s.os(c, "News added. Everyone sees it when they connect.")
	case "DEL":
		arg := argAt(args, 1)
		var picks []store.News
		if strings.EqualFold(arg, "ALL") {
			picks = s.news
		} else if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(s.news) {
			picks = []store.News{s.news[n-1]}
		} else {
			s.os(c, "Syntax: LOGONNEWS DEL {<num> | ALL} (see /os logonnews list)")
			return
		}
		for _, n := range picks {
			if _, err := s.store.DelNews(n.ID); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			s.audit(c, s.operServ, "LOGONNEWS DEL %s", n.Text)
		}
		s.reloadOperData()
		if len(picks) == 1 && !strings.EqualFold(arg, "ALL") {
			s.os(c, "News line %s removed.", arg)
		} else {
			s.os(c, "All news removed (%s).", plural(len(picks), "line"))
		}
	case "LIST":
		if len(s.news) == 0 {
			s.os(c, "No news.")
			return
		}
		s.os(c, "News:")
		for i, n := range s.news {
			s.os(c, "  %-3d %s  by %s, %s", i+1, n.Text, n.SetBy, n.SetAt.UTC().Format("2 Jan 2006"))
		}
		s.os(c, "%s.", plural(len(s.news), "line"))
	default:
		s.os(c, "Syntax: LOGONNEWS ADD|DEL|LIST ...")
	}
}
