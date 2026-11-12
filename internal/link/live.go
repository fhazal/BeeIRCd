package link

// Live changes after the burst (Stage 2c), independent of any protocol.
// Source is the UID or SID doing it, the way the wire names it.

// Nick renames a user.
type Nick struct {
	UID, Nick string
	TS        int64
}

// Quit takes a user off the network.
type Quit struct{ UID, Reason string }

// Part takes a user out of a channel.
type Part struct{ UID, Channel, Reason string }

// Kick takes UID (or a nick, from older servers) out of a channel.
type Kick struct{ Source, Channel, UID, Reason string }

// ModeItem is one channel mode letter going on or off, with its argument
// ("" for none). Rank arguments are UIDs going out; coming in they may be
// nicks.
type ModeItem struct {
	Add  bool
	Mode byte
	Arg  string
}

// Mode changes a channel's modes.
type Mode struct {
	Source, Channel string
	Items           []ModeItem
}

// UMode changes a user's modes ("+x-i"). Source is the user itself, or a
// server or service doing it to them.
type UMode struct{ Source, UID, Modes string }

// Login sets the account UID is identified to; "" logs out.
type Login struct{ Source, UID, Account string }

// Host changes the host others see.
type Host struct{ Source, UID, Host string }

// Ident changes the ident others see.
type Ident struct{ Source, UID, Ident string }

// Realname changes the real name.
type Realname struct{ Source, UID, Realname string }

// Invite lets UID into Channel.
type Invite struct{ Source, UID, Channel string }

// ForceNick is a services nick change (SVSNICK).
type ForceNick struct {
	Source, UID, Nick string
	TS                int64
}

// Message is PRIVMSG, NOTICE or TAGMSG to a UID, nick or channel (with a
// status prefix like "@#chan"). Tags are the message tags to carry.
type Message struct {
	Source, Verb, Target, Text string
	Tags                       map[string]string
}

// Numeric is a numeric reply for a user on another server: Params start
// with the target (a UID).
type Numeric struct {
	Source, Code string
	Params       []string
}

// Ban kinds that travel between servers (UnrealIRCd TKL letters).
const (
	BanGline     = 'G' // user@host
	BanGZline    = 'Z' // IP or CIDR
	BanShun      = 's' // user@host
	BanException = 'E' // user@host, exempt from the others
)

// Ban is a network ban going on (Add) or off.
type Ban struct {
	Source      string
	Add         bool
	Type        byte // BanGline ...
	User, Host  string
	SetBy       string
	Expires     int64 // unix, 0 = until removed
	SetAt       int64
	Reason      string
	Exempt      string // BanException: which kinds it exempts from ("GZs")
	FromBurst   bool   // part of a burst (nothing to tell opers)
	Unsupported bool   // a kind we don't keep (spamfilter, nick ban ...)
}
