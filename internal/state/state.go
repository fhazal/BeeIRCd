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
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// ErrNickInUse means someone else has that nick.
var ErrNickInUse = errors.New("nickname is already in use")

// User is one person (or, later, a service bot) on the network.
type User struct {
	UID      string
	Nick     string
	Ident    string // the ident others see (a HostServ vident can change it)
	Host     string // the host others see
	RealHost string
	// RealIdent is the ident the user connected with, when a vident
	// shows another one ("" = same as Ident). Network bans match it.
	RealIdent string
	IP        string
	Realname  string
	Server    string
	Account   string // services account, "" when not identified
	Away      string
	TLS       bool
	CertFP    string // SHA-256 of the TLS client certificate, lower case hex ("" = none)
	Service   bool   // NickServ and the other built-in services
	// Cloak is the host +x shows, for users on other servers: their
	// server made it (2c). Ours are made on the spot (cloakFor).
	Cloak    string
	SignOn   time.Time
	NickTS   int64
	Modes    map[byte]bool
	Channels map[string]*Channel // by folded name
}

// TrueIdent is the ident the user connected with.
func (u *User) TrueIdent() string {
	if u.RealIdent != "" {
		return u.RealIdent
	}
	return u.Ident
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
	real := u.Nick + "!" + u.TrueIdent() + "@" // a vident: the real host goes with the real ident
	if real != base {
		out = append(out, real+u.Host)
	}
	if u.RealHost != "" && u.RealHost != u.Host {
		out = append(out, real+u.RealHost)
	}
	if u.IP != "" && u.IP != u.Host && u.IP != u.RealHost {
		out = append(out, real+u.IP)
	}
	return out
}

// ListEntry is one ban, exception or invite exception.
type ListEntry struct {
	Mask  string
	SetBy string
	SetAt time.Time
}

// Rank is a member's status in a channel. A member can hold several ranks;
// higher ranks have higher values, so they compare with < and >.
type Rank uint8

// The ranks, lowest first. A server with only @ and + uses Op and Voice.
const (
	RankVoice  Rank = 1 << iota // +v  +
	RankHalfop                  // +h  %
	RankOp                      // +o  @
	RankAdmin                   // +a  &  (protected)
	RankOwner                   // +q  ~
)

// RankInfo ties a rank to its mode letter and NAMES prefix.
type RankInfo struct {
	Rank   Rank
	Mode   byte
	Prefix byte
}

// Ranks lists every rank, highest first.
var Ranks = []RankInfo{
	{RankOwner, 'q', '~'},
	{RankAdmin, 'a', '&'},
	{RankOp, 'o', '@'},
	{RankHalfop, 'h', '%'},
	{RankVoice, 'v', '+'},
}

// RankForMode returns the rank behind a mode letter like 'o', or 0.
func RankForMode(m byte) Rank {
	for _, r := range Ranks {
		if r.Mode == m {
			return r.Rank
		}
	}
	return 0
}

// RankForPrefix returns the rank behind a prefix like '@', or 0.
func RankForPrefix(p byte) Rank {
	for _, r := range Ranks {
		if r.Prefix == p {
			return r.Rank
		}
	}
	return 0
}

// Member is a user's place in a channel.
type Member struct {
	User  *User
	Ranks Rank
	// Hidden is a +D (delay join) member nobody else sees yet: they show
	// up when they first talk, get a status or the channel loses +D.
	Hidden bool
}

// Has reports whether the member holds rank r.
func (m *Member) Has(r Rank) bool { return m.Ranks&r != 0 }

// set gives or takes rank r and reports whether anything changed.
func (m *Member) set(r Rank, on bool) bool {
	if m.Has(r) == on {
		return false
	}
	if on {
		m.Ranks |= r
	} else {
		m.Ranks &^= r
	}
	return true
}

// Top is the member's highest rank, 0 for none.
func (m *Member) Top() Rank {
	for _, r := range Ranks {
		if m.Has(r.Rank) {
			return r.Rank
		}
	}
	return 0
}

// AtLeast reports whether the member's highest rank is r or above.
func (m *Member) AtLeast(r Rank) bool { return m.Top() >= r }

// Prefix is the highest prefix, like "@", or "".
func (m *Member) Prefix() string {
	for _, r := range Ranks {
		if m.Has(r.Rank) {
			return string(r.Prefix)
		}
	}
	return ""
}

// AllPrefixes is every prefix the member has, highest first ("@+").
func (m *Member) AllPrefixes() string {
	var b []byte
	for _, r := range Ranks {
		if m.Has(r.Rank) {
			b = append(b, r.Prefix)
		}
	}
	return string(b)
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
	// Params holds the argument of the other parameter modes (f F H L E B J).
	Params map[byte]string
}

// ParamModes are the channel modes that take an argument when set and none
// when unset (besides k and l).
const ParamModes = "BEFHJLf"

// IsParamMode reports whether m is one of ParamModes.
func IsParamMode(m byte) bool { return strings.IndexByte(ParamModes, m) >= 0 }

// Param returns the argument of parameter mode m, or "".
func (c *Channel) Param(m byte) string { return c.Params[m] }

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
		default:
			if IsParamMode(m) {
				params = append(params, c.Params[m])
			}
		}
	}
	return string(b), params
}

// Matches reports whether any entry of list matches u.
// Mute entries (~m, ~q) don't count here.
func (c *Channel) Matches(list byte, u *User) bool {
	for _, e := range c.Lists[list] {
		if m, mute := MatchBan(e.Mask, u); m && !mute {
			return true
		}
	}
	return false
}

// IsBanned reports whether u is banned (from joining) and not excepted.
func (c *Channel) IsBanned(u *User) bool { return c.Matches('b', u) && !c.Matches('e', u) }

// IsMuted reports whether a ban or a mute (~m) stops u talking, and no
// exception lets them. A mute exception (+e ~m:mask) lifts mutes but not
// bans, like UnrealIRCd and InspIRCd.
func (c *Channel) IsMuted(u *User) bool {
	for _, e := range c.Lists['b'] {
		if m, _ := MatchBan(e.Mask, u); m {
			return !c.matchesAny('e', u)
		}
	}
	return false
}

// matchesAny is Matches that also counts mute entries.
func (c *Channel) matchesAny(list byte, u *User) bool {
	for _, e := range c.Lists[list] {
		if m, _ := MatchBan(e.Mask, u); m {
			return true
		}
	}
	return false
}

// addListEntry adds mask to list. It returns false when the mask is already
// there.
func (c *Channel) addListEntry(list byte, mask, by string, at time.Time) bool {
	for _, e := range c.Lists[list] {
		if irc.EqualFold(e.Mask, mask) {
			return false
		}
	}
	c.Lists[list] = append(c.Lists[list], ListEntry{Mask: mask, SetBy: by, SetAt: at})
	return true
}

// removeListEntry removes mask from list and returns the stored form.
func (c *Channel) removeListEntry(list byte, mask string) (string, bool) {
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
	SID string
	// NewChannelParams are parameter modes every new channel starts with,
	// like UnrealIRCd's modes-on-join (+H from [history] default).
	NewChannelParams map[byte]string
	users            map[string]*User
	nicks            map[string]*User
	channels         map[string]*Channel
	next             uint64

	// Running counts for LUSERS (people = users who aren't services).
	people, invisible, opers int
}

// count adds d (1 or -1) for u to the running counts.
func (n *Network) count(u *User, d int) {
	if u.Service {
		return
	}
	n.people += d
	if u.Modes['i'] {
		n.invisible += d
	}
	if u.Modes['o'] {
		n.opers += d
	}
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

// addUser adds a registered user.
func (n *Network) addUser(u *User) error {
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
	n.count(u, 1)
	return nil
}

// removeUser takes u out of every channel and off the network.
func (n *Network) removeUser(u *User) {
	for _, ch := range u.Channels {
		n.part(u, ch)
	}
	n.count(u, -1)
	delete(n.users, u.UID)
	if n.nicks[irc.Fold(u.Nick)] == u {
		delete(n.nicks, irc.Fold(u.Nick))
	}
}

// changeNick renames u. Changing only the case of one's own nick is allowed.
func (n *Network) changeNick(u *User, nick string, ts int64) error {
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

// PeopleCounts counts users who aren't services, and how many of them
// are invisible (+i) and opers (+o), without building a list (LUSERS runs
// at every connect).
func (n *Network) PeopleCounts() (total, invisible, opers int) {
	return n.people, n.invisible, n.opers
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

// join puts u in channel name, creating it (+nt) when it doesn't exist.
// u gets ranks, or op when ranks is 0 and the channel is new. created
// reports whether it was new. A burst join (from a server link) creates
// the channel bare and gives exactly ranks.
func (n *Network) join(u *User, name string, ts int64, ranks Rank, burst bool) (ch *Channel, m *Member, created bool) {
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
		if burst {
			ch.Modes = map[byte]bool{}
		}
		for m, v := range n.NewChannelParams {
			if burst {
				break
			}
			if ch.Params == nil {
				ch.Params = make(map[byte]string)
			}
			ch.Modes[m], ch.Params[m] = true, v
		}
		n.channels[key] = ch
		created = true
	}
	if m = ch.Members[u.UID]; m != nil {
		return ch, m, false
	}
	m = &Member{User: u, Ranks: ranks}
	if created && ranks == 0 && !burst {
		m.Ranks = RankOp
	}
	if !created && ranks == 0 && ch.Modes['D'] && !burst {
		m.Hidden = true
	}
	ch.Members[u.UID] = m
	u.Channels[key] = ch
	delete(ch.Invited, u.UID)
	return ch, m, created
}

// part takes u out of ch. An empty channel is removed.
func (n *Network) part(u *User, ch *Channel) {
	delete(ch.Members, u.UID)
	delete(u.Channels, irc.Fold(ch.Name))
	n.dropIfEmpty(ch)
}

// dropIfEmpty removes ch when nobody is in it and it isn't +P (permanent).
func (n *Network) dropIfEmpty(ch *Channel) {
	if len(ch.Members) == 0 && !ch.Modes['P'] {
		delete(n.channels, irc.Fold(ch.Name))
	}
}

// Neighbours returns everyone who shares a channel with u, not u itself.
func (n *Network) Neighbours(u *User) []*User {
	seen := map[string]bool{u.UID: true}
	var out []*User
	for _, ch := range u.Channels {
		if me := ch.Members[u.UID]; me != nil && me.Hidden {
			continue // nobody there knows u is in
		}
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
