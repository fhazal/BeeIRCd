package services

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// csClone copies one registered channel's setup onto another, as Anope's
// CLONE does: ALL (the default) brings the settings, mode lock and LEVELS
// and adds the source's ACCESS, AKICK and BADWORDS entries the target
// lacks; the other words copy just that list. Entries already on the
// target stay as they are. Needs SET on both channels; ALL and ACCESS
// need the founder of the target.
func csClone(s *Services, c *Client, args []string) {
	what := strings.ToUpper(argAt(args, 2))
	if what == "" {
		what = "ALL"
	}
	if argAt(args, 1) == "" || !map[string]bool{"ALL": true, "ACCESS": true, "AKICK": true, "BADWORDS": true}[what] {
		s.cs(c, "Syntax: CLONE #from #to [ALL|ACCESS|AKICK|BADWORDS]")
		return
	}
	src, _, ok := s.csPriv(c, argAt(args, 0), "SET")
	if !ok {
		return
	}
	dst, dstLvl, ok := s.csPriv(c, argAt(args, 1), "SET")
	if !ok {
		return
	}
	// Levels and access entries could rank someone above the cloner: only
	// the target's founder copies them (LEVELS SET is founder-only too).
	if (what == "ALL" || what == "ACCESS") && dstLvl.level != store.LevelFounder {
		s.cs(c, "Only the founder of %s can copy access entries or levels onto it. CLONE %s %s AKICK or BADWORDS needs SET.", dst.Name, src.Name, dst.Name)
		return
	}
	if src.ID == dst.ID {
		s.cs(c, "%s and %s are the same channel.", src.Name, dst.Name)
		return
	}
	now := time.Now()
	var done []string
	persist := dst.Persist
	if what == "ALL" {
		if err := s.cloneSettings(src, dst); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		done = append(done, "settings, mode lock and levels")
	}
	if what == "ALL" || what == "ACCESS" {
		n, err := s.cloneAccess(src, dst, c.user.Nick, now)
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		done = append(done, plural(n, "access entry"))
	}
	if what == "ALL" || what == "AKICK" {
		n, err := s.cloneAkicks(src, dst, c.user.Nick, now)
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		done = append(done, plural(n, "AKICK"))
	}
	if what == "ALL" || what == "BADWORDS" {
		n, err := s.cloneBadWords(src, dst)
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		done = append(done, plural(n, "bad word"))
	}
	s.csUsed(c, dst)
	s.csLog(c, dst.Name, false, "to copy %s from %s", strings.ToLower(what), src.Name)
	s.cs(c, "Copied from %s to %s: %s.", src.Name, dst.Name, strings.Join(done, ", "))
	if live := s.net.Channel(dst.Name); live != nil && what == "ALL" {
		s.csMode(live, mlockChanges(dst, live))
	}
	// PERSIST came over: ChanServ joins or leaves as SET PERSIST makes it.
	if dst.Persist != persist {
		if dst.Persist {
			s.persistJoin(dst)
		} else {
			s.persistPart(dst)
		}
	}
}

// cloneSettings gives dst src's settings, keeping what makes dst itself:
// name, founder, successor, description, topic, dates, suspension and
// NOEXPIRE (an oper setting).
func (s *Services) cloneSettings(src, dst *store.Channel) error {
	keep := *dst
	*dst = *src
	dst.ID, dst.Name = keep.ID, keep.Name
	dst.FounderID, dst.Founder = keep.FounderID, keep.Founder
	dst.SuccessorID, dst.Successor = keep.SuccessorID, keep.Successor
	dst.Description, dst.URL = keep.Description, keep.URL
	dst.Registered, dst.LastUsed = keep.Registered, keep.LastUsed
	dst.Topic, dst.TopicBy, dst.TopicAt = keep.Topic, keep.TopicBy, keep.TopicAt
	dst.LastModes = keep.LastModes
	dst.SuspendedBy, dst.SuspendReason = keep.SuspendedBy, keep.SuspendReason
	dst.SuspendedAt, dst.SuspendUntil = keep.SuspendedAt, keep.SuspendUntil
	dst.NoExpire = keep.NoExpire
	dst.MlockParams = dropSelfOverflow(dst.MlockParams, dst.Name)
	if err := s.store.SaveChannel(dst); err != nil {
		return err
	}
	if err := s.store.ResetChanLevels(dst); err != nil {
		return err
	}
	for priv, lvl := range s.store.ChanLevels(src) {
		if err := s.store.SetChanLevel(dst, priv, lvl); err != nil {
			return err
		}
	}
	return nil
}

// dropSelfOverflow removes "L <name>" from locked parameter modes: a
// channel can't send people who can't get in to itself.
func dropSelfOverflow(params, name string) string {
	f := strings.Fields(params)
	out := f[:0]
	for i := 0; i < len(f); i++ {
		if f[i] == "L" && i+1 < len(f) && irc.Fold(f[i+1]) == irc.Fold(name) {
			i++
			continue
		}
		out = append(out, f[i])
	}
	return strings.Join(out, " ")
}

// cloneAccess adds src's access entries that dst lacks; dst's founder is
// left alone, and so are accounts with NickServ SET NEVEROP on.
func (s *Services) cloneAccess(src, dst *store.Channel, by string, now time.Time) (int, error) {
	list, err := s.store.AccessList(src, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range list {
		if e.AccountID == dst.FounderID {
			continue
		}
		if lvl, _ := s.store.Level(dst, e.AccountID); lvl != store.LevelNone {
			continue
		}
		a, _ := s.store.AccountByID(e.AccountID)
		if a == nil || a.NeverOp {
			continue
		}
		var err error
		if e.Flags != "" {
			err = s.store.SetFlags(dst, a, e.Flags, e.Level, by, now)
		} else {
			_, err = s.store.SetLevel(dst, a, e.Level, by, e.Note, now)
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// cloneAkicks adds src's AKICK masks that dst lacks.
func (s *Services) cloneAkicks(src, dst *store.Channel, by string, now time.Time) (int, error) {
	list, err := s.store.Akicks(src)
	if err != nil {
		return 0, err
	}
	cur, _ := s.store.Akicks(dst)
	n, have := 0, len(cur)
	for _, k := range list {
		if have+n >= maxAkicks {
			break
		}
		added, err := s.store.AddAkick(dst, k.Mask, k.Reason, by, now)
		if err != nil {
			return n, err
		}
		if added {
			n++
		}
	}
	return n, nil
}

// cloneBadWords adds src's bad words that dst lacks.
func (s *Services) cloneBadWords(src, dst *store.Channel) (int, error) {
	have := map[string]bool{}
	for _, w := range s.store.BadWords(dst) {
		have[strings.ToLower(w.Word)] = true
	}
	n, room := 0, badwordsMax-len(have)
	for _, w := range s.store.BadWords(src) {
		if n >= room {
			break
		}
		if have[strings.ToLower(w.Word)] {
			continue
		}
		if err := s.store.AddBadWord(dst, w.Word, w.Type); err != nil {
			return n, err
		}
		have[strings.ToLower(w.Word)] = true
		n++
	}
	return n, nil
}
