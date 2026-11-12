package link

import (
	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

// Local is what a handshake says about this server.
type Local struct {
	Name, SID, Description, Network string
	Version                         string // like "1.1.0"
	ChanModes                       string // ISUPPORT CHANMODES value
	UserModes                       string // every user mode letter
	Prefix                          string // ISUPPORT PREFIX value, like "(ov)@+"
	CloakHash                       string // short hash of the cloak secret (NETINFO)
	Casemapping                     string // "ascii" or "rfc1459" (2c)
	Services                        bool   // this server runs the built-in services (2c)
	Booted                          int64  // start time, unix
	MaxGlobal                       int    // most users seen (NETINFO)
	Now                             func() int64
	// Known reports whether a SID or server name is on the network
	// already (this server included), for loop and duplicate checks.
	Known func(sidOrName string) bool
	// Block finds the [[link.server]] block for a server name.
	Block func(name string) *config.LinkServer
}

// Peer is what a handshake learned about the other server.
type Peer struct {
	Name, SID, Description string
	Software               string // like "UnrealIRCd-6.2.7" or "BeeIRCd-1.1.0"
	Protocol               int
	Bee                    bool              // the other side is BeeIRCd (sent BEE=)
	Tokens                 map[string]string // every PROTOCTL token, for later steps
	Block                  config.LinkServer // the link block that matched
}

// Handshake drives one link from the TCP+TLS connection until both sides
// are servers. A protocol (UnrealIRCd 6 now, InspIRCd later) implements
// it.
type Handshake interface {
	// Start returns the lines to send right away.
	Start() []string
	// Handle takes one line from the other side and returns lines to send.
	// A *Refusal error ends the link.
	Handle(m irc.Message) ([]string, error)
	// Done reports whether the link is up; Peer is complete then.
	Done() bool
	Peer() Peer
	// Burst returns our side of the sync once Done: the servers we know
	// (in the order to introduce them), then body (users and channels,
	// written with Codec), then the end-of-sync marker.
	Burst(servers []ServerInfo, body []string) []string
	// Codec reads and writes the network once Done.
	Codec() Codec
}

// ServerInfo is one server on the network, for bursts.
type ServerInfo struct {
	Name, SID, Description string
	Uplink                 string // SID of the server it hangs off
	Hops                   int    // from us
	Synced                 bool
}

// Refusal ends a link: Reason goes to the other side in ERROR and to the
// opers.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(reason string) error { return &Refusal{Reason: reason} }
