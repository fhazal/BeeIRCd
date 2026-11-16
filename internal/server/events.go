package server

import (
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// emit is how the server changes the network: it applies ev, tells the
// local clients who should see it, and hands ev to every sink (server links,
// tests). Command handlers check permissions and send their
// own numerics; everything others see comes from here, so a change that
// arrives over a link later reaches local clients the same way.
func (s *Server) emit(ev state.Event) error {
	org := s.origin // for this event only, not what it sets off
	s.origin = nil
	if s.beforeEmit != nil {
		s.beforeEmit(ev)
	}
	secure := s.beforeModeEvent(ev)
	deliver := s.prepare(ev)
	mods := s.modPrepare(ev)
	watch := s.monitorPrepare(ev)
	histGone := s.historyChannels(ev)
	if err := s.net.Apply(ev); err != nil {
		return err
	}
	s.groupsAfter(ev)
	if deliver != nil {
		deliver()
	}
	if watch != nil {
		watch()
	}
	if cm, ok := ev.(*state.ChannelMode); ok {
		s.noteTimedBans(cm)
		s.historyModeChanged(cm)
	}
	s.dropGoneHistory(histGone)
	s.linkOut(ev, org)
	for _, sink := range s.sinks {
		sink(ev)
	}
	s.afterModeEvent(ev, secure)
	if mods != nil {
		mods()
	}
	return nil
}

// prepare looks at the network before ev changes it and returns what
// delivers ev afterwards. Leaving events need the old member lists, nick
// changes the old prefix.
func (s *Server) prepare(ev state.Event) func() {
	switch e := ev.(type) {
	case *state.UserQuit:
		u := s.net.UserByUID(e.UID)
		if u == nil {
			return nil
		}
		o := s.out(u, "QUIT", e.Reason).text()
		to := s.net.Neighbours(u)
		return func() {
			s.deliverAll(to, o)
			s.addWhowas(u)
		}
	case *state.NickChange:
		u := s.net.UserByUID(e.UID)
		if u == nil {
			return nil
		}
		o := s.out(u, "NICK", e.Nick)
		return func() {
			s.addWhowasNick(e.Old, u)
			s.toNeighbours(u, o, true)
		}
	case *state.Join:
		return func() {
			u, ch := s.net.UserByUID(e.UID), s.net.Channel(e.Channel)
			join := s.out(u, "JOIN", ch.Name)
			join.extParams = []string{ch.Name, accountOrStar(u), u.Realname}
			// +D: only they see it, until they talk; +u: only the ops do.
			to := seers(ch, u)
			s.deliverAll(to, join)
			if u.Away != "" {
				away := s.out(u, "AWAY", u.Away).text()
				away.needCap = "away-notify"
				for _, o := range to {
					if o != u {
						s.deliverTo(o, away)
					}
				}
			}
		}
	case *state.Part:
		u, ch := s.net.UserByUID(e.UID), s.net.Channel(e.Channel)
		if u == nil || ch == nil {
			return nil
		}
		params := []string{ch.Name}
		if e.Reason != "" {
			params = append(params, e.Reason)
		}
		o := s.out(u, "PART", params...)
		o.msg.Trailing = e.Reason != ""
		to := seers(ch, u)
		return func() { s.deliverAll(to, o) }
	case *state.Kick:
		t, ch := s.net.UserByUID(e.UID), s.net.Channel(e.Channel)
		if t == nil || ch == nil {
			return nil
		}
		o := s.outFrom(e.Source, "KICK", ch.Name, t.Nick, e.Reason).text()
		to := seers(ch, t)
		if src := s.net.UserByUID(e.Source); src != nil && src != t && ch.Member(t) != nil && !ch.Sees(src, ch.Member(t)) {
			to = append(to, src)
		}
		return func() { s.deliverAll(to, o) }
	case *state.Reveal:
		return func() {
			u, ch := s.net.UserByUID(e.UID), s.net.Channel(e.Channel)
			if u == nil || ch == nil {
				return
			}
			join := s.out(u, "JOIN", ch.Name)
			join.extParams = []string{ch.Name, accountOrStar(u), u.Realname}
			for _, o := range seers(ch, u) {
				if o == u {
					continue
				}
				s.deliverTo(o, join)
				if u.Away != "" {
					away := s.out(u, "AWAY", u.Away).text()
					away.needCap = "away-notify"
					s.deliverTo(o, away)
				}
			}
		}
	case *state.Topic:
		return func() {
			ch := s.net.Channel(e.Channel)
			s.broadcast(ch, s.outFrom(e.Source, "TOPIC", ch.Name, e.Text).text(), nil)
		}
	case *state.ChannelMode:
		name := e.Channel
		if ch := s.net.Channel(e.Channel); ch != nil {
			name = ch.Name
		}
		return func() {
			ch := s.net.Channel(e.Channel)
			for _, line := range modeLines(fromState(e.Changes)) {
				o := s.outFrom(e.Source, "MODE", append([]string{name}, line...)...)
				if ch == nil { // -P closed the empty channel: tell whoever did it
					if src := s.net.UserByUID(e.Source); src != nil {
						s.deliverTo(src, o)
					}
					continue
				}
				s.broadcast(ch, o, nil)
			}
		}
	case *state.UserMode:
		return func() {
			u := s.net.UserByUID(e.UID)
			c := s.clientOf(u)
			if c == nil || len(e.Changes) == 0 {
				return
			}
			src := s.cfg.Server.Name
			if e.Source == e.UID {
				src = u.Nick
			} else if o := s.net.UserByUID(e.Source); o != nil {
				src = o.Prefix()
			}
			c.send(irc.Message{Source: src, Command: "MODE", Params: []string{u.Nick, formatModes(fromState(e.Changes))[0]}}.String())
		}
	case *state.Away:
		u := s.net.UserByUID(e.UID)
		if u == nil || u.Away == e.Message {
			return nil
		}
		return func() {
			o := s.out(u, "AWAY")
			if e.Message != "" {
				o = s.out(u, "AWAY", e.Message).text()
			}
			o.needCap = "away-notify"
			s.toNeighbours(u, o, false)
			s.toWatchers(u, o)
		}
	case *state.Login:
		return func() {
			u := s.net.UserByUID(e.UID)
			o := s.out(u, "ACCOUNT", accountOrStar(u))
			o.needCap = "account-notify"
			s.toNeighbours(u, o, true)
			s.toWatchers(u, o)
		}
	case *state.HostChange:
		u := s.net.UserByUID(e.UID)
		if u == nil {
			return nil
		}
		ident := u.Ident
		if e.Ident != "" {
			ident = e.Ident
		}
		if u.Host == e.Host && ident == u.Ident {
			return nil
		}
		o := s.out(u, "CHGHOST", ident, e.Host)
		o.needCap = "chghost"
		return func() { s.toNeighbours(u, o, true); s.toWatchers(u, o) }
	case *state.RealnameChange:
		u := s.net.UserByUID(e.UID)
		if u == nil || u.Realname == e.Realname {
			return nil
		}
		return func() {
			o := s.out(u, "SETNAME", e.Realname).text()
			o.needCap = "setname"
			s.toNeighbours(u, o, true)
			s.toWatchers(u, o)
		}
	case *state.Invite:
		if e.Remove {
			return func() {
				name := e.Channel
				if ch := s.net.Channel(e.Channel); ch != nil {
					name = ch.Name
				}
				by := s.cfg.Server.Name
				if u := s.net.UserByUID(e.Source); u != nil {
					by = u.Nick
				}
				if tc := s.clients[e.UID]; tc != nil && tc.user != nil {
					s.reply(tc, "493", "You were uninvited from "+name+" by "+by)
				}
			}
		}
		return func() {
			t := s.net.UserByUID(e.UID)
			name := e.Channel
			if ch := s.net.Channel(e.Channel); ch != nil {
				name = ch.Name
				notify := s.outFrom(e.Source, "INVITE", t.Nick, ch.Name)
				notify.needCap = "invite-notify"
				for _, m := range ch.Members {
					if m.AtLeast(state.RankHalfop) && m.User.UID != e.Source {
						s.deliverTo(m.User, notify)
					}
				}
			}
			s.deliverTo(t, s.outFrom(e.Source, "INVITE", t.Nick, name))
		}
	}
	return nil
}

// outFrom starts a message from the user with uid, or from the server when
// there is none.
func (s *Server) outFrom(uid, cmd string, params ...string) *outMsg {
	if u := s.net.UserByUID(uid); u != nil {
		return s.out(u, cmd, params...)
	}
	o := s.out(&state.User{Nick: s.cfg.Server.Name}, cmd, params...)
	o.msg.Source = s.cfg.Server.Name
	return o
}

func members(ch *state.Channel) []*state.User {
	out := make([]*state.User, 0, len(ch.Members))
	for _, m := range ch.Members {
		out = append(out, m.User)
	}
	return out
}

func (s *Server) deliverAll(to []*state.User, o *outMsg) {
	for _, u := range to {
		s.deliverTo(u, o)
	}
}

// modeLines splits changes into MODE parameter lists of at most
// maxModeParams arguments each.
func modeLines(changes []modeChange) [][]string {
	var out [][]string
	for len(changes) > 0 {
		n, params := 0, 0
		for n < len(changes) {
			if changes[n].arg != "" {
				if params == maxModeParams {
					break
				}
				params++
			}
			n++
		}
		out = append(out, formatModes(changes[:n]))
		changes = changes[n:]
	}
	return out
}

func fromState(list []state.ModeChange) []modeChange {
	out := make([]modeChange, len(list))
	for i, m := range list {
		out[i] = modeChange{m.Add, m.Mode, m.Arg}
	}
	return out
}

// umodes builds user mode changes like "+os".
func umodes(add bool, modes string) []state.ModeChange {
	out := make([]state.ModeChange, len(modes))
	for i := range modes {
		out[i] = state.ModeChange{Add: add, Mode: modes[i]}
	}
	return out
}
