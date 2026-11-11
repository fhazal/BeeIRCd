package server

import (
	"time"

	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// These run from the normal commands so registered channels look after
// themselves.

// csJoinLevel is u's level on channel name, before joining (0 when the
// channel isn't registered). UOP and up get past +i and +k.
func (s *Server) csJoinLevel(c *Client, name string) int {
	if c.accountID == 0 {
		return store.LevelNone
	}
	reg, err := s.store.ChannelByName(name)
	if err != nil || reg == nil {
		return store.LevelNone
	}
	lvl, _ := s.store.Level(reg, c.accountID)
	return lvl
}

// csJoin runs after u joined ch. created means u opened the channel.
func (s *Server) csJoin(c *Client, ch *state.Channel, created bool) {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil {
		return
	}
	u := c.user
	lvl, _ := s.store.Level(reg, c.accountID)
	if created {
		if reg.KeepTopic && reg.Topic != "" {
			s.emit(&state.Topic{Source: s.chanServ.user.UID, Channel: ch.Name, Text: reg.Topic, By: reg.TopicBy, At: reg.TopicAt})
		}
		s.csMode(ch, mlockChanges(reg, ch))
		if lvl < store.LevelAOP {
			// Opening an empty registered channel doesn't make you its op.
			s.csMode(ch, []modeChange{{false, 'o', u.Nick}})
		}
	}
	if lvl == store.LevelNone {
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
	} else {
		reg.LastUsed = time.Now()
		s.store.SaveChannel(reg)
		s.csAutoModes(ch, u, lvl)
	}
	if reg.EntryMsg != "" {
		s.cs(c, "[%s] %s", ch.Name, reg.EntryMsg)
	}
}

// csOnIdentify gives a user who just identified the modes they earn in the
// channels they are already in.
func (s *Server) csOnIdentify(c *Client) {
	for _, ch := range c.user.Channels {
		reg, err := s.store.ChannelByName(ch.Name)
		if err != nil || reg == nil {
			continue
		}
		lvl, _ := s.store.Level(reg, c.accountID)
		if lvl > store.LevelNone {
			reg.LastUsed = time.Now()
			s.store.SaveChannel(reg)
			s.csAutoModes(ch, c.user, lvl)
		}
	}
}

// csAfterUserModes puts the mode lock back and, with SECURE, takes ops
// from people below AOP.
func (s *Server) csAfterUserModes(ch *state.Channel, changes []modeChange) {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil {
		return
	}
	fix := mlockChanges(reg, ch)
	if reg.Secure {
		for _, m := range changes {
			if m.add && m.mode != 'v' && state.RankForMode(m.mode) != 0 {
				if t := s.net.UserByNick(m.arg); t != nil && s.levelOf(reg, t) < csSecureLevel(m.mode) {
					fix = append(fix, modeChange{false, m.mode, t.Nick})
				}
			}
		}
	}
	s.csMode(ch, fix)
}

// csTopicLocked reports whether TOPICLOCK stops c changing ch's topic.
func (s *Server) csTopicLocked(c *Client, ch *state.Channel) bool {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil || !reg.TopicLock {
		return false
	}
	lvl, _ := s.store.Level(reg, c.accountID)
	return lvl < store.LevelAOP
}

// csTopicChanged keeps a new topic for KEEPTOPIC.
func (s *Server) csTopicChanged(ch *state.Channel) {
	if reg, err := s.store.ChannelByName(ch.Name); err == nil && reg != nil {
		s.csSaveTopic(reg, ch)
	}
}

// applyFounderChanges updates live channels after an account went away.
func (s *Server) applyFounderChanges(changes []store.FounderChange, why string) {
	for _, fc := range changes {
		if fc.NewOwner != "" {
			s.log.Printf("channel %s now belongs to %s (%s)", fc.Channel, fc.NewOwner, why)
			continue
		}
		s.log.Printf("dropped channel %s (%s, no successor)", fc.Channel, why)
		if live := s.net.Channel(fc.Channel); live != nil {
			s.csMode(live, []modeChange{{false, 'r', ""}})
		}
	}
}

// expireChannels drops channels nobody on their access list joined for
// chan_expire, except those in use right now.
func (s *Server) expireChannels(now time.Time) {
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
		if live := s.net.Channel(reg.Name); live != nil {
			s.csMode(live, []modeChange{{false, 'r', ""}})
		}
		s.log.Printf("expired channel %s (last used %s)", reg.Name, reg.LastUsed.UTC().Format(time.DateOnly))
	}
}

// memoToChannel sends a memo to a channel's founder and SOPs.
func (s *Server) memoToChannel(c *Client, from *store.Account, name, text string) {
	reg, err := s.store.ChannelByName(name)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	if reg == nil {
		s.ms(c, "%s isn't registered with ChanServ.", name)
		return
	}
	ids := []int64{reg.FounderID}
	sops, err := s.store.AccessList(reg, store.LevelSOP)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	for _, e := range sops {
		ids = append(ids, e.AccountID)
	}
	sent := 0
	for _, id := range ids {
		to, err := s.store.AccountByID(id)
		if err != nil || to == nil || to.ID == from.ID {
			continue
		}
		if ig, _ := s.store.Ignores(to, from.Name); ig {
			continue
		}
		memos, err := s.store.Memos(to)
		if err != nil || len(memos) >= to.MemoLimit {
			continue
		}
		if err := s.store.AddMemo(to, from.Name, "["+reg.Name+"] "+text, time.Now()); err != nil {
			continue
		}
		sent++
		if to.MemoNotify {
			for _, oc := range s.onlineClients(to.ID) {
				s.ms(oc, "You have a new memo from %s. Read it: /ms read %d", from.Name, len(memos)+1)
			}
		}
	}
	s.ms(c, "Memo sent to the founder and SOPs of %s (%s).", reg.Name, plural(sent, "person"))
}
