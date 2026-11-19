package services

import (
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Network bans from the Hive Panel. The same list, checks
// and enforcement as OperServ AKILL / ZLINE / SHUN / EXCEPTION ADD and DEL,
// without an IRC client: by names the oper, life < 0 means the default
// (ban_expiry; exceptions stay until removed).

// PanelKinds are the ban kinds the panel shows, in its order.
var PanelKinds = []string{store.BanAkill, store.BanZline, store.BanShun, store.BanSNLine, store.BanEline}

// PanelKindName is a kind as the panel says it.
func PanelKindName(kind string) string {
	if kind == store.BanEline {
		return "Exception"
	}
	return banKinds[kind].name
}

// panelArg is what the oper typed, made fit for netbanMask: an SNLINE
// typed in a form may hold spaces, which a stored glob writes as ? (a
// /regex/ keeps them).
func panelArg(kind, arg string) string {
	arg = strings.TrimSpace(arg)
	if kind == store.BanSNLine && !(len(arg) >= 3 && arg[0] == '/' && arg[len(arg)-1] == '/') {
		arg = strings.Join(strings.Fields(arg), "?")
	}
	return arg
}

func panelKind(kind string) bool {
	for _, k := range PanelKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// PanelBans returns the live bans of kind, newest first.
func (s *Services) PanelBans(kind string) []store.NetBan {
	out := s.liveBans(kind)
	sort.SliceStable(out, func(i, j int) bool { return out[i].SetAt.After(out[j].SetAt) })
	return out
}

// PanelBanMatch checks arg as a mask of kind and counts the people online
// it would match (exempt people not counted, except for exceptions).
func (s *Services) PanelBanMatch(kind, arg string) (mask string, n int, problem string) {
	if !panelKind(kind) {
		return "", 0, "That isn't a kind of ban."
	}
	mask, problem = s.netbanMask(kind, panelArg(kind, arg))
	if problem != "" {
		return "", 0, problem
	}
	b := store.NetBan{Kind: kind, Mask: mask}
	for _, oc := range s.banHits(&b) {
		ou := oc.user
		if ou.Service || kind != store.BanEline && s.exempt(ou.TrueIdent(), ou.RealHost, ou.IP) {
			continue
		}
		n++
	}
	return mask, n, ""
}

// PanelAkill adds an AKILL (People page). See PanelBan.
func (s *Services) PanelAkill(by, arg, reason string, life time.Duration) (mask string, removed int, problem string) {
	return s.PanelBan(by, store.BanAkill, arg, reason, life)
}

// PanelBan adds a network ban of kind on arg (a mask or an online nick)
// and applies it to the people it matches. It returns the stored mask and
// how many it hit, or problem (in words for the oper) when nothing was
// added.
func (s *Services) PanelBan(by, kind, arg, reason string, life time.Duration) (mask string, hit int, problem string) {
	return s.BanVia(by, "Hive Panel", kind, arg, reason, life)
}

// BanVia is PanelBan for any way in: via ("Hive Panel", "RPC") is named
// in the notice to opers.
func (s *Services) BanVia(by, via, kind, arg, reason string, life time.Duration) (mask string, hit int, problem string) {
	if !panelKind(kind) {
		return "", 0, "That isn't a kind of ban."
	}
	k := banKinds[kind]
	mask, problem = s.netbanMask(kind, panelArg(kind, arg))
	if problem != "" {
		return "", 0, problem
	}
	reason = irc.Truncate(strings.TrimSpace(reason), 200)
	if reason == "" {
		reason = "No reason"
	}
	if life < 0 {
		life = s.defaultBanLife(kind)
	}
	now := time.Now()
	b := store.NetBan{Kind: kind, Mask: mask, Reason: reason, SetBy: by, SetAt: now}
	if life > 0 {
		b.Expires = now.Add(life)
	}
	hits := s.banHits(&b)
	people := 0
	for _, oc := range s.clients {
		if !oc.closed && !oc.user.Service {
			people++
		}
	}
	if kind != store.BanEline && len(hits) > 1 && len(hits) >= people {
		return "", 0, mask + " matches everyone online. Give a narrower one."
	}
	for _, b2 := range s.liveBans(kind) {
		if b2.Mask == mask {
			return "", 0, "There is already a" + an(PanelKindName(kind)) + " " + PanelKindName(kind) + " on " + mask + "."
		}
	}
	if err := s.store.AddNetBan(b); err != nil {
		s.log.Printf("services database: %v", err)
		return "", 0, "Something went wrong on the server. Please try again in a moment."
	}
	s.reloadOperData()
	for _, oc := range hits {
		ou := oc.user
		if ou.Service || kind != store.BanEline && s.exempt(ou.TrueIdent(), ou.RealHost, ou.IP) {
			continue
		}
		hit++
		switch kind {
		case store.BanAkill, store.BanZline, store.BanSNLine:
			s.host.Numeric(ou.UID, irc.ErrYoureBannedCreep, "You are banned from this network: "+reason)
			s.quit(oc, "Banned: "+reason)
		}
	}
	how := "until removed"
	if life > 0 {
		how = "for " + config.DurationWords(life)
	}
	s.snotice('x', "%s (%s) added a%s %s on %s %s (%s): %d %s", by, via, an(PanelKindName(kind)), PanelKindName(kind), mask, how, reason, hit, k.hit)
	s.Flush()
	return mask, hit, ""
}

// an is " n" before a word starting with a vowel sound, else "".
func an(word string) string {
	if word != "" && strings.ContainsRune("AEIOU", rune(word[0])) {
		return "n"
	}
	return ""
}

// PanelBanDel removes the ban of kind on mask. problem says why not.
func (s *Services) PanelBanDel(by, kind, mask string) (removed string, problem string) {
	return s.BanDelVia(by, "Hive Panel", kind, mask)
}

// BanDelVia is PanelBanDel for any way in (see BanVia).
func (s *Services) BanDelVia(by, via, kind, mask string) (removed string, problem string) {
	if !panelKind(kind) {
		return "", "That isn't a kind of ban."
	}
	m := strings.TrimSpace(mask)
	if x, ok := irc.ServerExtban(m); ok {
		m = x
	}
	switch {
	case irc.IsPlaceMask(m): // a ban by place, stored in its short form
	case kind == store.BanSNLine: // stored as typed: a realname mask or /regex/
	case kind == store.BanZline:
		m = strings.ToLower(m)
	default:
		m = strings.ToLower(m)
		if !strings.Contains(m, "@") {
			m = "*@" + m
		}
	}
	ok, err := s.store.DelNetBan(kind, m)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return "", "Something went wrong on the server. Please try again in a moment."
	}
	if !ok {
		return "", "There is no " + PanelKindName(kind) + " on " + m + " (any more)."
	}
	s.reloadOperData()
	s.snotice('x', "%s (%s) removed the %s on %s", by, via, PanelKindName(kind), m)
	s.Flush()
	return m, ""
}

// NameBan is a nick or channel-name forbid (UnrealIRCd's name ban,
// a Q-line): Chan says which list it is on.
type NameBan struct {
	store.Forbid
	Chan bool
}

// nameBanKind is the forbid list for name: channel names go on the
// channel list, everything else on the nick list (= SQLINE).
func nameBanKind(name string) string {
	if irc.IsChannel(name) {
		return store.ForbidChan
	}
	return store.ForbidNick
}

// NameBans are the live nick and channel forbids, newest first.
func (s *Services) NameBans() []NameBan {
	now := time.Now()
	var out []NameBan
	for _, kind := range []string{store.ForbidNick, store.ForbidChan} {
		for _, f := range s.forbids[kind] {
			if f.Expires.IsZero() || now.Before(f.Expires) {
				out = append(out, NameBan{f, kind == store.ForbidChan})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SetAt.After(out[j].SetAt) })
	return out
}

// NameBanVia forbids nicks or channel names matching name, as
// /os sqline add and /os forbid add chan do; via is named in the notice
// to opers (see BanVia). It returns how many people it moved off the nick
// or out of the channel, and problem says why not.
func (s *Services) NameBanVia(by, via, name, reason string, life time.Duration) (hit int, problem string) {
	name = strings.TrimSpace(name)
	kind := nameBanKind(name)
	if name == "" || strings.ContainsAny(name, " ,\r\n") {
		return 0, "Give one nick or channel name, * and ? allowed."
	}
	if strings.Trim(name, "*?#") == "" {
		return 0, name + " would match every name. Give a narrower one."
	}
	if problem := s.forbidProblem(kind, name); problem != "" {
		return 0, problem
	}
	reason = irc.Truncate(strings.TrimSpace(reason), 200)
	if reason == "" {
		reason = "Not allowed here"
	}
	f := store.Forbid{Name: name, Reason: reason, SetBy: by, SetAt: time.Now()}
	if life > 0 {
		f.Expires = f.SetAt.Add(life)
	}
	added, err := s.store.AddForbid(kind, f)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return 0, "Something went wrong on the server. Please try again in a moment."
	}
	if !added {
		return 0, name + " is forbidden already."
	}
	s.reloadOperData()
	hit = s.enforceForbid(kind, name, reason)
	how := "until removed"
	if life > 0 {
		how = "for " + config.DurationWords(life)
	}
	s.snotice('x', "%s (%s) forbade the name %s %s (%s): %d moved", by, via, name, how, reason, hit)
	s.Flush()
	return hit, ""
}

// NameBanDelVia lifts the nick or channel forbid on name. problem says
// why not.
func (s *Services) NameBanDelVia(by, via, name string) (problem string) {
	name = strings.TrimSpace(name)
	gone, err := s.store.DelForbid(nameBanKind(name), name)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return "Something went wrong on the server. Please try again in a moment."
	}
	if !gone {
		return name + " isn't forbidden."
	}
	s.reloadOperData()
	s.snotice('x', "%s (%s) lifted the forbid on the name %s", by, via, name)
	s.Flush()
	return ""
}
