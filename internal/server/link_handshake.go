package server

// Server links - transport and handshake.
//
// A link is a TLS connection on the [link] port (incoming) or to a
// [[link.server]] host (outgoing). Both sides show a certificate; the
// other side's public key fingerprint must match its link block. The
// handshake is UnrealIRCd 6's (internal/link); once it is done we keep the
// server tree (SID, SQUIT, EOS), answer PING and measure lag. Users and
// channels are handled in link_burst.go and link_propagate.go; a command
// no part knows is ignored (logged once per command).
//
// Like clients, each link has a reader goroutine that hands lines to the
// loop and a writer goroutine that drains its send queue; the loop owns
// everything else.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fhazal/beeircd/internal/cloak"
	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
	"github.com/fhazal/beeircd/internal/version"
)

// Link timings. UnrealIRCd's servers class usually pings every 60-90 s.
const (
	linkHandshakeTimeout  = 30 * time.Second
	linkPingIdle          = 60 * time.Second // PING after this long without a line
	linkPingTimeout       = 90 * time.Second // drop when the PONG takes longer
	linkPingWarn          = 30 * time.Second // tell opers the link is lagging
	linkDialTimeout       = 15 * time.Second
	linkRetryMin          = 30 * time.Second // after a failed connect, doubling
	linkRetryMax          = 5 * time.Minute
	linkMaxPending        = 32               // TLS handshakes in progress on the link port
	linkPortNoticeEvery   = 10 * time.Second // one failed-connection notice per address
	linkHandshakeTimedOut = "handshake timed out"
	linkMaxLine           = 64 << 10 // BIGLINES lines plus tags fit easily
	// linkMaxPreLines: lines a server may send before its handshake is
	// done (PASS, a few PROTOCTL, SERVER: about 10 in real use).
	linkMaxPreLines = 64
)

// A network with its own services drops again at once: try every 10
// minutes and tell opers once an hour.
const (
	linkServicesRetry       = 10 * time.Minute
	linkServicesNoticeEvery = time.Hour
	linkDeniedNoticeEvery   = 10 * time.Minute // the same refusal of one incoming server
)

// linkRetryFirst is how soon we reconnect after a lost link (a variable
// for tests).
var linkRetryFirst = 10 * time.Second

// linkState is the loop's view of server links.
type linkState struct {
	cert    tls.Certificate
	fp      string // our link certificate's fingerprint
	ln      net.Listener
	conns   map[*linkConn]bool
	servers map[string]*remoteServer // by SID: every server behind a link
	// farAccept is who people on other servers let through their +g
	// (acceptor UID -> accepted UIDs); farTold when a sender last heard
	// about it (caller ID across links, usertools.go).
	farAccept map[string]map[string]bool
	farTold   map[string]time.Time
	retry     map[string]*linkRetry // by lower-case block name
	over      []*linkConn           // send queue overflowed, drop after this job
	debug     bool                  // BEEIRCD_LINK_DEBUG=1: log every link line
	// saslServer takes SASL for our clients when services are off here
	// (it sent MD saslmechlist); sasl are logins our services run for
	// clients on other servers, by UID.
	saslServer, saslMechs string
	sasl                  map[string]*saslSession
	// qlines are nick bans from other servers; qlineSent our own as last
	// sent, by folded mask.
	qlines    map[string]qline
	qlineSent map[string]*link.Ban
	// exceptsSent sums up the session exceptions we last sent (link_excepts.go);
	// exceptsFrom is the SID of the server whose list we use (services off).
	exceptsSent string
	exceptsFrom string
	stand       map[string]*Client    // stand-ins for people elsewhere (link_operserv.go)
	jupes       map[string]*jupe      // OperServ JUPE, by lower-case name
	noop        bool                  // NOOP: nobody may be an IRC operator here
	pending     atomic.Int32          // TLS handshakes running on the link port
	operBusy    map[string]*bool      // relayed /oper password checks running, by UID (link_oper.go)
	places      map[string]heardPlace // what servers said about their own place, by lower name (map_places.go)
	zone        string                // time zone for our own place, "" = the machine's (tests set it)
	ipPlace     *control.MapPlace     // where our IP address is (map_places.go), nil = not known
	ipAsked     bool                  // the lookup has started this run
	// remoteOpers: opers we granted on other servers, by UID (2g live changes).
	remoteOpers map[string]remoteGrant
	// logins: the accounts our services logged people on other servers in
	// to, by UID, kept through a netsplit (link_logins.go).
	logins      map[string]loginGrant
	loginsSwept time.Time // when expireLogins last ran
	// Changes to logins not in the database yet (flushLogins): the UIDs,
	// or loginsAll for "write everything, replacing what is there".
	loginsDirty map[string]struct{}
	loginsAll   bool
	loginsRetry time.Time // no database write before this, after a failed one
}

type linkRetry struct {
	next    time.Time
	wait    time.Duration
	dialing bool
	lastErr string
	ruleNo  bool // the link rule said no last time (told opers once)
	// svcNick is set while the last drop was because the network behind has
	// its own services (the nick of its bot); svcNoticed is when opers last
	// heard of it. Cleared when a link to it finishes its burst, or on rehash.
	svcNick    string
	svcNoticed time.Time
}

// remoteServer is one server behind a link.
type remoteServer struct {
	name, sid, desc string
	uplink          string // SID it hangs off (ours for direct links)
	hops            int    // 1 = linked to us directly
	via             *linkConn
	synced          bool // sent EOS
	// What it runs, from the handshake (direct links) and SINFO.
	software          string
	protocol          int
	boot              time.Time
	umodes, chanmodes string
	sinfo             string // its SINFO line as we got it, to pass on
}

// linkConn is one server link, from TCP connect until it closes.
type linkConn struct {
	s         *Server
	conn      net.Conn
	out       bool   // we connected
	want      string // outgoing: the block's server name
	addr      string // remote IP:port
	hs        link.Handshake
	up        bool
	peer      link.Peer
	limit     int // send queue limit in bytes
	started   time.Time
	since     time.Time // link up since
	lastRecv  time.Time
	pingSent  time.Time
	lag       time.Duration
	lastPong  time.Time
	lagWarned bool          // opers were told it isn't answering PING
	synced    time.Duration // how long their burst took
	closed    bool
	ignored   map[string]bool       // commands not handled yet, logged once
	exceptsIn []store.SessionExcept // a BEESESS list coming in (START .. END), nil = none
	warned    bool                  // NETINFO mismatch reported
	codec     link.Codec            // reads and writes users and channels
	bridge    link.Bridge           // another protocol than ours on the wire (InspIRCd)
	fp        string                // the other side's certificate fingerprint
	eos       bool                  // their burst is complete
	netjoin   *netjoinBatch         // JOINs of their burst, for batch clients
	preLines  int                   // lines before the handshake was done
	denied    bool                  // the handshake refused it (not a broken connection)
	svcDrop   bool                  // dropped for two sets of services: dropServicesLink tells opers
	// renameBack: channels their burst brought back under a name we renamed
	// away while they were split off, to rename there too (rename.go).
	renameBack []string

	mu      sync.Mutex
	queue   []string
	queued  int
	closing bool
	over    bool
	notify  chan struct{}
}

// linkSetup loads (or makes) the link certificate and opens the link port
// on hubs and leaves.
func (s *Server) linkSetup() error {
	s.links.conns = map[*linkConn]bool{}
	s.links.servers = map[string]*remoteServer{}
	s.links.retry = map[string]*linkRetry{}
	if !s.cfg.Link.Linked() {
		return nil
	}
	cert, fp, err := link.LoadOrCreate(s.cfg.Link.Cert, s.cfg.Link.Key, s.cfg.Server.Name)
	if err != nil {
		return err
	}
	s.links.cert, s.links.fp = cert, fp
	s.links.debug = os.Getenv("BEEIRCD_LINK_DEBUG") == "1"
	ln, err := net.Listen("tcp", net.JoinHostPort(s.cfg.Link.Address, strconv.Itoa(s.cfg.Link.Port)))
	if err != nil {
		return fmt.Errorf("link port: %w", err)
	}
	s.links.ln = ln
	s.log.Printf("link port %d open (TLS), our fingerprint %s", s.cfg.Link.Port, fp)
	return nil
}

// linkTLS is the TLS setup for links. Both sides show a certificate and
// nobody checks a CA: the fingerprint pinned in the link block is the
// check (like UnrealIRCd's spkifp).
func (s *Server) linkTLS(outgoing bool, host string) *tls.Config {
	c := &tls.Config{
		Certificates:       []tls.Certificate{s.links.cert},
		MinVersion:         tls.VersionTLS12,
		CipherSuites:       tlsutil.Ciphers12,
		InsecureSkipVerify: true, // pinned instead, see verifyPin
	}
	if outgoing {
		c.ServerName = host
	} else {
		c.ClientAuth = tls.RequireAnyClientCert
	}
	return c
}

// peerFingerprint is the fingerprint of the certificate the other side
// showed, "" without one.
func peerFingerprint(tc *tls.Conn) string {
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return ""
	}
	return link.Fingerprint(certs[0])
}

func (s *Server) linkAcceptLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-time.After(100 * time.Millisecond):
				continue
			case <-s.done:
				return
			}
		}
		if s.links.pending.Add(1) > linkMaxPending {
			s.links.pending.Add(-1)
			conn.Close()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.links.pending.Add(-1)
			tc := tls.Server(conn, s.linkTLS(false, ""))
			ctx, cancel := context.WithTimeout(context.Background(), linkDialTimeout)
			err := tc.HandshakeContext(ctx)
			cancel()
			if err != nil {
				s.log.Printf("link port: TLS with %s failed: %v", conn.RemoteAddr(), err)
				conn.Close()
				why := "TLS handshake failed: " + shortNetErr(err)
				if isTimeout(err) {
					why = fmt.Sprintf("TLS handshake timed out (%d seconds)", int(linkDialTimeout.Seconds()))
				}
				s.post(func() { s.linkPortFailed(conn.RemoteAddr(), why) })
				return
			}
			fp := peerFingerprint(tc)
			s.post(func() { s.newLink(tc, false, nil, fp) })
		}()
	}
}

// dialLink connects to a [[link.server]] block's host (off the loop).
func (s *Server) dialLink(b config.LinkServer) {
	defer s.wg.Done()
	addr := net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
	ctx, cancel := context.WithTimeout(context.Background(), linkDialTimeout)
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err == nil {
		tc := tls.Client(conn, s.linkTLS(true, b.Host))
		if err = tc.HandshakeContext(ctx); err == nil {
			fp := peerFingerprint(tc)
			s.post(func() {
				r := s.links.retry[strings.ToLower(b.Name)]
				if r == nil || s.cfg.Link.Server(b.Name) == nil {
					tc.Close() // the block went (REHASH, SERVER DEL) while we dialed
					return
				}
				r.dialing = false
				s.newLink(tc, true, &b, fp)
			})
			return
		}
		conn.Close()
	}
	s.post(func() { s.linkDialFailed(b.Name, err) })
}

// linkPortFailed tells opers that a connection on the link port failed
// before its handshake started (TLS, or too many waiting), at most once per
// address every 10 seconds; the log has each one.
func (s *Server) linkPortFailed(addr net.Addr, why string) {
	ip := addr.String()
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	key := "linkport:" + ip
	now := time.Now()
	if last, ok := s.noticeAt[key]; ok && now.Sub(last) < linkPortNoticeEvery {
		return
	}
	s.noticeAt[key] = now
	text := fmt.Sprintf("Server connection from %s failed: %s", ip, why)
	s.rpcLog(text, "link")
	s.snoticeOpers('l', text)
}

// isTimeout reports whether err is a dial or handshake running out of time.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout()
}

func (s *Server) linkDialFailed(name string, err error) {
	r := s.links.retry[strings.ToLower(name)]
	if r == nil {
		return // the block went while we dialed
	}
	r.dialing = false
	msg := err.Error()
	if isTimeout(err) {
		s.lnotice("CONNECT: Error connecting \x02%s\x02 (timeout of %d seconds)", name, int(linkDialTimeout.Seconds()))
	} else {
		s.lnotice("CONNECT: Error connecting \x02%s\x02: %s.", name, strings.TrimSuffix(shortNetErr(err), "."))
	}
	r.lastErr = msg
	r.wait = min(max(linkRetryMin, r.wait*2), linkRetryMax)
	r.next = time.Now().Add(r.wait)
}

// newLink starts the handshake on a connected TLS link.
func (s *Server) newLink(conn net.Conn, outgoing bool, b *config.LinkServer, fp string) {
	select {
	case <-s.done:
		conn.Close()
		return
	default:
	}
	if !outgoing {
		// Anyone can get this far (any certificate does for TLS): only a
		// few may wait in a handshake at once.
		waiting := 0
		for o := range s.links.conns {
			if !o.up && !o.out {
				waiting++
			}
		}
		if waiting >= linkMaxPending {
			s.log.Printf("link port: refused %s, %d handshakes are waiting already", conn.RemoteAddr(), waiting)
			conn.Close()
			s.linkPortFailed(conn.RemoteAddr(), fmt.Sprintf("%d handshakes are waiting already", waiting))
			return
		}
	}
	now := time.Now()
	limit := 20 << 20
	if b != nil {
		limit = b.SendQMB << 20
	}
	l := &linkConn{
		s: s, conn: conn, out: outgoing, addr: conn.RemoteAddr().String(),
		limit: limit, started: now, lastRecv: now,
		ignored: map[string]bool{}, notify: make(chan struct{}, 1),
	}
	if b != nil {
		l.want = b.Name
	}
	l.fp = fp
	if outgoing && b.Insp() {
		l.hs = link.NewInsp(s.linkLocal(), true, b, fp)
	} else {
		l.hs = link.NewUnreal(s.linkLocal(), outgoing, b, fp, s.linkSIDs)
	}
	s.links.conns[l] = true
	s.wg.Add(1)
	go l.writeLoop()
	if outgoing && fp != b.Fingerprint {
		// Before PASS: never send our password to a server that isn't the
		// one pinned in the block.
		s.dropLink(l, "its certificate fingerprint "+orStr(fp, "(none)")+" does not match the link block")
		return
	}
	if outgoing {
		ip := l.addr
		if h, _, err := net.SplitHostPort(ip); err == nil {
			ip = h
		}
		s.lnotice("Connection to \x02%s\x02[%s] started.", b.Name, ip)
	}
	go l.readLoop()
	for _, line := range l.hs.Start() {
		l.send(line)
	}
}

// linkLocal is what the handshake says about us.
// cloakHash is what NETINFO shows of our cloak keys: the checksum
// UnrealIRCd's module for the method shows, else a short hash of the
// secret.
func (s *Server) cloakHash() string {
	if s.cfg.KeyedCloak() && len(s.cfg.Cloak.Keys) == 3 {
		k := s.cfg.Cloak.Keys
		if s.cfg.InspCloak() {
			return cloak.InspChecksum([3]string{k[0], k[1], k[2]})
		}
		return cloak.UnrealChecksum([3]string{k[0], k[1], k[2]})
	}
	sum := sha256.Sum256([]byte("beeircd-cloak:" + s.cfg.Cloak.Secret))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) linkLocal() link.Local {
	return link.Local{
		Name: s.cfg.Server.Name, SID: s.cfg.Server.SID, Description: s.cfg.Server.Description,
		Network: s.cfg.Server.Network, Version: version.Version,
		ChanModes: chanModesList + "," + chanModesKey + "," + chanModesParam + "," + chanModesFlags,
		UserModes: userModes, Prefix: "(" + s.rankModes() + ")" + s.rankPrefixes(),
		Casemapping: s.cfg.Server.Casemapping, Services: s.cfg.Services.On(),
		CloakHash: s.cloakHash(), Booted: s.created.Unix(), MaxGlobal: max(s.maxGlobal, s.maxLocal),
		Now:   func() int64 { return time.Now().Unix() },
		Known: s.linkKnown,
		Block: func(name string) *config.LinkServer {
			if b := s.cfg.Link.Server(name); b != nil {
				c := *b
				return &c
			}
			return nil
		},
	}
}

// linkKnown reports whether a SID or server name is on the network
// already, this server included.
func (s *Server) linkKnown(x string) bool {
	if x == s.cfg.Server.SID || strings.EqualFold(x, s.cfg.Server.Name) {
		return true
	}
	if si, ok := s.servicesInfo(); ok && (x == si.SID || strings.EqualFold(x, si.Name)) {
		return true
	}
	if s.jupeFor(x) != nil {
		return true
	}
	if s.links.servers[x] != nil {
		return true
	}
	return s.serverByName(x) != nil
}

func (s *Server) serverByName(name string) *remoteServer {
	for _, r := range s.links.servers {
		if strings.EqualFold(r.name, name) {
			return r
		}
	}
	return nil
}

// linkSIDs lists every SID we know besides our own (PROTOCTL SERVERS=).
func (s *Server) linkSIDs() []string {
	out := make([]string, 0, len(s.links.servers))
	for sid := range s.links.servers {
		out = append(out, sid)
	}
	sort.Strings(out)
	return out
}

// linkName is how notices name a link: the server once known, else the
// address.
func (l *linkConn) name() string {
	if l.peer.Name != "" {
		return l.peer.Name
	}
	if n := l.hs.Peer().Name; n != "" {
		return n
	}
	if l.want != "" {
		return l.want
	}
	return l.addr
}

// handleLinkLine runs one line from a link (on the loop).
func (s *Server) handleLinkLine(l *linkConn, line string) {
	if l.closed {
		return
	}
	l.lastRecv = time.Now()
	if s.links.debug {
		s.log.Printf("link %s <- %s", l.name(), debugLine(line))
	}
	if l.up && l.bridge != nil {
		run, reply := l.bridge.In(line)
		for _, r := range reply {
			l.sendWire(r)
		}
		for _, r := range run {
			if l.closed {
				return
			}
			if m, err := irc.Parse(r); err == nil {
				s.linkUpLine(l, m)
			}
		}
		return
	}
	m, err := irc.Parse(line)
	if err != nil {
		return
	}
	if !l.up {
		if l.preLines == 0 && !l.out && link.LooksInsp(m) {
			// An InspIRCd server connecting: its handshake, not UnrealIRCd's.
			l.hs = link.NewInsp(s.linkLocal(), false, nil, l.fp)
		}
		if l.preLines++; l.preLines > linkMaxPreLines {
			s.dropLink(l, fmt.Sprintf("more than %d lines before the handshake was done", linkMaxPreLines))
			return
		}
		out, err := l.hs.Handle(m)
		for _, o := range out {
			l.send(o)
		}
		if err != nil {
			l.denied = true
			s.dropLink(l, err.Error())
			return
		}
		if l.hs.Done() {
			s.linkUp(l)
		}
		return
	}
	s.linkUpLine(l, m)
}

// linkUpLine runs one line (in our dialect) from a link that is up.
func (s *Server) linkUpLine(l *linkConn, m irc.Message) {
	if !l.eos {
		s.netjoin = s.netjoinFor(l)
	}
	s.linkCommand(l, m)
	s.netjoin = nil
}

// linkUp runs when the handshake is done: the server joins the tree, we
// send our burst and tell the other links.
func (s *Server) linkUp(l *linkConn) {
	p := l.hs.Peer()
	l.peer = p
	if b, ok := l.hs.(link.Bridger); ok {
		l.bridge = b.Bridge()
	}
	if !l.out && p.Block.RuleForAll() {
		if rule, no := s.linkRuleSaysNo(p.Block); no {
			l.denied = true
			s.dropLink(l, "link rule "+rule+" says no")
			return
		}
	}
	if s.cfg.Link.Role == "leaf" {
		for o := range s.links.conns {
			if o != l && o.up {
				l.denied = true
				s.dropLink(l, fmt.Sprintf("%s is a leaf and is linked to %s already", s.cfg.Server.Name, o.peer.Name))
				return
			}
		}
	}
	now := time.Now()
	l.up, l.since, l.limit = true, now, p.Block.SendQMB<<20
	if l.limit == 0 {
		l.limit = 20 << 20
	}
	others := s.burstServers(l)
	s.links.servers[p.SID] = &remoteServer{name: p.Name, sid: p.SID, desc: p.Description, uplink: s.cfg.Server.SID, hops: 1, via: l,
		software: p.Software, protocol: p.Protocol}
	s.svc.LogServerJoin(p.Name, p.Description, s.cfg.Server.Name)
	if r := s.links.retry[strings.ToLower(p.Name)]; r != nil {
		r.wait, r.lastErr, r.ruleNo = 0, "", false
	}
	dir := "they connected"
	if l.out {
		dir = "we connected"
	} else {
		s.lnotice("Verified server connection %s (%s)", p.Name, p.Description)
	}
	s.log.Printf("link with %s (%s, %s) is up: %s, %s", p.Name, p.SID, l.addr, orStr(p.Software, "unknown software"), dir)
	s.lnotice("Bursting to \x02%s\x02 (Authentication: %s).", p.Name, linkAuth(l.conn, p.Block.Password != ""))
	for _, line := range l.hs.Burst(others, s.burstBody(l)) {
		l.send(line)
	}
	for _, line := range append(s.operBurst(l), s.swhoisBurst(l)...) {
		l.send(line)
	}
	if p.Bee {
		for _, line := range s.sinfoBurst(l) {
			l.send(line)
		}
	}
	s.saslAnnounce(l)
	if t := s.saslTarget(); t != "" {
		if r := s.serverByName(t); r != nil && r.via != l {
			l.send(":" + r.sid + " MD client " + r.name + " saslmechlist :" + s.links.saslMechs)
		}
	}
	s.lnotice("Finished bursting to \x02%s\x02.", p.Name)
	s.linkBroadcast(l, fmt.Sprintf(":%s SID %s 2 %s :%s", s.cfg.Server.SID, p.Name, p.SID, p.Description))
}

// burstServers lists the servers to introduce to l: everyone not behind
// l, nearest first so every uplink comes before what hangs off it.
func (s *Server) burstServers(l *linkConn) []link.ServerInfo {
	var out []link.ServerInfo
	if si, ok := s.servicesInfo(); ok {
		out = append(out, si)
	}
	out = append(out, s.jupeInfos()...)
	for _, r := range s.links.servers {
		if r.via == l {
			continue
		}
		out = append(out, link.ServerInfo{Name: r.name, SID: r.sid, Description: r.desc, Uplink: r.uplink, Hops: r.hops, Synced: r.synced})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		return out[i].SID < out[j].SID
	})
	return out
}

// linkBroadcast sends line to every link that is up except one.
func (s *Server) linkBroadcast(except *linkConn, line string) {
	for o := range s.links.conns {
		if o != except && o.up && !o.closed {
			o.send(line)
		}
	}
}

// sourceServer is the server a line came from: by SID or name, and only
// one behind l (a link can't speak for servers elsewhere).
func (s *Server) sourceServer(l *linkConn, src string) *remoteServer {
	r := s.links.servers[src]
	if r == nil {
		r = s.serverByName(src)
	}
	if r == nil || r.via != l {
		return nil
	}
	return r
}

// linkCommand runs one line from a link that is up.
func (s *Server) linkCommand(l *linkConn, m irc.Message) {
	param := func(i int) string {
		if i < len(m.Params) {
			return m.Params[i]
		}
		return ""
	}
	if !s.linkGate(l, m) {
		return
	}
	if s.relayedIn(l, m) {
		return
	}
	switch m.Command {
	case "PING":
		if dest := param(1); dest == "" || strings.EqualFold(dest, s.cfg.Server.Name) || dest == s.cfg.Server.SID {
			l.send(fmt.Sprintf(":%s PONG %s :%s", s.cfg.Server.SID, s.cfg.Server.Name, param(0)))
		}
	case "PONG":
		if !l.pingSent.IsZero() {
			// Never exactly 0 once measured (a coarse clock can say 0),
			// so "not measured yet" stays apart.
			l.lag = max(time.Since(l.pingSent), time.Microsecond)
			l.pingSent, l.lastPong = time.Time{}, time.Now()
			if l.lagWarned {
				l.lagWarned = false
				s.lnotice("Server \x02%s\x02 is responding to PING again (lag %d ms)", l.peer.Name, l.lag.Milliseconds())
			}
		}
	case "SID":
		s.linkSID(l, m)
	case "EOS":
		r := s.sourceServer(l, m.Source)
		if r == nil || r.synced {
			return
		}
		r.synced = true
		if r.sid == l.peer.SID {
			l.eos = true
			s.linkServicesOK(r.name)
			l.netjoin.end()
			l.synced = time.Since(l.since)
			s.lnotice("Received end of netburst from \x02%s\x02 (burst time: %d ms)", r.name, l.synced.Milliseconds())
			s.sendPlaces(l)
			s.sendExcepts(l)
			s.renameAfterSplit(l)
		}
		s.linkBroadcast(l, ":"+r.sid+" EOS")
	case "SQUIT":
		s.linkSQUIT(l, m)
	case "ERROR":
		s.dropLink(l, closingReason(param(len(m.Params)-1)))
	case "NETINFO":
		s.linkNetinfo(l, m)
	case "MD":
		s.linkMD(l, m)
	case "SASL":
		s.linkSASL(l, m)
	case "WHOIS":
		s.remoteWhois(l, m)
	case "MOTD", "VERSION", "ADMIN", "TIME", "STATS", "TRACE":
		s.remoteQuery(l, m)
	case "TSCTL":
		s.remoteTsctl(l, m)
	case "ACCEPT":
		s.remoteAccept(l, m)
	case "SDESC":
		s.remoteSdesc(l, m)
	case "UNINVITE":
		s.remoteUninvite(l, m)
	case "SVSNOOP":
		s.remoteNoop(l, m)
	case "BEEADMIN":
		s.remoteAdmin(l, m)
	case "BEEOPER":
		s.remoteOper(l, m)
	case "BEEOPERR":
		s.operAnswered(l, m)
	case "BEEOPERLOOK":
		s.remoteOperLook(l, m)
	case "BEEPLACE":
		s.remotePlace(l, m.Source, m.Params)
	case "BEESESS":
		s.remoteExcepts(l, m)
	case "REPUTATION":
		s.remoteReputation(l, m)
	case "SINFO":
		s.linkSINFO(l, m)
	case "GLOBOPS":
		s.remoteGlobops(l, m)
	case "WALLOPS":
		s.remoteWallops(l, m)
	case "SENDUMODE":
		s.remoteSendUmode(l, m)
	case "SENDSNO":
		s.remoteSendSno(l, m)
	case "SLOG":
		s.remoteSlog(l, m)
	case "SWHOIS":
		s.remoteSwhois(l, m)
	case "SMOD", "BOTMOTD":
		// Server info, module lists and notices for opers: nothing to do,
		// and nothing worth a log line.
	default:
		if s.linkItem(l, m) {
			return
		}
		if !l.ignored[m.Command] {
			l.ignored[m.Command] = true
			s.log.Printf("link %s: %s not handled (skipped)", l.peer.Name, m.Command)
		}
	}
}

// linkSINFO is UnrealIRCd's ":sid SINFO boottime protocol umodes chanmodes
// nickchars :software": what a server runs, for the rpc server.* answers.
// It goes on to BeeIRCd links only.
func (s *Server) linkSINFO(l *linkConn, m irc.Message) {
	r := s.sourceServer(l, m.Source)
	if r == nil || len(m.Params) < 6 {
		return
	}
	if boot, err := strconv.ParseInt(m.Params[0], 10, 64); err == nil && boot > 0 {
		r.boot = time.Unix(boot, 0)
	}
	if n, err := strconv.Atoi(m.Params[1]); err == nil {
		r.protocol = n
	}
	r.umodes, r.chanmodes = irc.Truncate(m.Params[2], 64), irc.Truncate(m.Params[3], 200)
	r.software = irc.Truncate(m.Params[len(m.Params)-1], 80)
	r.sinfo = m.String()
	for o := range s.links.conns {
		if o != l && o.up && !o.closed && o.peer.Bee {
			o.send(r.sinfo)
		}
	}
	if l.eos { // a rename after the split may have waited for this
		s.renameAfterSplit(l)
	}
}

// sinfoBurst is SINFO for us and every server not behind l whose SINFO we
// have, for a BeeIRCd link.
func (s *Server) sinfoBurst(l *linkConn) []string {
	mine := func(sid string) string {
		return fmt.Sprintf(":%s SINFO %d %d %s %s * :%s", sid, s.created.Unix(), link.UnrealProtocol,
			userModes, strings.Join([]string{chanModesList, chanModesKey, chanModesParam, chanModesFlags}, ","), version.String())
	}
	out := []string{mine(s.cfg.Server.SID)}
	if si, ok := s.servicesInfo(); ok {
		out = append(out, mine(si.SID)) // services run inside us
	}
	for _, r := range s.links.servers {
		if r.sinfo != "" && !s.behind(l, r.sid) {
			out = append(out, r.sinfo)
		}
	}
	return out
}

// linkSID is ":uplink SID name hops sid :description": a server behind l.
func (s *Server) linkSID(l *linkConn, m irc.Message) {
	if len(m.Params) < 4 {
		return
	}
	up := s.sourceServer(l, m.Source)
	name, sid, desc := m.Params[0], m.Params[2], m.Params[len(m.Params)-1]
	switch {
	case up == nil:
		s.dropLink(l, fmt.Sprintf("SID %s from an unknown server %s", sid, m.Source))
		return
	case !link.ValidSID(sid) || !config.ValidServerName(name):
		s.dropLink(l, fmt.Sprintf("bad SID line for %s %s", name, sid))
		return
	case s.linkKnown(sid) || s.linkKnown(name):
		// A loop or two servers with one name/SID: UnrealIRCd drops the
		// link that brought the second one.
		s.dropLink(l, fmt.Sprintf("Server Exists: %s (%s) is on the network already", name, sid))
		return
	}
	r := &remoteServer{name: name, sid: sid, desc: desc, uplink: up.sid, hops: up.hops + 1, via: l}
	s.links.servers[sid] = r
	s.svc.LogServerJoin(name, desc, up.name)
	s.linkBroadcast(l, fmt.Sprintf(":%s SID %s %d %s :%s", up.sid, name, r.hops+1, sid, desc))
	s.lnotice("Server \x02%s\x02 introduced server \x02%s\x02 (%s)", up.name, name, desc)
}

// linkSQUIT is ":source SQUIT name :reason".
func (s *Server) linkSQUIT(l *linkConn, m irc.Message) {
	if len(m.Params) < 1 {
		return
	}
	target, reason := m.Params[0], ""
	if len(m.Params) > 1 {
		reason = m.Params[len(m.Params)-1]
	}
	if s.squitJupe(l, target) {
		return
	}
	r := s.links.servers[target]
	if r == nil {
		r = s.serverByName(target)
	}
	switch {
	case r == nil && (strings.EqualFold(target, s.cfg.Server.Name) || target == s.cfg.Server.SID):
		s.dropLink(l, "they ended the link: "+reason)
		return
	case r == nil || r.via != l:
		return // not behind this link: stale, ignore like UnrealIRCd
	case r.sid == l.peer.SID:
		s.dropLink(l, "they ended the link: "+reason)
		return
	}
	gone, users := s.splitOff(r.sid, s.links.servers[r.uplink].nameOr(r.uplink), r.name, reason)
	s.linkBroadcast(l, fmt.Sprintf(":%s SQUIT %s :%s", r.uplink, r.name, reason))
	s.remoteSplit(r.name, s.links.servers[r.uplink].nameOr(r.uplink), reason, gone, users)
}

func (r *remoteServer) nameOr(fallback string) string {
	if r == nil {
		return fallback
	}
	return r.name
}

// netsplitDone is InspIRCd's line after a split: how many users and
// servers went (the split server counts).
func (s *Server) netsplitDone(servers, users int) {
	s.lnotice("Netsplit complete, lost \x02%d\x02 %s on \x02%d\x02 %s.", users, pluralS(users, "user"), servers, pluralS(servers, "server"))
}

// remoteSplit tells opers that name, behind uplink, split off.
func (s *Server) remoteSplit(name, uplink, reason string, servers, users int) {
	s.lnotice("Server \x02%s\x02 split from server \x02%s\x02 with reason: %s", name, uplink, reason)
	s.netsplitDone(servers, users)
}

// pluralS is "user" for 1, else "users" (the word alone).
func pluralS(n int, what string) string {
	if n == 1 {
		return what
	}
	return what + "s"
}

// countOf is "1 user", "86 users".
func countOf(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// splitOff removes a server and everything behind it, with the users on
// them (quit reason "near far"); it returns how many servers and users
// went. reason goes in the services log line.
func (s *Server) splitOff(sid, near, far, reason string) (servers, users int) {
	gone := map[string]bool{sid: true}
	for changed := true; changed; {
		changed = false
		for k, r := range s.links.servers {
			if !gone[k] && gone[r.uplink] {
				gone[k], changed = true, true
			}
		}
	}
	s.logSplit(gone, reason)
	users = s.quitSplit(gone, near, far)
	for k := range gone {
		delete(s.links.servers, k)
		delete(s.roBy, k)
	}
	return len(gone), users
}

// logSplit posts a services log line for each server in gone, nearest
// first.
func (s *Server) logSplit(gone map[string]bool, reason string) {
	var rs []*remoteServer
	for sid := range gone {
		if r := s.links.servers[sid]; r != nil {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].hops != rs[j].hops {
			return rs[i].hops < rs[j].hops
		}
		return rs[i].name < rs[j].name
	})
	for _, r := range rs {
		s.svc.LogServerSplit(r.name, r.desc, s.uplinkName(r.uplink), reason)
	}
}

// uplinkName is the name of the server with SID sid: this one or a linked
// one.
func (s *Server) uplinkName(sid string) string {
	if sid == s.cfg.Server.SID {
		return s.cfg.Server.Name
	}
	return s.links.servers[sid].nameOr(sid)
}

// linkNetinfo is "NETINFO maxglobal ts protocol cloakhash 0 0 0 :network".
// A different network name or cloak secret is worth a warning, not a
// refusal (UnrealIRCd does the same).
func (s *Server) linkNetinfo(l *linkConn, m irc.Message) {
	if len(m.Params) < 8 || l.warned {
		return
	}
	var why []string
	if net := m.Params[len(m.Params)-1]; net != s.cfg.Server.Network {
		why = append(why, fmt.Sprintf("its network name is %q, ours %q", net, s.cfg.Server.Network))
	}
	switch theirs := m.Params[3]; {
	case theirs == s.cloakHash():
	case !l.peer.Bee && strings.HasPrefix(theirs, "INSPMD5:") && !s.cfg.InspCloak():
		why = append(why, "it loads the cloak_inspircd module, so people get different cloaks there and bans on cloaks miss (set [cloak] method = \"cloak_inspircd\" with its three cloak-keys)")
	case !l.peer.Bee && !s.cfg.KeyedCloak():
		why = append(why, "UnrealIRCd makes its own cloaks, so people get different cloaks there and bans on cloaks miss (set [cloak] method = \"unreal\" with its three cloak-keys)")
	case s.cfg.KeyedCloak():
		why = append(why, "its cloak method or keys differ from ours, so the same person gets a different cloak there (the same method and the same three keys on every server)")
	default:
		why = append(why, "its [cloak] secret differs from ours, so the same person gets a different cloak there (set the same secret on every server)")
	}
	if len(why) > 0 {
		l.warned = true
		s.lnotice("\x02WARNING\x02: link with \x02%s\x02: %s", l.peer.Name, strings.Join(why, "; "))
	}
}

// dropLink ends a link: the other side gets ERROR, opers a notice, the
// rest of the network an SQUIT for everything behind it.
func (s *Server) dropLink(l *linkConn, reason string) {
	if l.closed {
		return
	}
	l.closed = true
	delete(s.links.conns, l)
	s.dropQlinesVia(l)
	s.operRelayLost(l)
	l.netjoin.end()
	l.send("ERROR :Closing Link: " + s.cfg.Server.Name + " (" + reason + ")")
	l.close()
	name := l.name()
	if r := s.links.retry[strings.ToLower(name)]; r != nil && l.up {
		r.next, r.wait = time.Now().Add(linkRetryFirst), 0
	}
	if !l.up {
		s.linkFailed(l, name, reason)
		return
	}
	behind := map[string]bool{}
	for sid, r := range s.links.servers {
		if r.via == l {
			behind[sid] = true
		}
	}
	if !l.svcDrop {
		s.logSplit(behind, reason)
	}
	users := s.quitSplit(behind, s.cfg.Server.Name, l.peer.Name)
	for sid := range behind {
		delete(s.links.servers, sid)
		delete(s.roBy, sid)
	}
	gone := len(behind)
	s.linkBroadcast(nil, fmt.Sprintf(":%s SQUIT %s :%s", s.cfg.Server.SID, l.peer.Name, reason))
	if l.svcDrop {
		return // dropServicesLink says it once an hour
	}
	s.lnotice("Server \x02%s\x02 split: %s", l.peer.Name, reason)
	s.netsplitDone(gone, users)
	s.lnotice("Connection to '\x02%s\x02' was established for %s", l.peer.Name, longDuration(time.Since(l.since)))
}

// dropServicesLink ends link l (nil in tests) because the network behind it
// runs its own services (bot nick). It would only drop again, so the next
// autoconnect try waits linkServicesRetry, and opers hear of it at most once
// an hour per server; the split notices of dropLink stay out. A manual
// CONNECT or a rehash tries right away.
func (s *Server) dropServicesLink(l *linkConn, name, nick, reason string) {
	key := strings.ToLower(name)
	r := s.links.retry[key]
	if r == nil {
		r = &linkRetry{}
		s.links.retry[key] = r
	}
	now := time.Now()
	quiet := !r.svcNoticed.IsZero() && now.Sub(r.svcNoticed) < linkServicesNoticeEvery
	if l != nil {
		l.svcDrop = true
		s.dropLink(l, reason)
	}
	r.svcNick = nick
	r.next, r.wait = now.Add(linkServicesRetry), 0
	if !quiet {
		r.svcNoticed = now
		who := ""
		if nick != "" {
			who = " (" + nick + ")"
		}
		s.lnotice("\x02%s\x02 runs its own services%s; retrying every %d minutes. Turn services off on one side. (%s)",
			name, who, int(linkServicesRetry.Minutes()), reason)
	}
}

// linkServicesOK forgets a two-services drop of name: its link got through
// its burst, so normal retry timing and notices are back.
func (s *Server) linkServicesOK(name string) {
	if r := s.links.retry[strings.ToLower(name)]; r != nil {
		r.svcNick, r.svcNoticed = "", time.Time{}
	}
}

// linkFailed tells opers why a link ended before its handshake was done:
// our try to connect failed, they were refused, or their connection broke.
//
// A refusal would only come again, so our next autoconnect try backs off as
// after a failed dial (both sides running services: linkServicesRetry, told
// once an hour), and the same refusal of an incoming link is told to opers at
// most every linkDeniedNoticeEvery; the log keeps each one.
func (s *Server) linkFailed(l *linkConn, name, reason string) {
	bothSvc := strings.Contains(reason, link.BothServices)
	if l.out {
		if r := s.links.retry[strings.ToLower(name)]; r != nil {
			if bothSvc {
				s.dropServicesLink(nil, name, "", reason)
				return
			}
			r.wait = min(max(linkRetryMin, r.wait*2), linkRetryMax)
			r.next = time.Now().Add(r.wait)
		}
	} else if l.denied || bothSvc {
		key, every := "linkdeny:"+strings.ToLower(name)+":"+reason, linkDeniedNoticeEvery
		if bothSvc {
			key, every = "linksvc:"+strings.ToLower(name), linkServicesNoticeEvery
		}
		now := time.Now()
		if last, ok := s.noticeAt[key]; ok && now.Sub(last) < every {
			s.log.Printf("Server connection from %s denied again (opers were told), %s", name, reason)
			return
		}
		s.noticeAt[key] = now
	}
	switch {
	case l.out && reason == linkHandshakeTimedOut:
		s.lnotice("CONNECT: Error connecting \x02%s\x02 (timeout of %d seconds)", name, int(linkHandshakeTimeout.Seconds()))
	case l.out:
		s.lnotice("CONNECT: Error connecting \x02%s\x02: %s.", name, strings.TrimSuffix(reason, "."))
	case name == l.addr: // no server name yet: only the address
		ip := l.addr
		if h, _, err := net.SplitHostPort(ip); err == nil {
			ip = h
		}
		if l.denied {
			s.lnotice("Server connection from %s denied, %s", ip, reason)
		} else {
			s.lnotice("Server connection from %s failed: %s", ip, reason)
		}
	case l.denied:
		s.lnotice("Server connection from \x02%s\x02 denied, %s", name, reason)
	default:
		s.lnotice("Server connection from \x02%s\x02 failed: %s", name, reason)
	}
}

// closingReason is the reason in a peer's ERROR line without the
// "Closing Link: name (" wrapper, so the split notice reads "Server X
// split: Server updating" as InspIRCd's does. Other ERROR texts stay as
// they are; an empty one reads "closed the link".
func closingReason(text string) string {
	const prefix = "closing link:"
	if len(text) > len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
		rest := strings.TrimSpace(text[len(prefix):])
		if i := strings.IndexByte(rest, '('); i >= 0 && strings.HasSuffix(rest, ")") {
			rest = rest[i+1 : len(rest)-1]
		}
		text = rest
	}
	if text = strings.TrimSpace(text); text == "" {
		return "closed the link"
	}
	return text
}

// orStr is a unless it is empty, else b.
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// longDuration writes a duration the way InspIRCd's
// Duration::ToLongString does, every non-zero unit: "45 seconds",
// "15 minutes and 3 seconds", "1 week, 2 days, 3 hours and 5 minutes".
func longDuration(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs <= 0 {
		return "0 seconds"
	}
	units := []struct {
		n    int64
		name string
	}{{365 * 24 * 3600, "year"}, {7 * 24 * 3600, "week"}, {24 * 3600, "day"}, {3600, "hour"}, {60, "minute"}, {1, "second"}}
	var parts []string
	for _, u := range units {
		if n := secs / u.n; n > 0 {
			parts = append(parts, countOf(int(n), u.name))
			secs -= n * u.n
		}
	}
	return config.JoinWords(parts)
}

// linkTick handles handshake timeouts, pings and autoconnect.
func (s *Server) linkTick(now time.Time) {
	for l := range s.links.conns {
		switch {
		case !l.up && now.Sub(l.started) > linkHandshakeTimeout:
			s.dropLink(l, linkHandshakeTimedOut)
		case l.up && l.pingSent.IsZero() && (now.Sub(l.lastRecv) > linkPingIdle ||
			// Lag: measured as soon as the link is synced, then every
			// linkPingIdle even when the link is busy, so it stays current.
			l.eos && (l.lastPong.IsZero() || now.Sub(l.lastPong) > linkPingIdle)):
			l.pingSent = time.Now()
			l.send("PING :" + s.cfg.Server.Name)
		case l.up && !l.pingSent.IsZero() && now.Sub(l.pingSent) > linkPingTimeout:
			s.dropLink(l, fmt.Sprintf("Ping timeout: %d seconds", int(now.Sub(l.lastRecv).Seconds())))
		case l.up && !l.pingSent.IsZero() && !l.lagWarned && now.Sub(l.pingSent) > linkPingWarn:
			l.lagWarned = true
			s.lnotice("Server \x02%s\x02 has not responded to PING for %d seconds, high latency.", l.peer.Name, int(now.Sub(l.pingSent).Seconds()))
		}
	}
	s.expireLogins(now)
	s.flushLogins(now, false)
	s.autoconnect(now)
}

// autoconnect dials [[link.server]] blocks with autoconnect that aren't
// linked. A leaf keeps to one uplink: it dials only while it has none.
func (s *Server) autoconnect(now time.Time) {
	if !s.cfg.Link.Linked() || s.links.ln == nil {
		return
	}
	busy := false // a leaf with a link up or a dial running
	for l := range s.links.conns {
		busy = busy || l.up || l.out
	}
	for _, r := range s.links.retry {
		busy = busy || r.dialing
	}
	for _, b := range s.cfg.Link.Servers {
		if !b.Autoconnect {
			continue
		}
		if s.cfg.Link.Role == "leaf" && busy {
			return
		}
		key := strings.ToLower(b.Name)
		r := s.links.retry[key]
		if r == nil {
			r = &linkRetry{}
			s.links.retry[key] = r
		}
		if r.dialing || now.Before(r.next) || s.linkKnown(b.Name) || s.linkPending(b.Name) {
			continue
		}
		if rule, no := s.linkRuleSaysNo(b); no {
			// Look again later; tell opers only when the answer changes,
			// not on every try.
			r.next = now.Add(linkRetryMin)
			if !r.ruleNo {
				r.ruleNo = true
				s.lnotice("CONNECT: Not connecting to \x02%s\x02: link rule %s says no", b.Name, rule)
			}
			continue
		}
		r.ruleNo = false
		r.dialing, busy = true, true
		r.next = now.Add(linkRetryMin)
		s.lnotice("AUTOCONNECT: Auto-connecting server \x02%s\x02", b.Name)
		s.wg.Add(1)
		go s.dialLink(b)
	}
}

// linkPending reports whether a handshake with name is running.
func (s *Server) linkPending(name string) bool {
	for l := range s.links.conns {
		if !l.up && strings.EqualFold(l.hs.Peer().Name, name) {
			return true
		}
	}
	return false
}

// reapLinks drops links whose send queue overflowed.
func (s *Server) reapLinks() {
	for len(s.links.over) > 0 {
		over := s.links.over
		s.links.over = nil
		for _, l := range over {
			s.dropLink(l, fmt.Sprintf("SendQ exceeded (%d MB)", l.limit>>20))
		}
	}
}

// linkRehash drops links whose block is gone after REHASH.
func (s *Server) linkRehash() {
	for l := range s.links.conns {
		if l.up && s.cfg.Link.Server(l.peer.Name) == nil {
			s.dropLink(l, "its link block was removed")
		}
	}
	for key, r := range s.links.retry {
		if s.cfg.Link.Server(key) == nil {
			delete(s.links.retry, key)
		} else if r.svcNick != "" {
			// The config may have changed (services off): try again now.
			r.svcNick, r.svcNoticed, r.next = "", time.Time{}, time.Now()
		}
	}
}

// closeLinks ends every link at shutdown.
func (s *Server) closeLinks(reason string) {
	if s.links.ln != nil {
		s.links.ln.Close()
	}
	for l := range s.links.conns {
		l.closed = true
		l.send("ERROR :Closing Link: " + s.cfg.Server.Name + " (" + reason + ")")
		l.close()
	}
}

// linkStatus is the Links part of `beeircd status`.
func (s *Server) linkStatus() []control.Link {
	if !s.cfg.Link.Linked() {
		return nil
	}
	var out []control.Link
	seen := map[string]bool{}
	for l := range s.links.conns {
		st := control.Link{Name: l.name(), Out: l.out}
		if l.up {
			st.State, st.SID, st.Since, st.LagMS, st.Software = "up", l.peer.SID, l.since, float64(l.lag)/float64(time.Millisecond), l.peer.Software
			for _, r := range s.links.servers {
				if r.via == l && r.sid != l.peer.SID {
					st.Behind++
				}
			}
		} else {
			st.State = "linking"
		}
		seen[strings.ToLower(st.Name)] = true
		out = append(out, st)
	}
	for _, b := range s.cfg.Link.Servers {
		if seen[strings.ToLower(b.Name)] {
			continue
		}
		st := control.Link{Name: b.Name, State: "down", Out: b.Autoconnect}
		if r := s.links.retry[strings.ToLower(b.Name)]; r != nil && b.Autoconnect {
			st.Error, st.Retry = r.lastErr, r.next
			if r.ruleNo {
				st.Error = "link rule says no"
			}
			if r.dialing {
				st.State = "connecting"
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---- link I/O ----

// send queues one line (on the loop).
func (l *linkConn) send(line string) {
	if l.bridge != nil && l.up {
		for _, w := range l.bridge.Out(line) {
			l.sendWire(w)
		}
		return
	}
	l.sendWire(line)
}

// sendWire queues a line as it goes on the wire.
func (l *linkConn) sendWire(line string) {
	if l.s.links.debug {
		l.s.log.Printf("link %s -> %s", l.name(), debugLine(line))
	}
	line = irc.CleanFraming(line) + crlf
	l.mu.Lock()
	if l.closing || l.over {
		l.mu.Unlock()
		return
	}
	if l.queued+len(line) > l.limit {
		l.over = true
		l.mu.Unlock()
		l.s.links.over = append(l.s.links.over, l)
		return
	}
	l.queue = append(l.queue, line)
	l.queued += len(line)
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

func (l *linkConn) close() {
	l.mu.Lock()
	l.closing = true
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

func (l *linkConn) writeLoop() {
	defer l.s.wg.Done()
	for {
		l.mu.Lock()
		for len(l.queue) == 0 && !l.closing {
			l.mu.Unlock()
			<-l.notify
			l.mu.Lock()
		}
		q := l.queue
		l.queue, l.queued = nil, 0
		closing := l.closing
		l.mu.Unlock()
		var buf []byte
		for _, line := range q {
			buf = append(buf, line...)
		}
		l.conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
		if _, err := l.conn.Write(buf); err != nil {
			l.conn.Close()
			return
		}
		if closing {
			l.mu.Lock()
			empty := len(l.queue) == 0
			l.mu.Unlock()
			if empty {
				l.conn.Close()
				return
			}
		}
	}
}

func (l *linkConn) readLoop() {
	r := bufio.NewReaderSize(l.conn, linkMaxLine)
	for {
		b, err := r.ReadSlice('\n')
		if err != nil {
			reason := "Connection closed"
			switch {
			case errors.Is(err, bufio.ErrBufferFull):
				reason = "line too long"
			case !errors.Is(err, io.EOF):
				reason = "Read error: " + shortNetErr(err)
			}
			l.conn.Close()
			l.s.post(func() { l.s.dropLink(l, reason) })
			return
		}
		line := strings.TrimRight(string(b), "\r\n")
		if line == "" {
			continue
		}
		l.s.post(func() { l.s.handleLinkLineSafe(l, line) })
	}
}

// shortNetErr drops the "read tcp a->b:" part of a network error.
func shortNetErr(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// linkAuth is how a link was checked, for the "Bursting to" notice:
// "TLS certificate fingerprint and plaintext password, TLS 1.3
// TLS_AES_128_GCM_SHA256". Links are always TLS with the certificate
// pinned; the password is checked when the block has one.
func linkAuth(conn net.Conn, password bool) string {
	auth := "TLS certificate fingerprint"
	if password {
		auth += " and plaintext password"
	}
	if tc, ok := conn.(*tls.Conn); ok {
		st := tc.ConnectionState()
		auth += ", " + tls.VersionName(st.Version) + " " + tls.CipherSuiteName(st.CipherSuite)
	}
	return auth
}
