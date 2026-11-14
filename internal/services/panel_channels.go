package services

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// What the Hive Panel's Channels page asks of services. The same
// paths as ChanServ TOPIC / SUSPEND / UNSUSPEND for opers, without an IRC
// client: by names the oper. Answers are in words for the oper.

// RegChannels are the channels registered with ChanServ.
func (s *Services) RegChannels() []*store.Channel {
	all, _ := s.store.Channels()
	return all
}

// RegChannel is the registration of name, nil when it isn't registered.
func (s *Services) RegChannel(name string) *store.Channel {
	reg, err := s.store.ChannelByName(name)
	if err != nil {
		return nil
	}
	return reg
}

// PanelChanNotice sends text to everyone in channel, from Global, marked
// with the oper's name.
func (s *Services) PanelChanNotice(by, channel, text string) string {
	live := s.net.Channel(channel)
	if live == nil {
		return channel + " is empty right now."
	}
	s.host.ChannelMessage(s.global.user.UID, live.Name, 0, false, "["+by+"] "+text)
	s.snotice("%s (Hive Panel) sent a notice to %s", by, live.Name)
	s.Flush()
	return ""
}

// PanelChanTopic sets the topic of channel as ChanServ (and keeps it for
// a registered channel, like ChanServ TOPIC).
func (s *Services) PanelChanTopic(by, channel, text string) string {
	live := s.net.Channel(channel)
	if live == nil {
		return channel + " is empty right now."
	}
	topic := irc.Truncate(strings.Join(strings.Fields(text), " "), s.cfg.Limits.TopicLen)
	s.emit(&state.Topic{Source: s.chanServ.user.UID, Channel: live.Name, Text: topic, By: by, At: time.Now()})
	if reg := s.RegChannel(live.Name); reg != nil {
		s.csSaveTopic(reg, live)
	}
	s.snotice("%s (Hive Panel) set the topic of %s", by, live.Name)
	s.Flush()
	return ""
}

// PanelChanSuspend closes a registered channel like ChanServ SUSPEND:
// non-opers are kicked out and can't come back. life 0 = until unsuspended.
func (s *Services) PanelChanSuspend(by, channel, reason string, life time.Duration) (kicked int, problem string) {
	reg := s.RegChannel(channel)
	switch {
	case reg == nil:
		return 0, channel + " isn't registered with ChanServ, so it can't be suspended."
	case reg.Suspended(time.Now()):
		return 0, reg.Name + " is suspended already (by " + reg.SuspendedBy + ")."
	}
	reason = irc.Truncate(strings.Join(strings.Fields(reason), " "), 200)
	if reason == "" {
		reason = "No reason given"
	}
	now := time.Now()
	reg.SuspendedBy, reg.SuspendReason, reg.SuspendedAt, reg.SuspendUntil = by, reason, now, time.Unix(0, 0)
	if life > 0 {
		reg.SuspendUntil = now.Add(life)
	}
	if err := s.store.SaveChannel(reg); err != nil {
		s.log.Printf("services database: %v", err)
		return 0, "Something went wrong on the server. Please try again in a moment."
	}
	kicked = s.enforceKick(reg, func(u *state.User) bool { return !s.isOperUser(u) }, false, "This channel is suspended: "+reason)
	s.snotice("%s (Hive Panel) suspended %s: %s", by, reg.Name, reason)
	s.Flush()
	return kicked, ""
}

// PanelChanUnsuspend opens a suspended channel again.
func (s *Services) PanelChanUnsuspend(by, channel string) string {
	reg := s.RegChannel(channel)
	switch {
	case reg == nil:
		return channel + " isn't registered with ChanServ."
	case reg.SuspendedBy == "":
		return reg.Name + " isn't suspended."
	}
	s.unsuspend(reg)
	s.snotice("%s (Hive Panel) unsuspended %s", by, reg.Name)
	s.Flush()
	return ""
}
