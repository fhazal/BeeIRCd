package services

import (
	"fmt"
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

func operServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"STATS": {"STATS", "people, channels, opers, uptime",
			"Shows how busy the network is. Any oper.", osStats},
		"GLOBAL": {"GLOBAL <text>", "(g) a notice to everyone",
			"Sends a notice from Global to everyone on the network.\nExample: /os global The server restarts at 22:00 UTC.", osGlobal},
		"AKILL": {"AKILL ADD|DEL|LIST ...", "(k) ban from the whole network",
			"AKILL ADD <user@host|nick> <time> <reason>   time like 30m, 12h, 7d, or 0 for until removed\nAKILL DEL <mask|number>\nAKILL LIST [pattern]\nA nick bans that person's IP. People who match are disconnected at once.\nExample: /os akill add *@203.0.113.* 7d spam bots", osAkill},
		"KILL": {"KILL <nick> <reason>", "(k) disconnect someone",
			"Disconnects someone, with OperServ as the sender.", osKill},
		"MODE": {"MODE #chan <modes>", "(s) set channel modes as services",
			"Sets modes on a channel without being an op there.\nExample: /os mode #bchat -i+b *!*@spam.example", osMode},
		"KICK": {"KICK #chan <nick> <reason>", "(s) kick as services", "Kicks someone with OperServ as the sender.", osKick},
		"NICK": {"NICK SETPASS|DROP|FORBID|UNFORBID <nick> ...", "(s) nick tools",
			"NICK SETPASS <nick> <password>   a new password for a forgotten one\nNICK DROP <nick>                 removes the account that owns the nick\nNICK FORBID <nick> [reason]      nobody can use or register it\nNICK UNFORBID <nick>", osNick},
		"CHAN": {"CHAN DROP|FORBID|UNFORBID|SETFOUNDER #chan ...", "(s) channel tools",
			"CHAN DROP #chan                  removes the registration\nCHAN FORBID #chan [reason]       nobody but opers can join or register it\nCHAN UNFORBID #chan\nCHAN SETFOUNDER #chan <nick>     hands the channel to someone", osChan},
		"FORBIDS": {"FORBIDS", "(s) forbidden nicks and channels", "Lists every forbidden nick and channel.", osForbids},
		"SESSION": {"SESSION [LIMIT <n> | EXCEPT ADD|DEL|LIST ...]", "(k) connections per IP",
			"SESSION                              the limit and the busiest IPs\nSESSION LIMIT <n>                    connections allowed from one IP\nSESSION EXCEPT ADD <ip-mask> <n> [reason]\nSESSION EXCEPT DEL <ip-mask>\nSESSION EXCEPT LIST\nExample: /os session except add 192.168.50.* 20 home network", osSession},
		"NEWS": {"NEWS ADD|DEL|LIST ...", "(g) lines shown after the MOTD",
			"NEWS ADD <text>\nNEWS DEL <number>\nNEWS LIST\nEveryone sees the news when they connect, from Global.", osNews},
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

func osAkill(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "ADD":
		if len(args) < 4 {
			s.os(c, "Syntax: AKILL ADD <user@host|nick> <time> <reason>")
			return
		}
		mask, problem := s.akillMask(args[1])
		if problem != "" {
			s.os(c, "%s", problem)
			return
		}
		var life time.Duration
		if args[2] != "0" {
			d, err := config.ParseDuration(args[2])
			if err != nil || d < time.Minute {
				s.os(c, "%s isn't a time. Use 30m, 12h, 7d, or 0 for until removed.", args[2])
				return
			}
			life = d
		}
		u := c.user
		if irc.Match(mask, u.Ident+"@"+u.RealHost) || irc.Match(mask, u.Ident+"@"+u.IP) || irc.Match(mask, u.Ident+"@"+u.Host) {
			s.os(c, "%s matches you. Give a narrower one.", mask)
			return
		}
		var hits []*Client
		people := 0
		for _, oc := range s.clients {
			if oc.user == nil || oc.closed {
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
		k := store.Akill{Mask: mask, Reason: irc.Truncate(strings.Join(args[3:], " "), 200), SetBy: u.Nick, SetAt: now}
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
			s.os(c, "Syntax: AKILL DEL <mask|number>")
			return
		}
		mask := strings.ToLower(arg)
		live := s.liveAkills()
		if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(live) {
			mask = live[n-1].Mask
		} else if !strings.Contains(mask, "@") {
			mask = "*@" + mask
		}
		gone, err := s.store.DelAkill(mask)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if !gone {
			s.os(c, "No AKILL on %s. See them all: /os akill list", mask)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "AKILL DEL %s", mask)
		s.os(c, "AKILL on %s removed.", mask)
	case "LIST":
		pattern := argAt(args, 1)
		live := s.liveAkills()
		shown := 0
		for i, k := range live {
			if pattern != "" && !irc.Match(pattern, k.Mask) {
				continue
			}
			if shown == 0 {
				s.os(c, "AKILLs:")
			}
			shown++
			if shown > maxAkillList {
				continue
			}
			left := "until removed"
			if !k.Expires.IsZero() {
				left = durWords(time.Until(k.Expires)+30*time.Second) + " left"
			}
			s.os(c, "  %-3d %s  by %s · %s · %s", i+1, k.Mask, k.SetBy, left, k.Reason)
		}
		if shown == 0 {
			s.os(c, "No AKILLs.")
			return
		}
		s.os(c, "%s.", plural(shown, "AKILL"))
	default:
		s.os(c, "Syntax: AKILL ADD|DEL|LIST ... (/os help akill)")
	}
}

func osKill(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	if len(args) < 2 {
		s.os(c, "Syntax: KILL <nick> <reason>")
		return
	}
	t := s.net.UserByNick(args[0])
	switch {
	case t == nil:
		s.os(c, "%s isn't online.", args[0])
	case t.Service:
		s.os(c, "%s is a service.", t.Nick)
	default:
		s.killUser(t, "OperServ", strings.Join(args[1:], " ")+" ["+c.user.Nick+"]")
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

func osNick(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	sub, nick := strings.ToUpper(argAt(args, 0)), argAt(args, 1)
	if nick == "" {
		s.os(c, "Syntax: NICK SETPASS|DROP|FORBID|UNFORBID <nick> ... (/os help nick)")
		return
	}
	switch sub {
	case "SETPASS", "DROP":
		a, err := s.store.AccountByNick(nick)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if a == nil {
			s.os(c, "%s isn't a registered nick.", nick)
			return
		}
		if s.isAdminAccount(a) && c.accountID != a.ID {
			s.os(c, "%s is the server admin from beeircd.conf. Only they can change it.", a.Name)
			return
		}
		if sub == "DROP" {
			name := a.Name
			if err := s.dropAccount(a, "founder "+name+" was dropped by "+c.user.Nick, "by "+c.user.Nick+" through OperServ"); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			s.audit(c, s.operServ, "NICK DROP %s", name)
			s.os(c, "The account %s is gone.", name)
			return
		}
		pw := argAt(args, 2)
		if len(pw) < minPasswordLen {
			s.os(c, "Syntax: NICK SETPASS <nick> <password> (at least %d characters)", minPasswordLen)
			return
		}
		id, name := a.ID, a.Name
		s.hashPassword(c, pw, func(hash string) {
			fresh, err := s.store.AccountByID(id)
			if err != nil || fresh == nil {
				s.os(c, "%s is gone.", name)
				return
			}
			fresh.PassHash = hash
			if err := s.store.SaveAccount(fresh); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			delete(s.lockouts, id)
			s.audit(c, s.operServ, "NICK SETPASS %s", name)
			s.os(c, "%s has a new password. Tell them to change it: /ns set password <new>", name)
		})
	case "FORBID":
		if !irc.ValidNick(nick, s.cfg.Limits.NickLen) || isReserved(nick) {
			s.os(c, "%s isn't a nick that can be used.", nick)
			return
		}
		if a, err := s.store.AccountByNick(nick); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		} else if a != nil {
			s.os(c, "%s is registered. Drop it first: /os nick drop %s", nick, nick)
			return
		}
		reason := strings.Join(args[2:], " ")
		if reason == "" {
			reason = "Not allowed here"
		}
		added, err := s.store.AddForbid(store.ForbidNick, store.Forbid{Name: nick, Reason: irc.Truncate(reason, 200), SetBy: c.user.Nick, SetAt: time.Now()})
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if !added {
			s.os(c, "%s is forbidden already.", nick)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "NICK FORBID %s (%s)", nick, reason)
		if u := s.net.UserByNick(nick); u != nil {
			if oc := s.clientOf(u); oc != nil && !oc.isOper() {
				guest := s.guestNick()
				s.forceNick(oc, guest)
				s.notice(oc, "*** That nick is forbidden by the network, so you are now "+guest+".")
			}
		}
		s.os(c, "%s is forbidden now.", nick)
	case "UNFORBID":
		s.unforbid(c, store.ForbidNick, nick)
	default:
		s.os(c, "Syntax: NICK SETPASS|DROP|FORBID|UNFORBID <nick> ... (/os help nick)")
	}
}

func (s *Services) unforbid(c *Client, kind, name string) {
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
	s.audit(c, s.operServ, "%s UNFORBID %s", strings.ToUpper(kind), name)
	s.os(c, "%s can be used again.", name)
}

func osChan(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	sub, name := strings.ToUpper(argAt(args, 0)), argAt(args, 1)
	if !irc.IsChannel(name) {
		s.os(c, "Syntax: CHAN DROP|FORBID|UNFORBID|SETFOUNDER #chan ... (/os help chan)")
		return
	}
	switch sub {
	case "DROP", "SETFOUNDER":
		reg, err := s.store.ChannelByName(name)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if reg == nil {
			s.os(c, "%s isn't registered.", name)
			return
		}
		if sub == "DROP" {
			if err := s.store.DeleteChannel(reg.ID); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			if live := s.net.Channel(reg.Name); live != nil {
				s.csMode(live, []modeChange{{false, 'r', ""}})
			}
			s.audit(c, s.operServ, "CHAN DROP %s", reg.Name)
			s.os(c, "%s is no longer registered.", reg.Name)
			return
		}
		a, err := s.store.AccountByNick(argAt(args, 2))
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if a == nil {
			s.os(c, "Syntax: CHAN SETFOUNDER #chan <registered nick>")
			return
		}
		if a.ID == reg.FounderID {
			s.os(c, "%s is the founder of %s already.", a.Name, reg.Name)
			return
		}
		if err := s.store.SetFounder(reg, a); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.audit(c, s.operServ, "CHAN SETFOUNDER %s %s (was %s)", reg.Name, a.Name, reg.Founder)
		s.os(c, "The founder of %s is now %s (was %s).", reg.Name, a.Name, reg.Founder)
	case "FORBID":
		if reg, err := s.store.ChannelByName(name); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		} else if reg != nil {
			s.os(c, "%s is registered. Drop it first: /os chan drop %s", reg.Name, reg.Name)
			return
		}
		if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
			s.os(c, "%s isn't a valid channel name.", name)
			return
		}
		reason := strings.Join(args[2:], " ")
		if reason == "" {
			reason = "Not allowed here"
		}
		added, err := s.store.AddForbid(store.ForbidChan, store.Forbid{Name: name, Reason: irc.Truncate(reason, 200), SetBy: c.user.Nick, SetAt: time.Now()})
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if !added {
			s.os(c, "%s is forbidden already.", name)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "CHAN FORBID %s (%s)", name, reason)
		kicked := 0
		if live := s.net.Channel(name); live != nil {
			for _, m := range live.SortedMembers() {
				if oc := s.clientOf(m.User); oc != nil && !oc.isOper() {
					s.svcKick(s.operServ, live, m.User, "This channel is forbidden: "+reason)
					kicked++
				}
			}
		}
		s.os(c, "%s is forbidden now (%s kicked).", name, plural(kicked, "person"))
	case "UNFORBID":
		s.unforbid(c, store.ForbidChan, name)
	default:
		s.os(c, "Syntax: CHAN DROP|FORBID|UNFORBID|SETFOUNDER #chan ... (/os help chan)")
	}
}

func osForbids(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	var all []store.Forbid
	for _, f := range s.forbidNicks {
		all = append(all, f)
	}
	for _, f := range s.forbidChans {
		all = append(all, f)
	}
	if len(all) == 0 {
		s.os(c, "Nothing is forbidden.")
		return
	}
	sort.Slice(all, func(i, j int) bool { return irc.Fold(all[i].Name) < irc.Fold(all[j].Name) })
	width := 4
	for _, f := range all {
		width = max(width, len(f.Name))
	}
	s.os(c, "Forbidden:")
	for _, f := range all {
		s.os(c, "  %-*s  by %s, %s · %s", width, f.Name, f.SetBy, f.SetAt.UTC().Format("2 Jan 2006"), f.Reason)
	}
	s.os(c, "%s.", plural(len(all), "entry"))
}

func osSession(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "":
		s.os(c, "Session limit: %d connections per IP (%s).", s.cfg.Limits.SessionLimit, plural(len(s.sessionExcepts), "exception"))
		counts := map[string]int{}
		for _, oc := range s.clients {
			if !oc.closed {
				counts[oc.ip]++
			}
		}
		type ipCount struct {
			ip string
			n  int
		}
		var busy []ipCount
		for ip, n := range counts {
			if n > 1 {
				busy = append(busy, ipCount{ip, n})
			}
		}
		sort.Slice(busy, func(i, j int) bool { return busy[i].n > busy[j].n || busy[i].n == busy[j].n && busy[i].ip < busy[j].ip })
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
	case "LIMIT":
		if argAt(args, 1) == "" {
			s.os(c, "Syntax: SESSION LIMIT <n>")
			return
		}
		s.host.ApplySetting(c.user.UID, s.operServ.user.UID, "session-limit", args[1])
	case "EXCEPT":
		osSessionExcept(s, c, args[1:])
	default:
		s.os(c, "Syntax: SESSION [LIMIT <n> | EXCEPT ADD|DEL|LIST ...]")
	}
}

func osSessionExcept(s *Services, c *Client, args []string) {
	sub, mask := strings.ToUpper(argAt(args, 0)), strings.ToLower(argAt(args, 1))
	switch sub {
	case "ADD":
		n, err := strconv.Atoi(argAt(args, 2))
		if mask == "" || err != nil || n < 1 || n > 10000 || strings.ContainsAny(mask, "@! ") {
			s.os(c, "Syntax: SESSION EXCEPT ADD <ip-mask> <n> [reason], like 192.168.50.* 20")
			return
		}
		e := store.SessionExcept{Mask: mask, Max: n, Reason: irc.Truncate(strings.Join(args[3:], " "), 200), SetBy: c.user.Nick, SetAt: time.Now()}
		if err := s.store.SaveSessionExcept(e); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "SESSION EXCEPT ADD %s %d", mask, n)
		s.os(c, "%s may now have %d connections.", mask, n)
	case "DEL":
		gone, err := s.store.DelSessionExcept(mask)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if !gone {
			s.os(c, "No session exception for %s.", mask)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "SESSION EXCEPT DEL %s", mask)
		s.os(c, "%s is back to the normal limit (%d).", mask, s.cfg.Limits.SessionLimit)
	case "LIST":
		if len(s.sessionExcepts) == 0 {
			s.os(c, "No session exceptions. Everyone may have %d connections.", s.cfg.Limits.SessionLimit)
			return
		}
		s.os(c, "Session exceptions:")
		for _, e := range s.sessionExcepts {
			s.os(c, "  %-20s %-4d by %s · %s", e.Mask, e.Max, e.SetBy, e.Reason)
		}
		s.os(c, "%s.", plural(len(s.sessionExcepts), "exception"))
	default:
		s.os(c, "Syntax: SESSION EXCEPT ADD|DEL|LIST ...")
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
			s.os(c, "Syntax: NEWS ADD <text>")
			return
		}
		if len(s.news) >= maxNews {
			s.os(c, "There are %d news lines already, the most. Remove one first: /os news del <number>", maxNews)
			return
		}
		if err := s.store.AddNews(irc.Truncate(text, 300), c.user.Nick, time.Now()); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "NEWS ADD %s", text)
		s.os(c, "News added. Everyone sees it when they connect.")
	case "DEL":
		n, err := strconv.Atoi(argAt(args, 1))
		if err != nil || n < 1 || n > len(s.news) {
			s.os(c, "Syntax: NEWS DEL <number> (see /os news list)")
			return
		}
		if _, err := s.store.DelNews(s.news[n-1].ID); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.audit(c, s.operServ, "NEWS DEL %s", s.news[n-1].Text)
		s.reloadOperData()
		s.os(c, "News line %d removed.", n)
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
		s.os(c, "Syntax: NEWS ADD|DEL|LIST ...")
	}
}
