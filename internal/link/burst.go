package link

import "github.com/fhazal/beeircd/internal/irc"

// The network as servers exchange it, independent of any one protocol.
// A Codec turns these into wire lines and back.

// User is one user on the network.
type User struct {
	UID, Nick string
	NickTS    int64
	Hops      int // 1 = on the server that sends it
	Ident     string
	RealHost  string
	Host      string // the host others see (cloak or vhost)
	IP        string // "" when not known
	Account   string // "" = not identified
	Modes     string // user mode letters, no '+'
	Realname  string
}

// Member is a user in a channel with its rank mode letters ("ov").
type Member struct {
	UID   string
	Ranks string
}

// ListEntry is one ban (b), exception (e) or invite exception (I).
type ListEntry struct {
	Mode  byte
	Mask  string
	SetBy string
	SetAt int64
}

// Channel is a channel's state in a burst. Modes are the letters of the
// non-list, non-rank modes; Args the arguments of those that have one.
type Channel struct {
	Name    string
	TS      int64
	Modes   string
	Args    map[byte]string
	Members []Member
	Lists   []ListEntry
}

// Topic is a channel's topic.
type Topic struct {
	Channel, Text, By string
	At                int64
}

// Away sets (or clears, "") a user's away message.
type Away struct{ UID, Message string }

// Kill removes a user; Source is the server or user doing it.
type Kill struct{ Source, UID, Reason string }

// Codec writes and reads the network for one protocol, after the
// handshake.
type Codec interface {
	// User introduces u (on server sid).
	User(u User, sid string) []string
	// Away is u's away message right after it was introduced.
	Away(uid, message string) string
	// Channel sends a channel's state from server sid.
	Channel(c Channel, sid string) []string
	// Topic sends a channel's topic from server sid.
	Topic(t Topic, sid string) string
	// Kill removes a user, from server sid.
	Kill(sid, uid, reason string) string
	// Decode reads one line: *User, *Channel, *Topic, *Away, *Kill, or
	// nil when it isn't one of those. The sender's SID or UID is in
	// m.Source.
	Decode(m irc.Message) any
	// Rename reports whether nick collisions rename the loser to its UID
	// (both sides BeeIRCd) instead of killing it (UnrealIRCd's rule).
	Rename() bool
}
