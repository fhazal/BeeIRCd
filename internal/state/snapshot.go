package state

import (
	"sort"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Snapshot is a network as plain data, so one server process can hand it
// to the next (the seamless upgrade in internal/server). Unlike a link
// burst it keeps everything as it is: join times, hidden members, invites.
// It is made to be written with encoding/gob: strings stay the bytes they
// are. (gob leaves out empty values, so a pointer to "" comes back nil:
// User.OperTag, which only users of other servers have.)
type Snapshot struct {
	SID  string
	Next uint64 // the UID counter
	// Users are copies with Channels nil: membership is in Channels below.
	Users    []*User
	Channels []ChannelSnap
}

// ChannelSnap is one channel in a Snapshot. It mirrors Channel field for
// field (a test counts them), with members and invites by UID.
type ChannelSnap struct {
	Name    string
	TS      int64
	Modes   string // the mode letters, sorted
	Key     string
	Limit   int
	Topic   string
	TopicBy string
	TopicAt time.Time
	Members []MemberSnap
	Lists   map[string][]ListEntry // "b", "e", "I"
	Invited []string
	Params  map[string]string // by mode letter
}

// MemberSnap is one member of a ChannelSnap.
type MemberSnap struct {
	UID    string
	Ranks  Rank
	Hidden bool
	Joined time.Time
}

// Snapshot copies the whole network. Users and channels are ordered, so
// the same network always gives the same snapshot.
func (n *Network) Snapshot() *Snapshot {
	s := &Snapshot{SID: n.SID, Next: n.next}
	for _, u := range n.Users() {
		cp := u.Copy()
		cp.Channels = nil
		cp.Groups = append([]string(nil), u.Groups...)
		cp.OperSwhois = append([]string(nil), u.OperSwhois...)
		s.Users = append(s.Users, cp)
	}
	sort.SliceStable(s.Users, func(i, j int) bool { return s.Users[i].UID < s.Users[j].UID })
	for _, ch := range n.Channels() {
		c := ChannelSnap{Name: ch.Name, TS: ch.TS, Modes: string(sortedKeys(ch.Modes)), Key: ch.Key, Limit: ch.Limit,
			Topic: ch.Topic, TopicBy: ch.TopicBy, TopicAt: ch.TopicAt}
		for _, m := range ch.Members {
			c.Members = append(c.Members, MemberSnap{UID: m.User.UID, Ranks: m.Ranks, Hidden: m.Hidden, Joined: m.Joined})
		}
		sort.Slice(c.Members, func(i, j int) bool { return c.Members[i].UID < c.Members[j].UID })
		for l, entries := range ch.Lists {
			if len(entries) == 0 {
				continue
			}
			if c.Lists == nil {
				c.Lists = make(map[string][]ListEntry)
			}
			c.Lists[string(l)] = append([]ListEntry(nil), entries...)
		}
		for uid := range ch.Invited {
			c.Invited = append(c.Invited, uid)
		}
		sort.Strings(c.Invited)
		for m, v := range ch.Params {
			if c.Params == nil {
				c.Params = make(map[string]string)
			}
			c.Params[string(m)] = v
		}
		s.Channels = append(s.Channels, c)
	}
	return s
}

// Remap renames UIDs all through the snapshot: to[old] is the new UID, ""
// drops that user (and its memberships and invites). UIDs not in to stay.
func (s *Snapshot) Remap(to map[string]string) {
	if len(to) == 0 {
		return
	}
	users := s.Users[:0]
	for _, u := range s.Users {
		if id, ok := to[u.UID]; ok {
			if id == "" {
				continue
			}
			u.UID = id
		}
		users = append(users, u)
	}
	s.Users = users
	for i := range s.Channels {
		c := &s.Channels[i]
		members := c.Members[:0]
		for _, m := range c.Members {
			if id, ok := to[m.UID]; ok {
				if id == "" {
					continue
				}
				m.UID = id
			}
			members = append(members, m)
		}
		c.Members = members
		invited := c.Invited[:0]
		for _, uid := range c.Invited {
			if id, ok := to[uid]; ok {
				if id == "" {
					continue
				}
				uid = id
			}
			invited = append(invited, uid)
		}
		c.Invited = invited
	}
}

// Reserve makes sure NextUID never gives out a UID below the counter
// value next (a snapshot's Next), so new UIDs can't collide with the
// snapshot's users.
func (n *Network) Reserve(next uint64) {
	if next > n.next {
		n.next = next
	}
}

// Load puts a snapshot's users and channels into n, without events: a
// server that just started (its own service bots are already there) takes
// over the network of the process before it. A service bot of the snapshot
// that n has too (same UID and nick) stays as n has it and keeps its
// channels. Any other user whose UID or nick is taken is left out; skipped
// lists their UIDs. A channel n already has (one a service bot keeps open)
// takes the snapshot's settings and keeps its own members too. Members and
// invites of users the snapshot doesn't bring are dropped.
func (n *Network) Load(s *Snapshot) (skipped []string) {
	n.Reserve(s.Next)
	in := make(map[string]bool, len(s.Users)) // the snapshot's users that are in n now
	for _, u := range s.Users {
		if have := n.users[u.UID]; have != nil && have.Service && u.Service && irc.Fold(have.Nick) == irc.Fold(u.Nick) {
			in[u.UID] = true
			continue
		}
		if u.UID == "" || n.users[u.UID] != nil || n.nicks[irc.Fold(u.Nick)] != nil || n.addUser(u.Copy()) != nil {
			skipped = append(skipped, u.UID)
			continue
		}
		in[u.UID] = true
	}
	for i := range s.Channels {
		c := &s.Channels[i]
		key := irc.Fold(c.Name)
		ch := n.channels[key]
		if ch == nil {
			ch = &Channel{Members: make(map[string]*Member), net: n}
			n.channels[key] = ch
		}
		ch.Name, ch.TS, ch.Key, ch.Limit = c.Name, c.TS, c.Key, c.Limit
		ch.Topic, ch.TopicBy, ch.TopicAt = c.Topic, c.TopicBy, c.TopicAt
		ch.Modes = make(map[byte]bool, len(c.Modes))
		for j := 0; j < len(c.Modes); j++ {
			ch.Modes[c.Modes[j]] = true
		}
		ch.Lists = make(map[byte][]ListEntry)
		for l, entries := range c.Lists {
			if len(l) == 1 {
				ch.Lists[l[0]] = append([]ListEntry(nil), entries...)
			}
		}
		ch.Params = nil
		for m, v := range c.Params {
			if len(m) != 1 {
				continue
			}
			if ch.Params == nil {
				ch.Params = make(map[byte]string)
			}
			ch.Params[m[0]] = v
		}
		ch.Invited = make(map[string]bool)
		for _, uid := range c.Invited {
			if in[uid] {
				ch.Invited[uid] = true
			}
		}
		for _, m := range c.Members {
			u := n.users[m.UID]
			if u == nil || !in[m.UID] {
				continue
			}
			ch.Members[u.UID] = &Member{User: u, Ranks: m.Ranks, Hidden: m.Hidden, Joined: m.Joined}
			u.Channels[key] = ch
		}
		n.dropIfEmpty(ch)
	}
	return skipped
}
