package services

import (
	"sort"
	"strings"
	"time"

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
		how = "for " + durWords(life)
	}
	s.snotice("%s (Hive Panel) added a%s %s on %s %s (%s): %d %s", by, an(PanelKindName(kind)), PanelKindName(kind), mask, how, reason, hit, k.hit)
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
	if !panelKind(kind) {
		return "", "That isn't a kind of ban."
	}
	m := strings.TrimSpace(mask)
	switch kind {
	case store.BanSNLine: // stored as typed: a realname mask or /regex/
	case store.BanZline:
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
	s.snotice("%s (Hive Panel) removed the %s on %s", by, PanelKindName(kind), m)
	s.Flush()
	return m, ""
}
