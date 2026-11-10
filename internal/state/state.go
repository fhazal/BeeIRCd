// Package state is the network as BeeIRCd sees it: users, channels and who
// is in which channel. Everything is keyed by unique ID (UID), never by nick,
// so linked servers (Stage 2) can share the same model.
//
// Network is not safe for concurrent use; the server owns it from a single
// goroutine.
package state

import (
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// ErrNickInUse means someone else has that nick.
var ErrNickInUse = errors.New("nickname is already in use")

// User is one person (or, later, a service bot) on the network.
type User struct {
	UID      string
	Nick     string
	Ident    string
	Host     string // the host others see
	RealHost string
	IP       string
	Realname string
	Server   string
	Account  string // services account, "" when not identified
	Away     string
	TLS      bool
	Service  bool // NickServ and the other built-in services
	SignOn   time.Time
	NickTS   int64
	Modes    map[byte]bool
	Channels map[string]*Channel // by folded name
}

// Prefix is nick!ident@host.
func (u *User) Prefix() string { return u.Nick + "!" + u.Ident + "@" + u.Host }

// HasMode reports whether user mode m is set.
func (u *User) HasMode(m byte) bool { return u.Modes[m] }

// ModeString is the user modes as "+iw".
func (u *User) ModeString() string {
	return "+" + string(sortedKeys(u.Modes))
}

// Masks are the forms bans are matched against: nick!ident@ with the
// shown host, the real host and the IP.
func (u *User) Masks() []string {
	base := u.Nick + "!" + u.Ident + "@"
	out := []string{base + u.Host}
	if u.RealHost != "" && u.RealHost != u.Host {
		out = append(out, base+u.RealHost)
	}
	if u.IP != "" && u.IP != u.Host && u.IP != u.RealHost {
		out = append(out, base+u.IP)
	}
	return out
}

// ListEntry is one ban, exception or invite exception.
type ListEntry struct {
	Mask  string
	SetBy string
	SetAt time.Time
}

// Member is a user's place in a channel.
type Member struct {
	User  *User
	Op    bool
	Voice bool
}

// Prefix is "@", "+" or "".
func (m *Member) Prefix() string {
	switch {
	case m.Op:
		return "@"
	case m.Voice:
		return "+"
	}
	return ""
}

// AllPrefixes is every status the member has, highest first ("@+").
func (m *Member) AllPrefixes() string {
	p := ""
	if m.Op {
		p += "@"
	}
	if m.Voice {
		p += "+"
	}
	return p
}

// Channel is one channel.
type Channel struct {
	Name    string
	TS      int64
	Modes   map[byte]bool
	Key     string
	Limit   int
	Topic   string
	TopicBy string
	TopicAt time.Time
	Members map[string]*Member   // by UID
	Lists   map[byte][]ListEntry // 'b', 'e', 'I'
	Invited map[string]bool      // UIDs invited since their last join
}

// Member returns u's membership, or nil.
func (c *Channel) Member(u *User) *Member { return c.Members[u.UID] }

// HasMode reports whether channel mode m is set.
func (c *Channel) HasMode(m byte) bool { return c.Modes[m] }

// SortedMembers returns members ordered by nick.
func (c *Channel) SortedMembers() []*Member {
	out := make([]*Member, 0, len(c.Members))
	for _, m := range c.Members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].User.Nick) < irc.Fold(out[j].User.Nick) })
	return out
}

// ModeString returns "+klnt" and its parameters. The key is shown only when
// showKey is true.
func (c *Channel) ModeString(showKey bool) (string, []string) {
	b := []byte{'+'}
	var params []string
	for _, m := range sortedKeys(c.Modes) {
		b = append(b, m)
		switch m {
		case 'k':
			if showKey {
				params = append(params, c.Key)
			} else {
				params = append(params, "*")
			}
		case 'l':
			params = append(params, strconv.Itoa(c.Limit))
		}
	}
	return string(b), params
}

// Matches reports whether any entry of list matches u.
func (c *Channel) Matches(list byte, u *User) bool {
	for _, e := range c.Lists[list] {
		for _, m := range u.Masks() {
			if irc.Match(e.Mask, m) {
				return true
			}
		}
	}
	return false
}

// IsBanned reports whether u is banned and not excepted.
func (c *Channel) IsBanned(u *User) bool { return c.Matches('b', u) && !c.Matches('e', u) }

// AddListEntry adds mask to list. It returns false when the mask is already
// there.
func (c *Channel) AddListEntry(list byte, mask, by string, at time.Time) bool {
	for _, e := range c.Lists[list] {
		if irc.EqualFold(e.Mask, mask) {
			return false
		}
	}
	c.Lists[list] = append(c.Lists[list], ListEntry{Mask: mask, SetBy: by, SetAt: at})
	return true
}

// RemoveListEntry removes mask from list and returns the stored form.
func (c *Channel) RemoveListEntry(list byte, mask string) (string, bool) {
	entries := c.Lists[list]
	for i, e := range entries {
		if irc.EqualFold(e.Mask, mask) {
			c.Lists[list] = append(entries[:i:i], entries[i+1:]...)
			return e.Mask, true
		}
	}
	return "", false
}

// Network is every user and channel.
type Network struct {
	SID      string
	users    map[string]*User
	nicks    map[string]*User
	channels map[string]*Channel
	next     uint64
}

// NewNetwork starts an empty network for server ID sid.
func NewNetwork(sid string) *Network {
	return &Network{
		SID:      sid,
		users:    make(map[string]*User),
		nicks:    make(map[string]*User),
		channels: make(map[string]*Channel),
	}
}

const uidChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NextUID returns a new user ID: the server ID plus 6 characters.
func (n *Network) NextUID() string {
	v := n.next
	n.next++
	id := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		id[i] = uidChars[v%36]
		v /= 36
	}
	return n.SID + string(id)
}

// AddUser adds a registered user.
func (n *Network) AddUser(u *User) error {
	if n.nicks[irc.Fold(u.Nick)] != nil {
		return ErrNickInUse
	}
	if u.Modes == nil {
		u.Modes = make(map[byte]bool)
	}
	if u.Channels == nil {
		u.Channels = make(map[string]*Channel)
	}
	n.users[u.UID] = u
	n.nicks[irc.Fold(u.Nick)] = u
	return nil
}

// RemoveUser takes u out of every channel and off the network.
func (n *Network) RemoveUser(u *User) {
	for _, ch := range u.Channels {
		n.Part(u, ch)
	}
	delete(n.users, u.UID)
	if n.nicks[irc.Fold(u.Nick)] == u {
		delete(n.nicks, irc.Fold(u.Nick))
	}
}

// ChangeNick renames u. Changing only the case of one's own nick is allowed.
func (n *Network) ChangeNick(u *User, nick string, ts int64) error {
	if other := n.nicks[irc.Fold(nick)]; other != nil && other != u {
		return ErrNickInUse
	}
	delete(n.nicks, irc.Fold(u.Nick))
	u.Nick = nick
	u.NickTS = ts
	n.nicks[irc.Fold(nick)] = u
	return nil
}

// NickInUse reports whether a registered user has nick.
func (n *Network) NickInUse(nick string) bool { return n.nicks[irc.Fold(nick)] != nil }

// UserByNick finds a user by nick, or nil.
func (n *Network) UserByNick(nick string) *User { return n.nicks[irc.Fold(nick)] }

// UserByUID finds a user by ID, or nil.
func (n *Network) UserByUID(uid string) *User { return n.users[uid] }

// Users returns every user, ordered by nick.
func (n *Network) Users() []*User {
	out := make([]*User, 0, len(n.users))
	for _, u := range n.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Nick) < irc.Fold(out[j].Nick) })
	return out
}

// UserCount is the number of users.
func (n *Network) UserCount() int { return len(n.users) }

// Channel finds a channel by name, or nil.
func (n *Network) Channel(name string) *Channel { return n.channels[irc.Fold(name)] }

// Channels returns every channel, ordered by name.
func (n *Network) Channels() []*Channel {
	out := make([]*Channel, 0, len(n.channels))
	for _, c := range n.channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Name) < irc.Fold(out[j].Name) })
	return out
}

// ChannelCount is the number of channels.
func (n *Network) ChannelCount() int { return len(n.channels) }

// Join puts u in channel name, creating it (with u as op and +nt) when it
// doesn't exist. created reports whether it was new.
func (n *Network) Join(u *User, name string, ts int64) (ch *Channel, m *Member, created bool) {
	key := irc.Fold(name)
	ch = n.channels[key]
	if ch == nil {
		ch = &Channel{
			Name:    name,
			TS:      ts,
			Modes:   map[byte]bool{'n': true, 't': true},
			Members: make(map[string]*Member),
			Lists:   make(map[byte][]ListEntry),
			Invited: make(map[string]bool),
		}
		n.channels[key] = ch
		created = true
	}
	if m = ch.Members[u.UID]; m != nil {
		return ch, m, false
	}
	m = &Member{User: u, Op: created}
	ch.Members[u.UID] = m
	u.Channels[key] = ch
	delete(ch.Invited, u.UID)
	return ch, m, created
}

// Part takes u out of ch. An empty channel is removed.
func (n *Network) Part(u *User, ch *Channel) {
	delete(ch.Members, u.UID)
	delete(u.Channels, irc.Fold(ch.Name))
	if len(ch.Members) == 0 {
		delete(n.channels, irc.Fold(ch.Name))
	}
}

// Neighbours returns everyone who shares a channel with u, not u itself.
func (n *Network) Neighbours(u *User) []*User {
	seen := map[string]bool{u.UID: true}
	var out []*User
	for _, ch := range u.Channels {
		for uid, m := range ch.Members {
			if !seen[uid] {
				seen[uid] = true
				out = append(out, m.User)
			}
		}
	}
	return out
}

// SharesChannel reports whether a and b are in a common channel.
func SharesChannel(a, b *User) bool {
	for key := range a.Channels {
		if _, ok := b.Channels[key]; ok {
			return true
		}
	}
	return false
}

func sortedKeys(m map[byte]bool) []byte {
	out := make([]byte, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
