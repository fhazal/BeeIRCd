package services

// ChanServ FLAGS, as Anope's cs_flags: an access entry can be a set of
// privilege letters instead of a level. FLAGS LIST shows every entry that
// way, level entries as the letters their level gives.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// flagLetters are the FLAGS letters, sorted.
func flagLetters() string {
	seen := map[byte]bool{}
	var out []byte
	for _, p := range privileges {
		if !seen[p.flag] {
			seen[p.flag] = true
			out = append(out, p.flag)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return string(out)
}

// sortFlags returns the letters of set in order.
func sortFlags(set map[byte]bool) string {
	var out []byte
	for f, on := range set {
		if on {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return string(out)
}

// flagsOf is the letters someone with a has on reg (Anope's
// DetermineFlags): a FLAGS entry's own, or what a level gives. A level
// shows a letter only when it has every privilege behind it, so turning
// it into FLAGS never gives more (VOICEME without VOICE shows no v).
func (s *Services) flagsOf(reg *store.Channel, a access) string {
	if a.flags != "" {
		return a.flags
	}
	set := map[byte]bool{}
	for _, p := range privileges {
		if _, seen := set[p.flag]; !seen {
			set[p.flag] = true
		}
		if s.privLevel(reg, p.name) <= 0 || !s.levelHas(reg, a, p.name) {
			set[p.flag] = false
		}
	}
	return sortFlags(set)
}

// canGiveFlag reports whether someone with me may set or unset letter f:
// they need every privilege it stands for.
func (s *Services) canGiveFlag(reg *store.Channel, me access, f byte) bool {
	if me.level == store.LevelFounder {
		return true
	}
	for _, p := range privileges {
		if p.flag == f && !s.levelHas(reg, me, p.name) {
			return false
		}
	}
	return true
}

func csFlags(s *Services, c *Client, args []string) {
	name, sub := argAt(args, 0), strings.ToUpper(argAt(args, 1))
	switch sub {
	case "", "LIST":
		reg, _, ok := s.csPriv(c, name, "ACCESS_LIST")
		if ok {
			s.flagsList(c, reg, argAt(args, 2))
		}
	case "CLEAR":
		csAccess(s, c, []string{name, "CLEAR"})
	case "MODIFY":
		s.flagsModify(c, name, argAt(args, 2), argAt(args, 3))
	default:
		s.flagsModify(c, name, argAt(args, 1), argAt(args, 2))
	}
}

func (s *Services) flagsModify(c *Client, name, nick, changes string) {
	reg, me, ok := s.csPriv(c, name, "ACCESS_CHANGE")
	if !ok {
		return
	}
	if nick == "" || changes == "" {
		s.cs(c, "Syntax: FLAGS %s [MODIFY] <nick> <changes>, like +AOtv or -v", reg.Name)
		return
	}
	target, ok := s.csAccount(c, reg, nick)
	if !ok {
		return
	}
	cur := s.accountLevel(reg, target.ID)
	onList := cur.level != store.LevelNone
	// As in Anope: a level entry may only change those below it; a FLAGS
	// entry is held back by the letters it may give.
	if onList && me.flags == "" && me.level != store.LevelFounder && cur.level >= me.level {
		s.cs(c, "You can't change %s: their access is the same as or above yours.", target.Name)
		return
	}
	set := map[byte]bool{}
	if onList {
		for _, f := range []byte(s.flagsOf(reg, cur)) {
			set[f] = true
		}
	}
	before := sortFlags(set)
	letters := flagLetters()
	add := true
	var priv string
	var refused []string
	change := func(f byte) {
		if set[f] == add {
			return
		}
		if !s.canGiveFlag(reg, me, f) {
			refused = append(refused, string(f))
			return
		}
		set[f] = add
	}
	for i := 0; i < len(changes); i++ {
		switch f := changes[i]; {
		case f == '+':
			add = true
		case f == '-':
			add = false
		case f == '*':
			for j := 0; j < len(letters); j++ {
				if set[letters[j]] != add && s.canGiveFlag(reg, me, letters[j]) {
					set[letters[j]] = add
				}
			}
		default:
			// A privilege's name stands for its letter: +AUTOOP.
			if p, ok := findPrivilege(changes[i:]); ok {
				priv = p.name
				change(p.flag)
				i = len(changes)
				continue
			}
			if strings.IndexByte(letters, f) >= 0 {
				change(f)
			}
		}
	}
	for _, f := range refused {
		s.cs(c, "You can't set the %s flag: you don't have its privileges on %s.", f, reg.Name)
	}
	flags := sortFlags(set)
	if flags == "" {
		if !onList {
			s.cs(c, "%s isn't on the access list of %s.", target.Name, reg.Name)
			return
		}
		if _, err := s.store.RemoveAccess(reg, target.ID); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to delete %s", target.Name)
		s.cs(c, "%s removed from the access list of %s.", target.Name, reg.Name)
		s.afterAccessChange(reg, target.ID)
		return
	}
	if flags == before {
		if len(refused) == 0 {
			s.cs(c, "%s has flags +%s on %s already.", target.Name, flags, reg.Name)
		}
		return
	}
	if err := s.store.SetFlags(reg, target, flags, s.flagsRank(reg, flags), c.user.Account, time.Now()); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "to modify %s's flags to %s", target.Name, flags)
	switch {
	case priv != "" && add:
		s.cs(c, "Privilege %s added to %s on %s; the flags are now +%s.", priv, target.Name, reg.Name, flags)
	case priv != "":
		s.cs(c, "Privilege %s removed from %s on %s; the flags are now +%s.", priv, target.Name, reg.Name, flags)
	default:
		s.cs(c, "Flags for %s on %s set to +%s.", target.Name, reg.Name, flags)
	}
	s.afterAccessChange(reg, target.ID)
}

// flagsList is FLAGS LIST [nick-mask | +flags].
func (s *Services) flagsList(c *Client, reg *store.Channel, filter string) {
	entries, err := s.store.AccessList(reg, 0)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	if len(entries) == 0 {
		s.cs(c, "The access list of %s is empty.", reg.Name)
		return
	}
	width := 4
	for _, e := range entries {
		width = max(width, len(e.Account))
	}
	var lines []string
	for i, e := range entries {
		flags := e.Flags
		if flags == "" {
			flags = s.flagsOf(reg, access{level: e.Level})
		}
		switch {
		case strings.HasPrefix(filter, "+"):
			if strings.Trim(filter[1:], flags) != "" {
				continue
			}
		case filter != "" && !irc.Match(filter, e.Account):
			continue
		}
		shown := "+" + flags
		if flags == "" {
			shown = "(none)"
		}
		lines = append(lines, fmt.Sprintf("  %-3d %-*s  %-12s %s, %s", i+1, width, e.Account, shown, e.AddedBy, e.AddedAt.UTC().Format("2 Jan 2006")))
	}
	if len(lines) == 0 {
		s.cs(c, "No matching entries on the access list of %s.", reg.Name)
		return
	}
	s.cs(c, "Flags list for %s", reg.Name)
	s.cs(c, "  %-3s %-*s  %-12s %s", "#", width, "Nick", "Flags", "Added by")
	for _, l := range lines {
		s.cs(c, "%s", l)
	}
	if len(lines) == len(entries) {
		s.cs(c, "End of access list.")
	} else {
		s.cs(c, "End of access list: %d of %d entries shown.", len(lines), len(entries))
	}
}
