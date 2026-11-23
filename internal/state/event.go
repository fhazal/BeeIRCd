package state

import (
	"errors"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Event is one change to the network. Every change goes through
// Network.Apply as an event, so the same events can be delivered to local
// clients and sent to linked servers, which apply them to
// their own copy of the network.
//
// Events name users by UID and channels by name, never by pointer, so they
// can be written to a link as they are. Apply fills in what only the
// network knows (the effective mode changes, a removed ban's stored form)
// so that everyone who sees the event afterwards sees what really changed.
type Event interface{ event() }

// UserAdd puts a new user on the network. User is the full user; Apply
// takes it as it is.
type UserAdd struct{ User *User }

// UserQuit takes a user off the network: QUIT, KILL, a lost connection.
type UserQuit struct {
	UID    string
	Reason string
}

// NickChange renames a user. Apply sets Old to the old nick!ident@host.
type NickChange struct {
	UID  string
	Nick string
	TS   int64
	Old  string
}

// Join puts a user in a channel, creating it when it doesn't exist. The
// creator becomes op unless Ranks says otherwise. Apply sets Created and
// Ranks to what happened.
type Join struct {
	UID     string
	Channel string
	TS      int64
	Ranks   Rank
	Created bool
	// Burst: a join from a server link's sync (SJOIN). A channel it
	// creates gets no default modes and the member exactly Ranks (0 =
	// none), and +D doesn't hide it.
	Burst bool
}

// ChannelTS sets a channel's creation time: an older one from a linked
// server wins (UnrealIRCd's SJOIN rule).
type ChannelTS struct {
	Channel string
	TS      int64
}

// Part takes a user out of a channel.
type Part struct {
	UID     string
	Channel string
	Reason  string
}

// Kick takes UID out of a channel; Source did it.
type Kick struct {
	Source  string
	UID     string
	Channel string
	Reason  string
}

// Reveal shows a hidden +D member to the rest of the channel.
type Reveal struct {
	UID     string
	Channel string
}

// Topic sets a channel's topic. Source is who did it; By is the nick shown
// as setter.
type Topic struct {
	Source  string
	Channel string
	Text    string
	By      string
	At      time.Time
}

// ModeChange is one mode letter going on or off. For member ranks (q a o h
// v) UID is the target and Arg its nick; for lists Arg is the mask; for k
// and l the key and limit.
type ModeChange struct {
	Add  bool
	Mode byte
	Arg  string
	UID  string
}

// ChannelMode changes a channel's modes. By and At go on new list entries.
// Apply keeps in Changes only what really changed.
type ChannelMode struct {
	Source  string // UID, "" for the server
	Channel string
	By      string
	At      time.Time
	Changes []ModeChange
}

// UserMode changes a user's modes. Apply keeps in Changes only what really
// changed.
type UserMode struct {
	Source  string // UID, "" for the server
	UID     string
	Changes []ModeChange
}

// Away sets ("" clears) a user's away message.
type Away struct {
	UID     string
	Message string
}

// Login sets the services account a user is identified to; "" logs out.
type Login struct {
	UID     string
	Account string
}

// HostChange changes the host (and with Ident, the ident) others see.
// Ident "" keeps the shown ident; Ident equal to the real one drops a
// vident.
type HostChange struct {
	UID   string
	Host  string
	Ident string
}

// RealnameChange sets a user's real name (SETNAME, CHGNAME).
type RealnameChange struct {
	UID      string
	Realname string
}

// Invite lets UID past +i, +k and +l of a channel once. Source invited.
// The channel need not exist.
type Invite struct {
	Source  string
	UID     string
	Channel string
	// Remove takes the invite back (UNINVITE).
	Remove bool
}

// Rename gives channel Old the name New (IRCv3 draft/channel-rename). The
// channel keeps its members, modes, lists and topic. Source is the UID or
// SID that asked; Reason may be empty.
//
// Merge: New is already another channel (a rename from another server met
// a channel just made here). Old's members join it with their ranks, its
// metadata keys New lacks come along, and Old is gone; the server settles
// modes, lists, ranks and age first, by the SJOIN rules.
type Rename struct {
	Source string
	Old    string
	New    string
	Reason string
	Merge  bool
}

func (*UserAdd) event()        {}
func (*UserQuit) event()       {}
func (*NickChange) event()     {}
func (*Join) event()           {}
func (*Part) event()           {}
func (*Kick) event()           {}
func (*Topic) event()          {}
func (*ChannelMode) event()    {}
func (*UserMode) event()       {}
func (*Away) event()           {}
func (*Login) event()          {}
func (*HostChange) event()     {}
func (*RealnameChange) event() {}
func (*Invite) event()         {}
func (*Reveal) event()         {}
func (*ChannelTS) event()      {}
func (*Rename) event()         {}

// Errors from Apply.
var (
	ErrNoSuchUser    = errors.New("no such user")
	ErrNoSuchChannel = errors.New("no such channel")
	ErrNotOnChannel  = errors.New("not on that channel")
	ErrNotHidden     = errors.New("already seen in that channel")
	ErrChannelInUse  = errors.New("that channel name is in use")
)

// Apply carries out ev. It is the only way the network changes.
func (n *Network) Apply(ev Event) error {
	switch e := ev.(type) {
	case *UserAdd:
		return n.addUser(e.User)
	case *UserQuit:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		n.removeUser(u)
	case *NickChange:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		old := u.Prefix()
		if err := n.changeNick(u, e.Nick, e.TS); err != nil {
			return err
		}
		e.Old = old
	case *Join:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		_, m, created := n.join(u, e.Channel, e.TS, e.Ranks, e.Burst)
		e.Created, e.Ranks = created, m.Ranks
	case *Part:
		u, ch, err := n.member(e.UID, e.Channel)
		if err != nil {
			return err
		}
		n.part(u, ch)
	case *Kick:
		u, ch, err := n.member(e.UID, e.Channel)
		if err != nil {
			return err
		}
		n.part(u, ch)
	case *ChannelTS:
		ch := n.Channel(e.Channel)
		if ch == nil {
			return ErrNoSuchChannel
		}
		ch.TS = e.TS
	case *Topic:
		ch := n.Channel(e.Channel)
		if ch == nil {
			return ErrNoSuchChannel
		}
		ch.Topic, ch.TopicBy, ch.TopicAt = e.Text, e.By, e.At
	case *ChannelMode:
		ch := n.Channel(e.Channel)
		if ch == nil {
			return ErrNoSuchChannel
		}
		e.Changes = n.channelModes(ch, e)
		n.dropIfEmpty(ch) // -P on an empty channel closes it
	case *Reveal:
		u, ch, err := n.member(e.UID, e.Channel)
		if err != nil {
			return err
		}
		m := ch.Members[u.UID]
		if !m.Hidden {
			return ErrNotHidden
		}
		m.Hidden = false
	case *UserMode:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		var done []ModeChange
		n.count(u, -1)
		for _, m := range e.Changes {
			if u.Modes[m.Mode] != m.Add {
				setFlag(u.Modes, m.Mode, m.Add)
				done = append(done, m)
			}
		}
		n.count(u, 1)
		e.Changes = done
	case *Away:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		u.Away = e.Message
	case *Login:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		u.Account = e.Account
	case *HostChange:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		u.Host = e.Host
		if e.Ident != "" && e.Ident != u.Ident {
			if u.RealIdent == "" {
				u.RealIdent = u.Ident
			}
			u.Ident = e.Ident
			if u.Ident == u.RealIdent {
				u.RealIdent = ""
			}
		}
	case *RealnameChange:
		u := n.users[e.UID]
		if u == nil {
			return ErrNoSuchUser
		}
		u.Realname = e.Realname
	case *Rename:
		return n.rename(e.Old, e.New, e.Merge)
	case *Invite:
		if n.users[e.UID] == nil {
			return ErrNoSuchUser
		}
		if ch := n.Channel(e.Channel); ch != nil {
			if e.Remove {
				delete(ch.Invited, e.UID)
			} else {
				ch.Invited[e.UID] = true
			}
		}
	default:
		return errors.New("unknown event")
	}
	return nil
}

// mergeInto moves every member of ch into dst (a rename that met a channel
// of the same name, like a netjoin): someone already in both keeps the
// ranks of both. ch's invites and the metadata keys dst lacks come along,
// and ch is gone. Modes, lists and age are the server's to settle first.
func (n *Network) mergeInto(ch, dst *Channel) {
	oldKey, newKey := irc.Fold(ch.Name), irc.Fold(dst.Name)
	for uid, m := range ch.Members {
		if o := dst.Members[uid]; o != nil {
			o.Ranks |= m.Ranks
			o.Hidden = o.Hidden && m.Hidden
		} else {
			dst.Members[uid] = m
			m.User.Channels[newKey] = dst
			delete(dst.Invited, uid)
		}
		delete(m.User.Channels, oldKey)
	}
	for uid := range ch.Invited {
		if dst.Members[uid] == nil {
			dst.Invited[uid] = true
		}
	}
	for k, v := range ch.Metadata {
		if _, ok := dst.Metadata[k]; !ok {
			if dst.Metadata == nil {
				dst.Metadata = make(map[string]string)
			}
			dst.Metadata[k] = v
		}
	}
	delete(n.channels, oldKey)
}

// rename moves channel old to the name new: the same *Channel, under its new
// key in the network and in each member's list. A change of case only is
// allowed; a different channel already holding new is not, unless merge is
// set: then old's members move into that channel and old is gone.
func (n *Network) rename(old, new string, merge bool) error {
	ch := n.Channel(old)
	if ch == nil {
		return ErrNoSuchChannel
	}
	oldKey, newKey := irc.Fold(ch.Name), irc.Fold(new)
	if other := n.channels[newKey]; other != nil && other != ch {
		if !merge {
			return ErrChannelInUse
		}
		n.mergeInto(ch, other)
		return nil
	}
	delete(n.channels, oldKey)
	n.channels[newKey] = ch
	ch.Name = new
	for _, m := range ch.Members {
		delete(m.User.Channels, oldKey)
		m.User.Channels[newKey] = ch
	}
	return nil
}

func (n *Network) member(uid, channel string) (*User, *Channel, error) {
	u := n.users[uid]
	if u == nil {
		return nil, nil, ErrNoSuchUser
	}
	ch := n.Channel(channel)
	if ch == nil {
		return nil, nil, ErrNoSuchChannel
	}
	if ch.Members[uid] == nil {
		return nil, nil, ErrNotOnChannel
	}
	return u, ch, nil
}

// channelModes applies e's changes to ch and returns those that changed
// something, with arguments as stored.
func (n *Network) channelModes(ch *Channel, e *ChannelMode) []ModeChange {
	var done []ModeChange
	for _, m := range e.Changes {
		switch {
		case m.Mode == 'b' || m.Mode == 'e' || m.Mode == 'I':
			if m.Add {
				if !ch.addListEntry(m.Mode, m.Arg, e.By, e.At) {
					continue
				}
			} else {
				stored, ok := ch.removeListEntry(m.Mode, m.Arg)
				if !ok {
					continue
				}
				m.Arg = stored
			}
		case RankForMode(m.Mode) != 0:
			mem := ch.Members[m.UID]
			if mem == nil || !mem.set(RankForMode(m.Mode), m.Add) {
				continue
			}
			m.Arg = mem.User.Nick
		case m.Mode == 'k':
			if m.Add {
				if m.Arg == "" || ch.Modes['k'] && ch.Key == m.Arg {
					continue
				}
				ch.Key, ch.Modes['k'] = m.Arg, true
			} else {
				if !ch.Modes['k'] {
					continue
				}
				m.Arg = ch.Key
				ch.Key = ""
				delete(ch.Modes, 'k')
			}
		case m.Mode == 'l':
			if m.Add {
				l, err := strconv.Atoi(m.Arg)
				if err != nil || l <= 0 || ch.Modes['l'] && ch.Limit == l {
					continue
				}
				ch.Limit, ch.Modes['l'] = l, true
				m.Arg = strconv.Itoa(l)
			} else {
				if !ch.Modes['l'] {
					continue
				}
				ch.Limit = 0
				delete(ch.Modes, 'l')
				m.Arg = ""
			}
		case IsParamMode(m.Mode):
			if m.Add {
				if m.Arg == "" || ch.Modes[m.Mode] && ch.Params[m.Mode] == m.Arg {
					continue
				}
				if ch.Params == nil {
					ch.Params = make(map[byte]string)
				}
				ch.Params[m.Mode], ch.Modes[m.Mode] = m.Arg, true
			} else {
				if !ch.Modes[m.Mode] {
					continue
				}
				delete(ch.Params, m.Mode)
				delete(ch.Modes, m.Mode)
				m.Arg = ""
			}
		default:
			if ch.Modes[m.Mode] == m.Add {
				continue
			}
			setFlag(ch.Modes, m.Mode, m.Add)
		}
		done = append(done, m)
	}
	return done
}

func setFlag(m map[byte]bool, f byte, on bool) {
	if on {
		m[f] = true
	} else {
		delete(m, f)
	}
}

// Equal reports whether two networks hold the same users and channels, and
// if not, where they first differ. Tests use it to check that a copy fed
// only with events matches the real one.
func Equal(a, b *Network) (bool, string) {
	if len(a.users) != len(b.users) {
		return false, "number of users"
	}
	if a.people != b.people || a.invisible != b.invisible || a.opers != b.opers {
		return false, "LUSERS counts"
	}
	for uid, u := range a.users {
		v := b.users[uid]
		if v == nil {
			return false, "user " + u.Nick + " missing"
		}
		if u.Nick != v.Nick || u.Ident != v.Ident || u.RealIdent != v.RealIdent || u.Host != v.Host || u.RealHost != v.RealHost || u.IP != v.IP ||
			u.Realname != v.Realname || u.Account != v.Account || u.Away != v.Away || u.NickTS != v.NickTS ||
			u.Service != v.Service || u.ModeString() != v.ModeString() || len(u.Channels) != len(v.Channels) {
			return false, "user " + u.Nick
		}
	}
	if len(a.nicks) != len(b.nicks) {
		return false, "nick index"
	}
	if len(a.channels) != len(b.channels) {
		return false, "number of channels"
	}
	for key, c := range a.channels {
		d := b.channels[key]
		if d == nil {
			return false, "channel " + c.Name + " missing"
		}
		cm, cp := c.ModeString(true)
		dm, dp := d.ModeString(true)
		if c.Name != d.Name || c.TS != d.TS || cm != dm || strings.Join(cp, " ") != strings.Join(dp, " ") ||
			c.Topic != d.Topic || c.TopicBy != d.TopicBy || !c.TopicAt.Equal(d.TopicAt) {
			return false, "channel " + c.Name
		}
		if len(c.Members) != len(d.Members) {
			return false, "members of " + c.Name
		}
		for uid, m := range c.Members {
			if o := d.Members[uid]; o == nil || o.Ranks != m.Ranks {
				return false, "member " + m.User.Nick + " of " + c.Name
			}
		}
		for _, l := range []byte("beI") {
			if len(c.Lists[l]) != len(d.Lists[l]) {
				return false, "list +" + string(l) + " of " + c.Name
			}
			for i := range c.Lists[l] {
				x, y := c.Lists[l][i], d.Lists[l][i]
				if x.Mask != y.Mask || x.SetBy != y.SetBy || !x.SetAt.Equal(y.SetAt) {
					return false, "list +" + string(l) + " of " + c.Name
				}
			}
		}
		if len(c.Invited) != len(d.Invited) {
			return false, "invites of " + c.Name
		}
	}
	return true, ""
}

// Copy returns u without its channels, for another network.
func (u *User) Copy() *User {
	cp := *u
	cp.Modes = make(map[byte]bool, len(u.Modes))
	for k, v := range u.Modes {
		cp.Modes[k] = v
	}
	cp.Channels = make(map[string]*Channel)
	cp.Metadata = maps.Clone(u.Metadata)
	return &cp
}

// Clone returns a deep copy of the network. A link burst and the
// tests that check events against the real network start from one.
func (n *Network) Clone() *Network {
	c := NewNetwork(n.SID)
	c.next = n.next
	// Like +nt, the modes new channels start with are a network rule.
	if n.NewChannelParams != nil {
		c.NewChannelParams = make(map[byte]string, len(n.NewChannelParams))
		for k, v := range n.NewChannelParams {
			c.NewChannelParams[k] = v
		}
	}
	for uid, u := range n.users {
		cp := u.Copy()
		c.users[uid] = cp
		c.nicks[irc.Fold(cp.Nick)] = cp
		c.count(cp, 1)
	}
	for key, ch := range n.channels {
		cc := &Channel{Name: ch.Name, TS: ch.TS, Key: ch.Key, Limit: ch.Limit, Topic: ch.Topic, TopicBy: ch.TopicBy, TopicAt: ch.TopicAt,
			Modes: make(map[byte]bool), Members: make(map[string]*Member), Lists: make(map[byte][]ListEntry), Invited: make(map[string]bool), net: c}
		for k, v := range ch.Modes {
			cc.Modes[k] = v
		}
		for k, v := range ch.Lists {
			cc.Lists[k] = append([]ListEntry(nil), v...)
		}
		if ch.Params != nil {
			cc.Params = make(map[byte]string, len(ch.Params))
			for k, v := range ch.Params {
				cc.Params[k] = v
			}
		}
		for k, v := range ch.Invited {
			cc.Invited[k] = v
		}
		cc.Metadata = maps.Clone(ch.Metadata)
		for uid, m := range ch.Members {
			u := c.users[uid]
			cc.Members[uid] = &Member{User: u, Ranks: m.Ranks, Hidden: m.Hidden, Joined: m.Joined}
			u.Channels[key] = cc
		}
		c.channels[key] = cc
	}
	return c
}

// CopyEvent returns a copy of ev that shares nothing with it, so applying
// it to another network leaves ev alone.
func CopyEvent(ev Event) Event {
	switch e := ev.(type) {
	case *UserAdd:
		return &UserAdd{User: e.User.Copy()}
	case *UserQuit:
		cp := *e
		return &cp
	case *NickChange:
		cp := *e
		return &cp
	case *Join:
		cp := *e
		return &cp
	case *Part:
		cp := *e
		return &cp
	case *Kick:
		cp := *e
		return &cp
	case *Topic:
		cp := *e
		return &cp
	case *ChannelMode:
		cp := *e
		cp.Changes = append([]ModeChange(nil), e.Changes...)
		return &cp
	case *UserMode:
		cp := *e
		cp.Changes = append([]ModeChange(nil), e.Changes...)
		return &cp
	case *Away:
		cp := *e
		return &cp
	case *Login:
		cp := *e
		return &cp
	case *HostChange:
		cp := *e
		return &cp
	case *RealnameChange:
		cp := *e
		return &cp
	case *Invite:
		cp := *e
		return &cp
	case *Reveal:
		cp := *e
		return &cp
	case *ChannelTS:
		cp := *e
		return &cp
	case *Rename:
		cp := *e
		return &cp
	}
	return ev
}
