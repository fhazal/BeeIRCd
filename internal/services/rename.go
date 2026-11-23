package services

import (
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// RenameRights says what ChanServ thinks of uid renaming channel name
// (IRCv3 channel-rename): registered is false when the channel isn't
// registered, and the server's chanop rule applies. On a registered
// channel ok is true for the founder and whoever has the SET privilege;
// why says what stops them otherwise.
func (s *Services) RenameRights(uid, name string) (registered, ok bool, why string) {
	reg, err := s.store.ChannelByName(name)
	if err != nil || reg == nil {
		return false, false, ""
	}
	if reg.Suspended(s.now()) {
		return true, false, reg.Name + " is suspended"
	}
	c := s.clients[uid]
	if c == nil || c.closed {
		return true, false, "only the founder of " + reg.Name + " or someone with SET access can rename it"
	}
	if s.chanAccount(c, reg) == 0 {
		return true, false, reg.Name + " is registered: identify to NickServ first"
	}
	if !s.levelHas(reg, s.clientLevel(c, reg), "SET") {
		return true, false, "only the founder of " + reg.Name + " or someone with SET access can rename it"
	}
	return true, true, ""
}

// Registered reports whether channel name is registered with ChanServ.
func (s *Services) Registered(name string) bool {
	reg, err := s.store.ChannelByName(name)
	return err == nil && reg != nil
}

// renamed moves what services keep in memory under channel e.Old to e.New.
// The server has already moved the stored data (store.RenameChannel), so
// nothing here needs saving again.
func (s *Services) renamed(e *state.Rename) {
	oldKey, newKey := irc.Fold(e.Old), irc.Fold(e.New)
	if oldKey != newKey {
		if s.bsWatch[oldKey] {
			delete(s.bsWatch, oldKey)
			s.bsWatch[newKey] = true
		}
		if r := s.kickRuns[oldKey]; r != nil {
			delete(s.kickRuns, oldKey)
			s.kickRuns[newKey] = r
		}
		for _, a := range s.amsg {
			if a.channel == oldKey {
				a.channel = newKey
			}
		}
	}
	for key, seen := range s.seen {
		if seen.Channel != "" && irc.Fold(seen.Channel) == oldKey {
			cp := *seen
			cp.Channel = e.New
			s.seen[key] = &cp
		}
	}
}
