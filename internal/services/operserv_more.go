package services

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// OperServ CHANKILL, SVSNICK, SVSJOIN, SVSPART, UMODE,
// USERLIST, CHANLIST, INFO, IGNORE and SEEN, and ChanServ SEEN, as in
// Anope 2.0.20 (os_chankill, os_svs, os_mode, os_list, os_info,
// os_ignore, cs_seen).

const (
	maxOperInfo  = 10                  // Anope's os_info max
	seenPurge    = 30 * 24 * time.Hour // Anope's cs_seen purgetime
	seenSaveEach = time.Minute         // SEEN goes to the database this often
)

func operServMoreCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"CHANKILL": {"CHANKILL [+expiry] #chan <reason>", "AKILL everyone in a channel",
			"Puts an AKILL on the real host of everyone in the channel (not opers or\nservices) and disconnects them. Without +expiry the AKILLs last 30 days.\nExample: /os chankill +1d #spamhole drone channel", osChanKill},
		"SVSNICK": {"SVSNICK <nick> <newnick>", "change someone's nick",
			"Changes someone's nick, as the server.", osSvsNick},
		"SVSJOIN": {"SVSJOIN <nick> #chan", "make someone join a channel",
			"Joins someone to a channel, past every mode and ban.", osSvsJoin},
		"SVSPART": {"SVSPART <nick> #chan [reason]", "make someone leave a channel",
			"Parts someone from a channel, with the reason if you give one.", osSvsPart},
		"UMODE": {"UMODE <nick> <modes>", "change someone's user modes",
			"Changes someone's user modes as MODE would for them: +i +w +x, +s only on\nopers, -o takes oper away. They are told who did it.", osUmode},
		"USERLIST": {"USERLIST [{<pattern> | #chan} [INVISIBLE]]", "list the people online",
			"Lists everyone online, or those matching nick!user@host[#realname] (the\nreal host and IP match too), or those in a channel. INVISIBLE: only +i.", osUserList},
		"CHANLIST": {"CHANLIST [{<pattern> | <nick>} [SECRET]]", "list the open channels",
			"Lists every open channel, registered or not, with its people, modes and\ntopic; or those matching a pattern, or the channels of one nick. SECRET:\nonly +s and +p channels.", osChanList},
		"INFO": {"INFO {ADD | DEL | CLEAR} <nick|#chan> [text]", "oper notes on a nick or channel",
			"INFO ADD <nick|#chan> <text>\nINFO DEL <nick|#chan> <text>\nINFO CLEAR <nick|#chan>\nNotes only opers see, in NickServ INFO and ChanServ INFO. A nick's notes\nbelong to its account. Up to 10 each.", osInfo},
		"IGNORE": {"IGNORE {ADD | DEL | LIST | CLEAR} ...", "make services ignore someone",
			"IGNORE ADD <time> {<nick> | <mask>} [reason]   time like 30m, 2h, 7d (a bare\n    number is seconds), 0 for good\nIGNORE DEL {<nick> | <mask>}\nIGNORE LIST\nIGNORE CLEAR\nServices drop everything a match says to them. A nick online ignores their\nhost (*!*@host); masks are nick!user@host. Never applies to opers.", osIgnore},
		"SEEN": {"SEEN {STATS | CLEAR <time>}", "the SEEN data",
			"SEEN STATS   how many nicks SEEN knows\nSEEN CLEAR <time>   forget the nicks seen within that time, like 1h", osSeen},
	}
}

// ---- CHANKILL ----

func osChanKill(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	rest := args
	life := s.defaultBanLife(store.BanAkill)
	if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
		d, ok := parseExpiry(rest[0])
		if !ok {
			s.os(c, "%s isn't a time. Use +30m, +12h, +7d, or +0 for until removed.", rest[0])
			return
		}
		life, rest = d, rest[1:]
	}
	if len(rest) < 2 || !irc.IsChannel(rest[0]) {
		s.os(c, "Syntax: CHANKILL [+expiry] #chan <reason>")
		return
	}
	ch := s.net.Channel(rest[0])
	if ch == nil {
		s.os(c, "%s is empty right now.", rest[0])
		return
	}
	reason := irc.Truncate(strings.Join(rest[1:], " "), 200)
	have := make(map[string]bool)
	for _, b := range s.liveBans(store.BanAkill) {
		have[strings.ToLower(b.Mask)] = true
	}
	now := time.Now()
	var targets []*Client
	for _, m := range ch.SortedMembers() {
		t := s.clientOf(m.User)
		if t == nil || t.closed || m.User.Service || m.User.HasMode('o') || t == c {
			continue
		}
		targets = append(targets, t)
		mask, problem := s.netbanMask(store.BanAkill, "*@"+m.User.RealHost)
		if problem != "" || have[mask] {
			continue
		}
		have[mask] = true
		b := store.NetBan{Kind: store.BanAkill, Mask: mask, Reason: reason, SetBy: c.user.Nick, SetAt: now}
		if life > 0 {
			b.Expires = now.Add(life)
		}
		if err := s.store.AddNetBan(b); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
	}
	s.reloadOperData()
	n := 0
	for _, t := range targets {
		if s.exempt(t.user.TrueIdent(), t.user.RealHost, t.user.IP) {
			continue
		}
		n++
		s.reply(t, irc.ErrYoureBannedCreep, "You are banned from this network: "+reason)
		s.quit(t, "Banned: "+reason)
	}
	how := "until removed"
	if life > 0 {
		how = "for " + durWords(life)
	}
	s.audit(c, s.operServ, "CHANKILL %s %s (%s): %s removed", ch.Name, how, reason, plural(n, "person"))
	s.osLog(c, "on %s (%s)", ch.Name, reason)
	s.os(c, "AKILLed everyone in %s %s (%s): %s removed.", ch.Name, how, reason, plural(n, "person"))
}

// ---- SVSNICK, SVSJOIN, SVSPART, UMODE ----

// svsTarget finds the person an SVS command is about.
func (s *Services) svsTarget(c *Client, nick string) *Client {
	u := s.net.UserByNick(nick)
	if u == nil {
		s.os(c, "%s isn't online.", nick)
		return nil
	}
	if u.Service {
		s.os(c, "%s is a service.", u.Nick)
		return nil
	}
	t := s.clientOf(u)
	if t == nil || t.closed {
		s.os(c, "%s isn't online.", nick)
		return nil
	}
	return t
}

func osSvsNick(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 2 {
		s.os(c, "Syntax: SVSNICK <nick> <newnick>")
		return
	}
	t := s.svsTarget(c, args[0])
	if t == nil {
		return
	}
	newnick := args[1]
	if max := s.cfg.Limits.NickLen; len(newnick) > max {
		newnick = newnick[:max]
		s.os(c, "%s was cut to %d characters.", args[1], max)
	}
	if !irc.ValidNick(newnick, s.cfg.Limits.NickLen) || isReserved(newnick) {
		s.os(c, "%s can't be used as a nick.", newnick)
		return
	}
	if !irc.EqualFold(t.user.Nick, newnick) && s.net.NickInUse(newnick) {
		s.os(c, "%s is in use.", newnick)
		return
	}
	old := t.user.Nick
	s.os(c, "The nick %s is now being changed to %s.", old, newnick)
	s.osLog(c, "to change %s to %s", old, newnick)
	s.audit(c, s.operServ, "SVSNICK %s %s", old, newnick)
	s.emit(&state.NickChange{UID: t.user.UID, Nick: newnick, TS: time.Now().Unix()})
}

func osSvsJoin(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 2 {
		s.os(c, "Syntax: SVSJOIN <nick> #chan")
		return
	}
	t := s.svsTarget(c, args[0])
	if t == nil {
		return
	}
	name := args[1]
	if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
		s.os(c, "%s isn't a valid channel name.", name)
		return
	}
	if ch := s.net.Channel(name); ch != nil && ch.Member(t.user) != nil {
		s.os(c, "%s is already in %s.", t.user.Nick, ch.Name)
		return
	}
	s.host.ForceJoin(t.user.UID, name)
	s.osLog(c, "to force %s to join %s", t.user.Nick, name)
	s.audit(c, s.operServ, "SVSJOIN %s %s", t.user.Nick, name)
	s.os(c, "%s has been joined to %s.", t.user.Nick, name)
}

func osSvsPart(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 2 {
		s.os(c, "Syntax: SVSPART <nick> #chan [reason]")
		return
	}
	t := s.svsTarget(c, args[0])
	if t == nil {
		return
	}
	ch := s.net.Channel(args[1])
	switch {
	case ch == nil:
		s.os(c, "%s is empty right now.", args[1])
		return
	case ch.Member(t.user) == nil:
		s.os(c, "%s isn't in %s.", t.user.Nick, ch.Name)
		return
	}
	reason := irc.Truncate(strings.Join(args[2:], " "), s.cfg.Limits.KickLen)
	nick := t.user.Nick
	s.emit(&state.Part{UID: t.user.UID, Channel: ch.Name, Reason: reason})
	if reason != "" {
		s.osLog(c, "to force %s to part %s with reason %s", nick, ch.Name, reason)
	} else {
		s.osLog(c, "to force %s to part %s", nick, ch.Name)
	}
	s.audit(c, s.operServ, "SVSPART %s %s", nick, ch.Name)
	s.os(c, "%s has been parted from %s.", nick, ch.Name)
}

func osUmode(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	if len(args) < 2 {
		s.os(c, "Syntax: UMODE <nick> <modes>")
		return
	}
	t := s.svsTarget(c, args[0])
	if t == nil {
		return
	}
	modes := args[1]
	if s.host.SetUserModes(t.user.UID, s.operServ.user.UID, modes) {
		s.os(c, "Some of %s aren't user modes we know; the rest was set.", modes)
	}
	s.os(c, "Changed usermodes of %s to %s.", t.user.Nick, modes)
	s.host.Notice(s.operServ.user.UID, t.user.UID, c.user.Nick+" changed your usermodes to "+modes+".")
	s.osLog(c, "%s on %s", modes, t.user.Nick)
	s.audit(c, s.operServ, "UMODE %s %s", t.user.Nick, modes)
}

// ---- USERLIST, CHANLIST ----

func osUserList(s *Services, c *Client, args []string) {
	if !c.isOper() {
		s.os(c, "USERLIST is for opers.")
		return
	}
	pattern, opt := argAt(args, 0), strings.ToUpper(argAt(args, 1))
	if pattern != "" {
		s.osLog(c, "for %s", pattern)
	} else {
		s.osLog(c, "")
	}
	invisible := opt == "INVISIBLE"
	var users []*state.User
	if ch := s.net.Channel(pattern); pattern != "" && irc.IsChannel(pattern) && ch != nil {
		s.os(c, "%s users list:", ch.Name)
		for _, m := range ch.SortedMembers() {
			users = append(users, m.User)
		}
	} else {
		s.os(c, "Users list:")
		for _, u := range s.net.Users() {
			if pattern != "" && !userMatches(u, pattern) {
				continue
			}
			users = append(users, u)
		}
	}
	n := 0
	for _, u := range users {
		if invisible && !u.HasMode('i') {
			continue
		}
		n++
		s.os(c, "  %-16s %-40s %s", u.Nick, u.Ident+"@"+u.Host, u.Realname)
	}
	s.os(c, "End of users list. %s shown.", plural(n, "user"))
}

// userMatches is Anope's USERLIST match: nick!user@host with the shown
// host, the real host or the IP, each also with #realname after it.
func userMatches(u *state.User, pattern string) bool {
	for _, h := range []string{u.Host, u.RealHost, u.IP} {
		m := u.Nick + "!" + u.Ident + "@" + h
		if irc.Match(pattern, m) || irc.Match(pattern, m+"#"+u.Realname) {
			return true
		}
	}
	return false
}

func osChanList(s *Services, c *Client, args []string) {
	if !c.isOper() {
		s.os(c, "CHANLIST is for opers.")
		return
	}
	pattern, opt := argAt(args, 0), strings.ToUpper(argAt(args, 1))
	if pattern != "" {
		s.osLog(c, "for %s", pattern)
	} else {
		s.osLog(c, "")
	}
	secret := opt == "SECRET"
	var chans []*state.Channel
	if u := s.net.UserByNick(pattern); pattern != "" && !irc.IsChannel(pattern) && u != nil {
		s.os(c, "%s channel list:", u.Nick)
		for _, ch := range s.net.Channels() {
			if ch.Member(u) != nil {
				chans = append(chans, ch)
			}
		}
	} else {
		s.os(c, "Channel list:")
		for _, ch := range s.net.Channels() {
			if pattern == "" || irc.Match(pattern, ch.Name) {
				chans = append(chans, ch)
			}
		}
	}
	sort.Slice(chans, func(i, j int) bool { return irc.Fold(chans[i].Name) < irc.Fold(chans[j].Name) })
	n := 0
	for _, ch := range chans {
		if secret && !ch.HasMode('s') && !ch.HasMode('p') {
			continue
		}
		n++
		modes, params := ch.ModeString(true)
		s.os(c, "  %-24s %4d  %-14s %s", ch.Name, len(ch.Members), strings.TrimSpace(modes+" "+strings.Join(params, " ")), ch.Topic)
	}
	s.os(c, "End of channel list. %s shown.", plural(n, "channel"))
}

// ---- INFO ----

// infoTarget resolves INFO's target: a registered channel or a nick's
// account. channel tells which.
func (s *Services) infoTarget(c *Client, target string) (channel bool, id int64, ok bool) {
	if irc.IsChannel(target) {
		reg, err := s.store.ChannelByName(target)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return false, 0, false
		}
		if reg == nil {
			s.os(c, "%s isn't registered.", target)
			return false, 0, false
		}
		return true, reg.ID, true
	}
	a, err := s.store.AccountByNick(target)
	if err != nil {
		s.dbErr(c, s.operServ, err)
		return false, 0, false
	}
	if a == nil {
		s.os(c, "%s isn't a registered nick.", target)
		return false, 0, false
	}
	return false, a.ID, true
}

func osInfo(s *Services, c *Client, args []string) {
	if !c.isOper() {
		s.os(c, "INFO is for opers.")
		return
	}
	sub, target := strings.ToUpper(argAt(args, 0)), argAt(args, 1)
	text := strings.Join(args[min(2, len(args)):], " ")
	if target == "" || (sub == "ADD" || sub == "DEL") && text == "" || sub != "ADD" && sub != "DEL" && sub != "CLEAR" {
		s.os(c, "Syntax: INFO ADD <nick|#chan> <text> | DEL <nick|#chan> <text> | CLEAR <nick|#chan>")
		return
	}
	channel, id, ok := s.infoTarget(c, target)
	if !ok {
		return
	}
	notes, err := s.store.OperInfos(channel, id)
	if err != nil {
		s.dbErr(c, s.operServ, err)
		return
	}
	switch sub {
	case "ADD":
		if len(notes) >= maxOperInfo {
			s.os(c, "The oper info list for %s is full (%d).", target, maxOperInfo)
			return
		}
		for _, n := range notes {
			if strings.EqualFold(n.Text, text) {
				s.os(c, "That oper info is on %s already.", target)
				return
			}
		}
		if err := s.store.AddOperInfo(channel, id, irc.Truncate(text, 300), c.user.Nick, time.Now()); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.os(c, "Added info to %s.", target)
		s.osLog(c, "to add information to %s", target)
	case "DEL":
		for i := len(notes) - 1; i >= 0; i-- {
			if strings.EqualFold(notes[i].Text, text) {
				if _, err := s.store.DelOperInfo(notes[i].ID); err != nil {
					s.dbErr(c, s.operServ, err)
					return
				}
				s.os(c, "Deleted info from %s.", target)
				s.osLog(c, "to remove information from %s", target)
				return
			}
		}
		if len(notes) == 0 {
			s.os(c, "The oper info list for %s is empty.", target)
		} else {
			s.os(c, "No such info \"%s\" on %s.", text, target)
		}
	case "CLEAR":
		if len(notes) == 0 {
			s.os(c, "The oper info list for %s is empty.", target)
			return
		}
		if _, err := s.store.ClearOperInfo(channel, id); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.os(c, "Cleared info from %s.", target)
		s.osLog(c, "to clear information for %s", target)
	}
}

// showOperInfo adds the oper notes to NickServ or ChanServ INFO, for opers.
func (s *Services) showOperInfo(c *Client, sv *service, channel bool, id int64) {
	if !c.isOper() {
		return
	}
	notes, err := s.store.OperInfos(channel, id)
	if err != nil {
		return
	}
	for _, n := range notes {
		s.svcReply(c, sv, fmt.Sprintf("  Oper info     (by %s on %s) %s", n.AddedBy, showTime(n.AddedAt), n.Text))
	}
}

// ---- IGNORE ----

// ignoreMask is Anope's RealMask: a nick online becomes *!*@their real
// host, user@host gets *! in front, a bare nick gets !*@*. "" when it
// can't be a mask.
func (s *Services) ignoreMask(arg string) string {
	if u := s.net.UserByNick(arg); u != nil && !strings.ContainsAny(arg, "!@*?") {
		return "*!*@" + u.RealHost
	}
	at := strings.IndexByte(arg, '@')
	if at < 0 {
		return arg + "!*@*"
	}
	if bang := strings.IndexByte(arg, '!'); bang >= 0 {
		if bang > at {
			return ""
		}
		return arg
	}
	return "*!" + arg
}

// ignoreTime reads IGNORE's time: a bare number is seconds, 0 is for good.
func ignoreTime(arg string) (time.Duration, bool) {
	if n, err := strconv.Atoi(arg); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, true
	}
	d, err := config.ParseDuration(arg)
	return d, err == nil && d > 0
}

// liveIgnores drops expired ignores (with Anope's log line) and returns
// the rest.
func (s *Services) liveIgnores() []store.Ignore {
	now := time.Now()
	keep := s.ignores[:0:0]
	for _, g := range s.ignores {
		if g.Expires.Unix() > 0 && !now.Before(g.Expires) {
			s.store.DelSvcIgnore(g.Mask)
			s.logTo(s.operServ, "Expiring ignore entry "+g.Mask)
			continue
		}
		keep = append(keep, g)
	}
	s.ignores = keep
	return keep
}

// ignored reports whether services ignore u (never an oper).
func (s *Services) ignored(c *Client) bool {
	if len(s.ignores) == 0 || c.isOper() {
		return false
	}
	u := c.user
	for _, g := range s.liveIgnores() {
		for _, h := range []string{u.Host, u.RealHost, u.IP} {
			if irc.Match(g.Mask, u.Nick+"!"+u.Ident+"@"+h) {
				return true
			}
		}
	}
	return false
}

func osIgnore(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'k') {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "ADD":
		if len(args) < 3 {
			s.os(c, "Syntax: IGNORE ADD <time> {<nick> | <mask>} [reason]")
			return
		}
		d, ok := ignoreTime(args[1])
		if !ok {
			s.os(c, "%s isn't a time. Use 30m, 2h, 7d, a number of seconds, or 0 for good.", args[1])
			return
		}
		mask := s.ignoreMask(args[2])
		if mask == "" {
			s.os(c, "Masks look like nick!user@host.")
			return
		}
		now := time.Now()
		g := store.Ignore{Mask: mask, Reason: irc.Truncate(strings.Join(args[3:], " "), 200), SetBy: c.user.Nick, SetAt: now}
		if d > 0 {
			g.Expires = now.Add(d)
		}
		if err := s.store.SetSvcIgnore(g); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "IGNORE ADD %s", mask)
		if d == 0 {
			s.os(c, "%s will now permanently be ignored.", mask)
			s.osLog(c, "to add a permanent ignore for %s", mask)
		} else {
			s.os(c, "%s will now be ignored for %s.", mask, anopeDuration(d))
			s.osLog(c, "to add an ignore on %s for %s", mask, anopeDuration(d))
		}
	case "DEL":
		arg := argAt(args, 1)
		if arg == "" {
			s.os(c, "Syntax: IGNORE DEL {<nick> | <mask>}")
			return
		}
		mask := s.ignoreMask(arg)
		gone, err := s.store.DelSvcIgnore(mask)
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		if !gone {
			s.os(c, "%s isn't on the ignore list.", mask)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "IGNORE DEL %s", mask)
		s.osLog(c, "to remove an ignore on %s", mask)
		s.os(c, "%s will no longer be ignored.", mask)
	case "LIST", "":
		list := s.liveIgnores()
		if len(list) == 0 {
			s.os(c, "The ignore list is empty.")
			return
		}
		s.os(c, "Services ignore list:")
		for _, g := range list {
			left := "for good"
			if g.Expires.Unix() > 0 {
				left = durWords(time.Until(g.Expires)) + " left"
			}
			why := g.Reason
			if why == "" {
				why = "no reason"
			}
			s.os(c, "  %-30s by %s · %s · %s", g.Mask, g.SetBy, left, why)
		}
	case "CLEAR":
		if err := s.store.ClearSvcIgnores(); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "IGNORE CLEAR")
		s.osLog(c, "to CLEAR the list")
		s.os(c, "The ignore list has been cleared.")
	default:
		s.os(c, "Syntax: IGNORE ADD|DEL|LIST|CLEAR ...")
	}
}

// ---- SEEN ----

// seenNote records what nick was last seen doing.
func (s *Services) seenNote(u *state.User, nick, kind, other, channel, message string) {
	if u == nil || u.Service {
		return
	}
	key := irc.Fold(nick)
	s.seen[key] = &store.Seen{Nick: nick, Mask: u.Ident + "@" + u.Host, Kind: kind, Other: other,
		Channel: channel, Message: message, Last: time.Now()}
	s.seenDirty[key] = true
}

// seenEvent feeds SEEN from a network event (Anope's cs_seen hooks).
func (s *Services) seenEvent(ev state.Event) {
	switch e := ev.(type) {
	case *state.UserAdd:
		s.seenNote(e.User, e.User.Nick, store.SeenConnect, "", "", "")
	case *state.NickChange:
		u := s.net.UserByUID(e.UID)
		if u == nil {
			return
		}
		old := e.Old
		if i := strings.IndexByte(old, '!'); i >= 0 {
			old = old[:i]
		}
		s.seenNote(u, old, store.SeenNickTo, u.Nick, "", "")
		s.seenNote(u, u.Nick, store.SeenNickFrom, old, "", "")
	case *state.Join:
		u := s.net.UserByUID(e.UID)
		if u != nil {
			s.seenNote(u, u.Nick, store.SeenJoin, "", e.Channel, "")
		}
	case *state.Part:
		u := s.net.UserByUID(e.UID)
		if u != nil {
			s.seenNote(u, u.Nick, store.SeenPart, "", e.Channel, "")
		}
	case *state.Kick:
		u := s.net.UserByUID(e.UID)
		if u != nil {
			s.seenNote(u, u.Nick, store.SeenKick, "", e.Channel, e.Reason)
		}
	case *state.UserQuit:
		if c := s.clients[e.UID]; c != nil && c.user != nil {
			s.seenNote(c.user, c.user.Nick, store.SeenQuit, "", "", e.Reason)
		}
	}
}

// SaveSeen writes changed SEEN entries to the database (every minute, and
// at shutdown).
func (s *Services) SaveSeen() {
	if len(s.seenDirty) == 0 && len(s.seenGone) == 0 {
		return
	}
	var changed []store.Seen
	for key := range s.seenDirty {
		if e := s.seen[key]; e != nil {
			changed = append(changed, *e)
		}
	}
	if err := s.store.SaveSeen(changed, s.seenGone); err != nil {
		s.log.Printf("services database (seen): %v", err)
		return
	}
	s.seenDirty, s.seenGone = make(map[string]bool), nil
	s.seenSaved = time.Now()
}

// loadSeen reads SEEN from the database at start.
func (s *Services) loadSeen() error {
	all, err := s.store.AllSeen()
	if err != nil {
		return err
	}
	for i := range all {
		e := all[i]
		s.seen[irc.Fold(e.Nick)] = &e
	}
	return nil
}

// expireSeen forgets nicks not seen for seenPurge.
func (s *Services) expireSeen(now time.Time) {
	for key, e := range s.seen {
		if now.Sub(e.Last) > seenPurge {
			delete(s.seen, key)
			delete(s.seenDirty, key)
			s.seenGone = append(s.seenGone, key)
		}
	}
}

// seenHidden is Anope's ShouldHide: a +s or +p channel, or a private
// registered one.
func (s *Services) seenHidden(channel string) bool {
	if ch := s.net.Channel(channel); ch != nil && (ch.HasMode('s') || ch.HasMode('p')) {
		return true
	}
	reg, err := s.store.ChannelByName(channel)
	return err == nil && reg != nil && reg.Private
}

func csSeen(s *Services, c *Client, args []string) {
	target := argAt(args, 0)
	if target == "" {
		s.cs(c, "Syntax: SEEN <nick>")
		return
	}
	if len(target) > s.cfg.Limits.NickLen {
		s.cs(c, "Nick too long, max length is %d characters.", s.cfg.Limits.NickLen)
		return
	}
	if u := s.net.UserByNick(target); u != nil && u.Service {
		s.cs(c, "%s is a client on services.", u.Nick)
		return
	}
	if irc.EqualFold(target, c.user.Nick) {
		s.cs(c, "You might see yourself in the mirror, %s.", c.user.Nick)
		return
	}
	e := s.seen[irc.Fold(target)]
	if e == nil {
		s.cs(c, "Sorry, I have not seen %s.", target)
		return
	}
	status := fmt.Sprintf(" but %s mysteriously dematerialized.", target)
	if s.net.UserByNick(target) != nil {
		status = "."
	}
	ago, when := anopeDuration(time.Since(e.Last)), showTime(e.Last)
	who := e.Nick + " (" + e.Mask + ")"
	switch e.Kind {
	case store.SeenConnect:
		s.cs(c, "%s was last seen connecting %s ago (%s)%s", who, ago, when, status)
	case store.SeenNickTo:
		st := fmt.Sprintf(", but %s mysteriously dematerialized.", e.Other)
		if u := s.net.UserByNick(e.Other); u != nil {
			st = fmt.Sprintf(". %s is still online.", u.Nick)
		}
		s.cs(c, "%s was last seen changing nick to %s %s ago%s", who, e.Other, ago, st)
	case store.SeenNickFrom:
		s.cs(c, "%s was last seen changing nick from %s to %s %s ago%s", who, e.Other, e.Nick, ago, status)
	case store.SeenJoin, store.SeenPart:
		verb := "joining"
		if e.Kind == store.SeenPart {
			verb = "parting"
		}
		if s.seenHidden(e.Channel) {
			s.cs(c, "%s was last seen %s a secret channel %s ago%s", who, verb, ago, status)
		} else {
			s.cs(c, "%s was last seen %s %s %s ago%s", who, verb, e.Channel, ago, status)
		}
	case store.SeenQuit:
		s.cs(c, "%s was last seen quitting (%s) %s ago (%s).", who, e.Message, ago, when)
	case store.SeenKick:
		if s.seenHidden(e.Channel) {
			s.cs(c, "%s was kicked from a secret channel %s ago%s", who, ago, status)
		} else {
			s.cs(c, "%s was kicked from %s (\"%s\") %s ago%s", who, e.Channel, e.Message, ago, status)
		}
	}
}

func osSeen(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "STATS":
		size := 0
		for _, e := range s.seen {
			size += 80 + len(e.Nick) + len(e.Mask) + len(e.Other) + len(e.Channel) + len(e.Message)
		}
		s.os(c, "%s are stored in the database, using %.2f kB of memory.", plural(len(s.seen), "nick"), float64(size)/1024)
	case "CLEAR":
		d, err := config.ParseDuration(argAt(args, 1))
		if err != nil || d <= 0 {
			s.os(c, "Syntax: SEEN CLEAR <time>   like 1h or 7d")
			return
		}
		since := time.Now().Add(-d)
		n := 0
		for key, e := range s.seen {
			if e.Last.After(since) {
				delete(s.seen, key)
				delete(s.seenDirty, key)
				s.seenGone = append(s.seenGone, key)
				n++
			}
		}
		s.SaveSeen()
		s.osLog(c, "CLEAR and removed %d nicks that were added after %s", n, showTime(since))
		s.os(c, "Database cleared, removed %s that were added after %s.", plural(n, "nick"), showTime(since))
	default:
		s.os(c, "Syntax: SEEN {STATS | CLEAR <time>}")
	}
}
