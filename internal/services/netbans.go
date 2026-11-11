package services

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// The network ban engine (step 1.5.5): AKILL, ZLINE, SNLINE, SHUN and
// ELINE share one list and one set of sub-commands. Bans match the real
// host and the IP, never the cloak.

// banKind describes one kind for OperServ.
type banKind struct {
	kind  string
	name  string // AKILL, ZLINE, ...
	cmd   string // what the OperServ command is called ("EXCEPTION BAN" for ELINE)
	maskF string // what a mask looks like, for syntax replies
	hit   string // what happens to people it matches, for ADD replies
}

var banKinds = map[string]banKind{
	store.BanAkill:  {store.BanAkill, "AKILL", "AKILL", "<user@host|nick>", "removed"},
	store.BanZline:  {store.BanZline, "ZLINE", "ZLINE", "<ip|cidr|nick>", "removed"},
	store.BanSNLine: {store.BanSNLine, "SNLINE", "SNLINE", "<realname-mask|/regex/|nick>", "removed"},
	store.BanShun:   {store.BanShun, "SHUN", "SHUN", "<user@host|nick>", "shunned"},
	store.BanEline:  {store.BanEline, "ELINE", "EXCEPTION BAN", "<user@host|nick>", "exempt"},
}

// liveBans returns the bans of kind still in force, oldest first.
func (s *Services) liveBans(kind string) []store.NetBan {
	now := time.Now()
	var out []store.NetBan
	for _, b := range s.netbans {
		if b.Kind == kind && b.Live(now) {
			out = append(out, b)
		}
	}
	return out
}

// liveAkills are the AKILLs that haven't run out.
func (s *Services) liveAkills() []store.NetBan { return s.liveBans(store.BanAkill) }

// banMatches reports whether b covers someone with this ident, real host,
// IP and realname.
func (s *Services) banMatches(b *store.NetBan, ident, host, ip, realname string) bool {
	switch b.Kind {
	case store.BanZline:
		return ip != "" && irc.MatchHost(b.Mask, ip)
	case store.BanSNLine:
		if re := s.banRegexp(b.Mask); re != nil {
			return re.MatchString(realname)
		}
		return irc.Match(b.Mask, realname)
	}
	return irc.MatchUserHost(b.Mask, ident, host, ip)
}

// banRegexp is the compiled /regex/ of an SNLINE mask, or nil for a glob.
func (s *Services) banRegexp(mask string) *regexp.Regexp {
	if len(mask) < 3 || mask[0] != '/' || mask[len(mask)-1] != '/' {
		return nil
	}
	if re, ok := s.banRegexps[mask]; ok {
		return re
	}
	re, err := regexp.Compile("(?i)" + mask[1:len(mask)-1])
	if err != nil {
		re = regexp.MustCompile(`\A\b\B`) // matches nothing
	}
	s.banRegexps[mask] = re
	return re
}

// firstBan returns the first live ban of kind matching, or nil.
func (s *Services) firstBan(kind, ident, host, ip, realname string) *store.NetBan {
	now := time.Now()
	for i := range s.netbans {
		b := &s.netbans[i]
		if b.Kind == kind && b.Live(now) && s.banMatches(b, ident, host, ip, realname) {
			return b
		}
	}
	return nil
}

// exempt reports whether an ELINE covers ident at host/ip. With ident ""
// (before the ident is known) only the host part counts.
func (s *Services) exempt(ident, host, ip string) bool {
	now := time.Now()
	for _, b := range s.netbans {
		if b.Kind != store.BanEline || !b.Live(now) {
			continue
		}
		if ident == "" {
			_, h, _ := strings.Cut(b.Mask, "@")
			if irc.MatchHost(h, ip) || irc.MatchHost(h, host) {
				return true
			}
			continue
		}
		if irc.MatchUserHost(b.Mask, ident, host, ip) {
			return true
		}
	}
	return false
}

// connectBan is the ban that refuses someone registering, or nil.
func (s *Services) connectBan(ident, host, ip, realname string) *store.NetBan {
	if s.exempt(ident, host, ip) {
		return nil
	}
	for _, kind := range []string{store.BanZline, store.BanAkill, store.BanSNLine} {
		if b := s.firstBan(kind, ident, host, ip, realname); b != nil {
			return b
		}
	}
	return nil
}

// shunned reports whether a SHUN covers u.
func (s *Services) shunned(u *state.User) bool {
	return s.firstBan(store.BanShun, u.Ident, u.RealHost, u.IP, u.Realname) != nil && !s.exempt(u.Ident, u.RealHost, u.IP)
}

// banMask turns what an oper typed into the stored mask for kind, or says
// what was wrong.
func (s *Services) netbanMask(kind, arg string) (string, string) {
	u := s.net.UserByNick(arg)
	if u != nil && u.Service {
		u = nil
	}
	switch kind {
	case store.BanZline:
		if u != nil && u.IP != "" {
			return irc.DisplayIP(u.IP), ""
		}
		m := strings.ToLower(arg)
		if strings.Contains(m, "/") {
			if !irc.IsCIDR(m) {
				return "", m + " isn't a CIDR range. One looks like 203.0.113.0/24 or 2001:db8::/32."
			}
			return m, ""
		}
		_, ipErr := netip.ParseAddr(m)
		if m == "" || strings.Trim(m, "0123456789abcdef.:*?") != "" || !strings.ContainsAny(m, "*?") && ipErr != nil {
			return "", "A ZLINE is an IP, like 203.0.113.5, 203.0.113.* or 203.0.113.0/24 (or the nick of someone online)."
		}
		if strings.Trim(m, "*?.:") == "" {
			return "", "That mask matches everyone. Give a narrower one."
		}
		return m, ""
	case store.BanSNLine:
		if u != nil {
			return strings.ReplaceAll(u.Realname, " ", "?"), ""
		}
		if strings.Trim(arg, "*?") == "" {
			return "", "That mask matches everyone. Give a narrower one."
		}
		if len(arg) >= 3 && arg[0] == '/' && arg[len(arg)-1] == '/' {
			if _, err := regexp.Compile(arg[1 : len(arg)-1]); err != nil {
				return "", "That regex doesn't work: " + err.Error()
			}
		}
		return arg, ""
	}
	m, problem := s.akillMask(arg)
	if problem == "" {
		if _, host, _ := strings.Cut(m, "@"); strings.Contains(host, "/") && !irc.IsCIDR(host) {
			return "", host + " isn't a CIDR range. One looks like 203.0.113.0/24 or 2001:db8::/32."
		}
	}
	return m, problem
}

// akillMask turns a nick, host or user@host into a user@host mask.
func (s *Services) akillMask(arg string) (string, string) {
	if !strings.Contains(arg, "@") {
		if u := s.net.UserByNick(arg); u != nil && !u.Service && u.IP != "" {
			return "*@" + irc.DisplayIP(u.IP), ""
		}
		if irc.ValidNick(arg, s.cfg.Limits.NickLen) && !strings.ContainsAny(arg, ".*?:/") {
			return "", arg + " isn't online. Give a mask like *@203.0.113.5 instead."
		}
		arg = "*@" + arg
	}
	user, host, _ := strings.Cut(arg, "@")
	if user == "" || host == "" || strings.ContainsAny(arg, "! ,") || strings.Count(arg, "@") > 1 {
		return "", "A mask looks like *@203.0.113.*, *@203.0.113.0/24 or ~bot@*.example.net."
	}
	if (strings.Trim(host, "*?.:") == "" || strings.HasSuffix(host, "/0")) && strings.Trim(user, "*?~") == "" {
		return "", "That mask matches everyone. Give a narrower one."
	}
	return strings.ToLower(arg), ""
}

// banHits are the people on the network a new ban of kind would match.
func (s *Services) banHits(b *store.NetBan) []*Client {
	var hits []*Client
	for _, oc := range s.clients {
		if oc.closed {
			continue
		}
		u := oc.user
		if s.banMatches(b, u.Ident, u.RealHost, u.IP, u.Realname) {
			hits = append(hits, oc)
		}
	}
	return hits
}

// defaultBanLife is how long a network ban added without +expiry lasts:
// ban_expiry (30 days, Anope's autokillexpiry/snlineexpiry/sqlineexpiry)
// for everything but ELINE, which stays until removed.
func (s *Services) defaultBanLife(kind string) time.Duration {
	if kind == store.BanEline {
		return 0
	}
	return s.cfg.Services.BanExpiry.Duration
}

// osNetBan is the OperServ command for one kind of network ban.
func osNetBan(kind string) func(*Services, *Client, []string) {
	k := banKinds[kind]
	return func(s *Services, c *Client, args []string) {
		if !s.needFlag(c, s.operServ, 'k') {
			return
		}
		s.netBanCommand(c, k, args)
	}
}

func (s *Services) netBanCommand(c *Client, k banKind, args []string) {
	sub := strings.ToUpper(argAt(args, 0))
	live := s.liveBans(k.kind)
	switch sub {
	case "ADD":
		rest := args[1:]
		life := s.defaultBanLife(k.kind)
		if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
			d, ok := parseExpiry(rest[0])
			if !ok {
				s.os(c, "%s isn't a time. Use +30m, +12h, +7d, or +0 for until removed.", rest[0])
				return
			}
			life, rest = d, rest[1:]
		}
		if k.kind == store.BanSNLine && len(rest) > 0 && !strings.HasPrefix(rest[0], "/") {
			// Anope's form: mask:reason, where the mask may hold spaces.
			if joined := strings.Join(rest, " "); strings.Contains(joined, ":") {
				m, r, _ := strings.Cut(joined, ":")
				if m, r = strings.TrimSpace(m), strings.TrimSpace(r); m == "" || r == "" {
					s.os(c, "Syntax: SNLINE ADD [+expiry] <mask>:<reason>")
					return
				}
				rest = []string{m, r}
			}
		}
		if len(rest) < 2 {
			s.os(c, "Syntax: %s ADD [+expiry] %s <reason>", k.cmd, k.maskF)
			return
		}
		mask, problem := s.netbanMask(k.kind, rest[0])
		if problem != "" {
			s.os(c, "%s", problem)
			return
		}
		now := time.Now()
		b := store.NetBan{Kind: k.kind, Mask: mask, Reason: irc.Truncate(strings.Join(rest[1:], " "), 200), SetBy: c.user.Nick, SetAt: now}
		if life > 0 {
			b.Expires = now.Add(life)
		}
		u := c.user
		if k.kind != store.BanEline && s.banMatches(&b, u.Ident, u.RealHost, u.IP, u.Realname) {
			s.os(c, "%s matches you. Give a narrower one.", mask)
			return
		}
		hits := s.banHits(&b)
		people := 0
		for _, oc := range s.clients {
			if !oc.closed {
				people++
			}
		}
		if k.kind != store.BanEline && len(hits) > 1 && len(hits) == people {
			s.os(c, "%s matches everyone online. Give a narrower one.", mask)
			return
		}
		if err := s.store.AddNetBan(b); err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.reloadOperData()
		how := "until removed"
		if life > 0 {
			how = "for " + durWords(life)
		}
		s.audit(c, s.operServ, "%s ADD %s %s (%s)", k.name, mask, how, b.Reason)
		n := 0
		for _, oc := range hits {
			ou := oc.user
			if k.kind != store.BanEline && s.exempt(ou.Ident, ou.RealHost, ou.IP) {
				continue
			}
			n++
			switch k.kind {
			case store.BanAkill, store.BanZline, store.BanSNLine:
				s.reply(oc, irc.ErrYoureBannedCreep, "You are banned from this network: "+b.Reason)
				s.quit(oc, "Banned: "+b.Reason)
			}
		}
		s.osLog(c, "%s", anopeLineAdd(mask, b.Reason, life, n, people))
		if k.kind == store.BanEline {
			s.os(c, "%s added: %s %s (%s). %s online now.", k.name, mask, how, b.Reason, plural(n, "person")+" "+k.hit)
			return
		}
		s.os(c, "%s added: %s %s (%s). %s %s.", k.name, mask, how, b.Reason, plural(n, "person"), k.hit)
	case "DEL":
		arg := argAt(args, 1)
		if arg == "" {
			s.os(c, "Syntax: %s DEL {<mask> | <num> | <list>}", k.cmd)
			return
		}
		var masks []string
		if nums, isList := numList(arg, len(live)); isList {
			for _, n := range nums {
				masks = append(masks, live[n-1].Mask)
			}
		} else {
			m := arg
			switch k.kind {
			case store.BanSNLine:
			case store.BanZline:
				m = strings.ToLower(m)
			default:
				m = strings.ToLower(m)
				if !strings.Contains(m, "@") {
					m = "*@" + m
				}
			}
			masks = []string{m}
		}
		var gone []string
		for _, m := range masks {
			ok, err := s.store.DelNetBan(k.kind, m)
			if err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			if ok {
				gone = append(gone, m)
			}
		}
		if len(gone) == 0 {
			s.os(c, "No %s on %s. See them all: /os %s list", k.name, arg, strings.ToLower(k.cmd))
			return
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "%s DEL %s", k.name, strings.Join(gone, " "))
		for _, m := range gone {
			s.osLog(c, "to remove %s from the list", m)
		}
		if len(gone) == 1 {
			s.os(c, "%s on %s removed.", k.name, gone[0])
		} else {
			s.os(c, "%s removed: %s.", plural(len(gone), k.name), strings.Join(gone, ", "))
		}
	case "LIST", "VIEW", "":
		show := pickEntries(argAt(args, 1), len(live), func(i int) string { return live[i].Mask })
		if len(show) == 0 {
			s.os(c, "No %ss.", k.name)
			return
		}
		s.os(c, "%ss:", k.name)
		for n, i := range show {
			if n == maxAkillList {
				break
			}
			b := live[i]
			left := "until removed"
			if !b.Expires.IsZero() {
				left = durWords(time.Until(b.Expires)+30*time.Second) + " left"
			}
			if sub == "VIEW" {
				s.os(c, "  %-3d %s  set by %s, %s · %s · %s", i+1, b.Mask, b.SetBy, showTime(b.SetAt), left, b.Reason)
			} else {
				s.os(c, "  %-3d %s  by %s · %s · %s", i+1, b.Mask, b.SetBy, left, b.Reason)
			}
		}
		s.os(c, "%s.", plural(len(show), k.name))
	case "CLEAR":
		for _, b := range s.netbans {
			if b.Kind != k.kind {
				continue
			}
			if _, err := s.store.DelNetBan(k.kind, b.Mask); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
		}
		n := len(live)
		s.reloadOperData()
		s.audit(c, s.operServ, "%s CLEAR (%d)", k.name, n)
		s.osLog(c, "to CLEAR the list")
		s.os(c, "Every %s is removed (%d).", k.name, n)
	default:
		s.os(c, "Syntax: %s ADD|DEL|LIST|VIEW|CLEAR ... (/os help %s)", k.cmd, strings.ToLower(strings.Fields(k.cmd)[0]))
	}
}

// osSQLine is Anope's SQLINE: nick bans. It is the FORBID NICK list.
func osSQLine(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 's') {
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	switch sub {
	case "ADD":
		rest := args[1:]
		life := s.cfg.Services.BanExpiry.Duration
		if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
			d, ok := parseExpiry(rest[0])
			if !ok {
				s.os(c, "%s isn't a time. Use +30m, +12h, +7d, or +0 for until removed.", rest[0])
				return
			}
			life, rest = d, rest[1:]
		}
		if len(rest) == 0 {
			s.os(c, "Syntax: SQLINE ADD [+expiry] <nick-mask> [reason]")
			return
		}
		osForbidAdd(s, c, store.ForbidNick, rest[0], strings.Join(rest[1:], " "), life)
	case "DEL":
		osForbid(s, c, []string{"DEL", "NICK", argAt(args, 1)})
	case "LIST", "VIEW", "":
		osForbidList(s, c, "NICK")
	case "CLEAR":
		n := 0
		for _, f := range s.forbids[store.ForbidNick] {
			if _, err := s.store.DelForbid(store.ForbidNick, f.Name); err != nil {
				s.dbErr(c, s.operServ, err)
				return
			}
			n++
		}
		s.reloadOperData()
		s.audit(c, s.operServ, "SQLINE CLEAR (%d)", n)
		s.osLog(c, "to CLEAR the list")
		s.os(c, "Every SQLINE is removed (%d).", n)
	default:
		s.os(c, "Syntax: SQLINE ADD|DEL|LIST|CLEAR ... (/os help sqline)")
	}
}

// anopeLineAdd is os_akill/os_sxline's ADD log text: "on MASK (REASON),
// expires in DURATION [affects N user(s) (P%)]".
func anopeLineAdd(mask, reason string, life time.Duration, affected, people int) string {
	expires := "never"
	if life > 0 {
		expires = anopeDuration(life)
	}
	percent := 0
	if people > 0 {
		percent = affected * 100 / people
	}
	return fmt.Sprintf("on %s (%s), expires in %s [affects %d user(s) (%d%%)]", mask, reason, expires, affected, percent)
}
