// Package gate is BeeIRCd's door keeper. On Linux `beeircd run` starts as a
// small, long-lived gate process that owns the client ports and runs the
// real server as a child process (the same program, marked by the
// BEEIRCD_GATE environment variable). The gate accepts each client, does
// the TLS, WebSocket and PROXY steps (the Front, which lives in
// internal/server), then hands the plain IRC byte stream to the child with
// what it learned about the client (Info). Links, the control socket and
// the panel stay in the child.
//
// Two SOCK_SEQPACKET socket pairs join gate and child, inherited as fd 3
// and fd 4:
//
//   - fd 3, gate to child: one message per client, Info as JSON plus one
//     file descriptor (SCM_RIGHTS). A plain port sends the client's own TCP
//     socket, so nothing sits between client and server. TLS, WebSocket and
//     PROXY ports send one end of a fresh socket pair; the gate copies bytes
//     between the client and the other end (pump).
//   - fd 4, child to gate: Sync requests (Spec, the whole client-facing
//     setup) and their answers (Result). Separate from fd 3, so a Sync from
//     the server's loop never waits behind a client.
//
// Life cycle: the child exits with ExitRestart to be started again (clients
// drop), with ExitRegate when this gate is from another version (the gate
// then executes itself again), and with any other code to make the gate
// exit with it. SIGTERM and SIGINT go on to the child; the gate keeps
// copying until the child has said goodbye to everyone, then exits. SIGHUP
// goes on to the child (rehash).
//
// Hand-over (SIGUSR2 to the gate, or Child.RequestHandover): the gate
// replaces the child with a new one while the clients stay connected.
//
//  1. The gate stops giving the child clients (new ones wait in the gate)
//     and sends it "hand" on fd 3.
//  2. The child stops reading its clients, writes a snapshot of everything
//     it knows and sends the gate each client's socket ("fd" on fd 4) and
//     the snapshot in an unnamed file ("snap"), then exits (ExitHandover).
//  3. Only when that child is gone the gate starts the next one, with the
//     snapshot on fd 5, and sends it the sockets ("old" on fd 3) before
//     any new client. The gate then closes its own copies.
//  4. The new child loads the snapshot, takes the sockets and says how it
//     went ("resumed").
//
// It is never worse than a restart: a child that doesn't hand over in time
// is killed and the next one starts empty; a new child that can't use the
// snapshot drops those clients and carries on empty; one that dies while
// taking over is started once more, empty. Clients the gate still holds
// when a hand-over fails are closed with a goodbye line.
//
// A bug here hits every user, so this package stays small and plain.
package gate

//lint:file-ignore U1000 on other systems these helpers are used only by gate_linux.go

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

const (
	// Proto is the gate/child protocol version. A child that finds another
	// one exits with ExitRegate, so an upgraded binary never talks to an
	// old gate.
	Proto = "2"
	// ExitRestart: the child asks to be started again (an admin's RESTART).
	ExitRestart = 120
	// ExitRegate: the child is newer than the gate; the gate re-executes.
	ExitRegate = 121
	// ExitHandover: the child handed its clients to the gate and stopped,
	// as the gate asked; the next child takes them over.
	ExitHandover = 122

	envKey = "BEEIRCD_GATE"
)

// ErrRegate is what ChildFromEnv returns when the gate speaks another
// protocol version.
var ErrRegate = errors.New("the gate in front of this server is from another BeeIRCd version; starting it again")

// ErrHandover is what Child.Accept returns when the gate asks the server
// to hand its clients over to the next server process.
var ErrHandover = errors.New("the gate asks for a hand-over")

// Handover is what a server that takes over gets from the gate: the last
// server's snapshot and its clients' connections, by the names the
// snapshot uses. Err is set when it didn't arrive whole (no clients then).
type Handover struct {
	Snapshot []byte
	Err      error
	conns    map[string]int // file descriptors
}

// ErrReset is what a Conn's Read returns when the gate dropped the client
// after a read error, so the server says "Read error" as it did when it
// read from the client itself. A clean close by the client stays io.EOF.
var ErrReset = errors.New("connection reset")

// Info is what the gate knows about one client.
type Info struct {
	Remote    string `json:"r"`           // the client, after PROXY or X-Forwarded-For
	Local     string `json:"l"`           // our address it connected to
	TLS       bool   `json:"t,omitempty"` // TLS here, or at the proxy in front of a WebSocket port
	Cipher    string `json:"c,omitempty"`
	CertFP    string `json:"f,omitempty"` // SHA-256 of the client certificate, hex
	WebSocket bool   `json:"w,omitempty"`
	Pipe      bool   `json:"p,omitempty"` // set by the gate: the fd is a pipe, not the client's socket
}

// TLSFiles is the certificate the client ports use, as the server loads it.
type TLSFiles struct {
	Cert, Key  string
	SelfSigned bool // make a self-signed pair when the files are missing
	Host       string
}

// Spec is the whole client-facing setup. Each Sync replaces the last one:
// ports in it are opened or kept, ports not in it are closed.
type Spec struct {
	Ports   []config.Listen
	TLS     *TLSFiles     // nil: no TLS port
	CertGen int           // the server bumps it after reloading the certificate
	Wait    time.Duration // how long a TLS handshake may take
	// Deny: clients the server refuses anyway (a ZLINE, the connect
	// throttle). The gate closes them before any TLS or WebSocket work and
	// tells the server (Child.Accept returns a *Refused). An older gate
	// ignores it; a Spec without it (an older server) clears the list.
	Deny []Deny `json:",omitempty"`
}

// Deny is one entry of Spec.Deny.
type Deny struct {
	Net   string `json:"n"`           // an IP prefix ("192.0.2.7/32", "2001:db8::/32")
	Until int64  `json:"u,omitempty"` // Unix milliseconds it ends at; 0: at the next Sync
}

// Refused is the error Child.Accept returns for a client the gate closed
// itself (Spec.Deny): there is no connection, only who it was, so the
// server can count and log it the way it does a client it refuses.
type Refused struct{ Info Info }

func (r *Refused) Error() string { return "gate: refused " + r.Info.Remote }

// Result answers a Sync: per port in Spec order, the bound address or the
// error text (the same text net.Listen gives).
type Result struct {
	Addrs  []string
	Errors []string
	Err    string // the certificate didn't load (ports untouched)
}

// Deliver hands one ready client to the child. False: it couldn't (no
// child, or the child went away); the caller closes conn. A nil conn only
// tells the child about a client the gate refused (Spec.Deny); that never
// waits for a hand-over.
type Deliver func(conn net.Conn, info Info) bool

// Front owns the client ports inside the gate (internal/server.Front).
type Front interface {
	Apply(Spec) Result
	Close() // stop accepting; connected clients stay
}

// Conn is a client that reaches the child through a pipe from the gate (a
// TLS, WebSocket or PROXY port). Its addresses are the client's, not the
// pipe's.
type Conn struct {
	*net.UnixConn
	Info          Info
	remote, local net.Addr
}

func newConn(u *net.UnixConn, info Info) *Conn {
	return &Conn{UnixConn: u, Info: info, remote: tcpAddr(info.Remote), local: tcpAddr(info.Local)}
}

func (c *Conn) RemoteAddr() net.Addr { return c.remote }
func (c *Conn) LocalAddr() net.Addr  { return c.local }

// Read reads the client's bytes. At the end it tells a clean close
// (io.EOF: the gate shut only its writing side) from a dropped connection
// (ErrReset: the gate closed the pipe completely).
func (c *Conn) Read(b []byte) (int, error) {
	n, err := c.UnixConn.Read(b)
	if err == io.EOF && peerClosed(c.UnixConn) {
		err = ErrReset
	}
	return n, err
}

// tcpAddr turns "ip:port" back into a *net.TCPAddr, as the server expects
// from a client socket. Anything else keeps its text.
func tcpAddr(s string) net.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return net.TCPAddrFromAddrPort(ap)
	}
	return textAddr(s)
}

type textAddr string

func (a textAddr) Network() string { return "tcp" }
func (a textAddr) String() string  { return string(a) }
