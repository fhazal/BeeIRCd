package services

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Step 3b: network bans added from the Hive Panel. The same list, checks
// and enforcement as OperServ AKILL ADD, without an IRC client: by names
// the oper, life < 0 means the default (ban_expiry).

// PanelAkill adds an AKILL on arg (a mask or an online nick) and removes
// the people it matches. It returns the stored mask and how many were
// removed, or problem (in words for the oper) when nothing was added.
func (s *Services) PanelAkill(by, arg, reason string, life time.Duration) (mask string, removed int, problem string) {
	mask, problem = s.akillMask(strings.TrimSpace(arg))
	if problem != "" {
		return "", 0, problem
	}
	reason = irc.Truncate(strings.TrimSpace(reason), 200)
	if reason == "" {
		reason = "Banned"
	}
	if life < 0 {
		life = s.defaultBanLife(store.BanAkill)
	}
	now := time.Now()
	b := store.NetBan{Kind: store.BanAkill, Mask: mask, Reason: reason, SetBy: by, SetAt: now}
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
	if len(hits) > 1 && len(hits) >= people {
		return "", 0, mask + " matches everyone online. Give a narrower one."
	}
	for _, b2 := range s.liveAkills() {
		if b2.Mask == mask {
			return "", 0, "There is already an AKILL on " + mask + "."
		}
	}
	if err := s.store.AddNetBan(b); err != nil {
		s.log.Printf("services database: %v", err)
		return "", 0, "Something went wrong on the server. Please try again in a moment."
	}
	s.reloadOperData()
	for _, oc := range hits {
		ou := oc.user
		if ou.Service || s.exempt(ou.TrueIdent(), ou.RealHost, ou.IP) {
			continue
		}
		removed++
		s.host.Numeric(ou.UID, irc.ErrYoureBannedCreep, "You are banned from this network: "+reason)
		s.quit(oc, "Banned: "+reason)
	}
	how := "until removed"
	if life > 0 {
		how = "for " + durWords(life)
	}
	s.snotice("%s (Hive Panel) added an AKILL on %s %s (%s): %d removed", by, mask, how, reason, removed)
	s.Flush()
	return mask, removed, ""
}
