package services

import (
	"time"

	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// These run from the normal commands so registered channels look after
// themselves.

// csJoinLevel is u's level on channel name, before joining (0 when the
// channel isn't registered). Anyone on the access list gets past +i and +k.
func (s *Services) csJoinLevel(c *Client, name string) int {
	if c.accountID == 0 && !s.isSuperAdmin(c) {
		return store.LevelNone
	}
	reg, err := s.store.ChannelByName(name)
	if err != nil || reg == nil || reg.Suspended(s.KeepNow()) {
		return store.LevelNone
	}
	return s.clientLevel(c, reg).level
}

// csJoin runs after u joined ch. created means u opened the channel.
func (s *Services) csJoin(c *Client, ch *state.Channel, created bool) {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil {
		return
	}
	u := c.user
	if reg.Suspended(s.KeepNow()) {
		if !c.isOper() {
			s.csKickUser(ch, u, "This channel is suspended: "+reg.SuspendReason)
		}
		return
	}
	lvl := s.clientLevel(c, reg)
	if created {
		s.restoreChannel(reg, ch)
		if !s.secureAllows(reg, lvl, 'o') {
			// Opening an empty registered channel doesn't make you its op.
			s.csMode(ch, []modeChange{{false, 'o', u.Nick}})
		}
		if reg.Persist {
			s.persistJoin(reg)
		}
	}
	if lvl.level == store.LevelNone {
		kicks, err := s.store.Akicks(reg)
		if err != nil {
			return
		}
		for _, k := range kicks {
			if matchesMask(k.Mask, u) {
				s.csMode(ch, []modeChange{{true, 'b', k.Mask}})
				s.csKickUser(ch, u, k.Reason)
				return
			}
		}
	}
	if reg.Restricted && !s.levelHas(reg, lvl, "NOKICK") {
		s.csMode(ch, []modeChange{{true, 'b', banMask(reg, u)}})
		s.csKickUser(ch, u, "This channel is for its access list only.")
		return
	}
	if lvl.level > store.LevelNone {
		reg.LastUsed = time.Now()
		s.store.SaveChannel(reg)
	}
	s.csAutoModes(reg, ch, u)
	for _, e := range s.store.EntryMsgs(reg) {
		s.cs(c, "[%s] %s", ch.Name, e.Text)
	}
	s.chanMemoNotice(c, reg)
	s.bsJoined(c, reg, ch)
}

// csOnIdentify gives a user who just identified the modes they earn in the
// channels they are already in, and tells them about channel memos there.
func (s *Services) csOnIdentify(c *Client) {
	for _, ch := range c.user.Channels {
		reg, err := s.store.ChannelByName(ch.Name)
		if err != nil || reg == nil || reg.Suspended(s.KeepNow()) {
			continue
		}
		if s.clientLevel(c, reg).level > store.LevelNone {
			reg.LastUsed = time.Now()
			s.store.SaveChannel(reg)
			s.csAutoModes(reg, ch, c.user)
		}
		s.chanMemoNotice(c, reg)
	}
}

// csAfterUserModes puts the mode lock back and, with SECUREOPS, takes
// status from people not allowed it.
func (s *Services) csAfterUserModes(ch *state.Channel, changes []modeChange) {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil {
		return
	}
	fix := append(mlockChanges(reg, ch), s.bsKeepModes(reg, ch)...)
	if reg.SecureOps {
		for _, m := range changes {
			if m.add && state.RankForMode(m.mode) != 0 {
				if t := s.net.UserByNick(m.arg); t != nil && !t.Service && !s.secureAllows(reg, s.userLevel(reg, t), m.mode) {
					fix = append(fix, modeChange{false, m.mode, t.Nick})
				}
			}
		}
	}
	s.csMode(ch, fix)
}

// csTopicLocked reports whether a topic lock stops c changing ch's topic.
func (s *Services) csTopicLocked(c *Client, ch *state.Channel) bool {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil || !reg.TopicLock {
		return false
	}
	return !s.levelHas(reg, s.clientLevel(c, reg), "TOPIC")
}

// csTopicChanged keeps a new topic for KEEPTOPIC.
func (s *Services) csTopicChanged(ch *state.Channel) {
	if reg, err := s.store.ChannelByName(ch.Name); err == nil && reg != nil {
		s.csSaveTopic(reg, ch)
	}
}

// applyFounderChanges updates live channels after an account went away.
func (s *Services) applyFounderChanges(changes []store.FounderChange, why string) {
	for _, fc := range changes {
		if fc.NewOwner != "" {
			s.log.Printf("channel %s now belongs to %s (%s)", fc.Channel, fc.NewOwner, why)
			s.logTo(s.chanServ, "Transferring foundership of "+fc.Channel+" from deleted nick "+fc.OldOwner+" to "+fc.NewOwner)
			continue
		}
		s.log.Printf("dropped channel %s (%s, no successor)", fc.Channel, why)
		s.logTo(s.chanServ, "Deleting channel "+fc.Channel+" owned by deleted nick "+fc.OldOwner)
		s.channelGone(fc.Channel)
	}
}

// expireChannels drops channels nobody on their access list joined for
// chan_expire, except those in use right now (NOEXPIRE and suspended
// channels never expire).
func (s *Services) expireChannels(now time.Time) {
	if s.readonly || s.noexpire {
		return
	}
	old, err := s.store.ExpiredChannels(now.Add(-s.cfg.Services.ChanExpire.Duration))
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	for _, reg := range old {
		if s.channelInUse(reg) {
			reg.LastUsed = now
			s.store.SaveChannel(reg)
			continue
		}
		if err := s.store.DeleteChannel(reg.ID); err != nil {
			s.log.Printf("services database: %v", err)
			continue
		}
		s.channelGone(reg.Name)
		s.log.Printf("expired channel %s (last used %s)", reg.Name, reg.LastUsed.UTC().Format(time.DateOnly))
		s.logTo(s.chanServ, "Expiring channel "+reg.Name+" (founder: "+orNoneP(reg.Founder)+")")
	}
}
