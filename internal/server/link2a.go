package server

// Stage 2a: server links - transport and handshake (PLAN-2.md).
//
// A link is a TLS connection on the [link] port (incoming) or to a
// [[link.server]] host (outgoing). Both sides show a certificate; the
// other side's public key fingerprint must match its link block. The
// handshake is UnrealIRCd 6's (internal/link); once it is done we keep the
// server tree (SID, SQUIT, EOS), answer PING and measure lag. Users and
// channels over links arrive in 2b/2c: until then their lines are ignored
// (logged once per command).
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
	"github.com/fhazal/beeircd/internal/tlsutil"
	"github.com/fhazal/beeircd/internal/version"
)

// Link timings. UnrealIRCd's servers class usually pings every 60-90 s.
const (
	linkHandshakeTimeout = 30 * time.Second
	linkPingIdle         = 60 * time.Second // PING after this long without a line
	linkPingTimeout      = 90 * time.Second // drop when the PONG takes longer
	linkDialTimeout      = 15 * time.Second
	linkRetryMin         = 30 * time.Second // after a failed connect, doubling
	linkRetryMax         = 5 * time.Minute
	linkMaxPending       = 32       // TLS handshakes in progress on the link port
	linkMaxLine          = 64 << 10 // BIGLINES lines plus tags fit easily
	// linkMaxPreLines: lines a server may send before its handshake is
	// done (PASS, a few PROTOCTL, SERVER: about 10 in real use).
	linkMaxPreLines = 64
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
	retry   map[string]*linkRetry    // by lower-case block name
	over    []*linkConn              // send queue overflowed, drop after this job
	debug   bool                     // BEEIRCD_LINK_DEBUG=1: log every link line
	// saslServer takes SASL for our clients when services are off here
	// (it sent MD saslmechlist); sasl are logins our services run for
	// clients on other servers, by UID.
	saslServer, saslMechs string
	sasl                  map[string]*saslSession
	// qlines are nick bans from other servers; qlineSent our own as last
	// sent, by folded mask.
	qlines    map[string]qline
	qlineSent map[string]*link.Ban
	stand     map[string]*Client    // stand-ins for people elsewhere (link2d_ops.go)
	jupes     map[string]*jupe      // OperServ JUPE, by lower-case name
	noop      bool                  // NOOP: nobody may be an IRC operator here
	pending   atomic.Int32          // TLS handshakes running on the link port
	operBusy  map[string]*bool      // relayed /oper password checks running, by UID (link2f_oper.go)
	places    map[string]heardPlace // what servers said about their own place, by lower name (panel3l.go)
	zone      string                // time zone for our own place, "" = the machine's (tests set it)
	ipPlace   *control.MapPlace     // where our IP address is (panel3l.go), nil = not known
	ipAsked   bool                  // the lookup has started this run
	// remoteOpers: opers we granted on other servers, by UID (2g live changes).
	remoteOpers map[string]remoteGrant
}

type linkRetry struct {
	next    time.Time
	wait    time.Duration
	dialing bool
	lastErr string
}

// remoteServer is one server behind a link.
type remoteServer struct {
	name, sid, desc string
	uplink          string // SID it hangs off (ours for direct links)
	hops            int    // 1 = linked to us directly
	via             *linkConn
	synced          bool // sent EOS
}

// linkConn is one server link, from TCP connect until it closes.
type linkConn struct {
	s        *Server
	conn     net.Conn
	out      bool   // we connected
	want     string // outgoing: the block's server name
	addr     string // remote IP:port
	hs       link.Handshake
	up       bool
	peer     link.Peer
	limit    int // send queue limit in bytes
	started  time.Time
	since    time.Time // link up since
	lastRecv time.Time
	pingSent time.Time
	lag      time.Duration
	lastPong time.Time
	synced   time.Duration // how long their burst took
	closed   bool
	ignored  map[string]bool // commands not handled yet, logged once
	warned   bool            // NETINFO mismatch reported
	codec    link.Codec      // reads and writes users and channels (2b)
	eos      bool            // their burst is complete
	netjoin  *netjoinBatch   // JOINs of their burst, for batch clients
	preLines int             // lines before the handshake was done

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

func (s *Server) linkDialFailed(name string, err error) {
	r := s.links.retry[strings.ToLower(name)]
	if r == nil {
		return // the block went while we dialed
	}
	r.dialing = false
	msg := err.Error()
	if r.lastErr != msg {
		s.snotice("Link to %s failed: %s (trying again every few minutes)", name, msg)
	} else {
		s.log.Printf("link to %s failed again: %s", name, msg)
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
	l.hs = link.NewUnreal(s.linkLocal(), outgoing, b, fp, s.linkSIDs)
	s.links.conns[l] = true
	s.wg.Add(1)
	go l.writeLoop()
	if outgoing && fp != b.Fingerprint {
		// Before PASS: never send our password to a server that isn't the
		// one pinned in the block.
		s.dropLink(l, "its certificate fingerprint "+orStr(fp, "(none)")+" does not match the link block")
		return
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
	m, err := irc.Parse(line)
	if err != nil {
		return
	}
	if !l.up {
		if l.preLines++; l.preLines > linkMaxPreLines {
			s.dropLink(l, fmt.Sprintf("more than %d lines before the handshake was done", linkMaxPreLines))
			return
		}
		out, err := l.hs.Handle(m)
		for _, o := range out {
			l.send(o)
		}
		if err != nil {
			s.dropLink(l, err.Error())
			return
		}
		if l.hs.Done() {
			s.linkUp(l)
		}
		return
	}
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
	if s.cfg.Link.Role == "leaf" {
		for o := range s.links.conns {
			if o != l && o.up {
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
	s.links.servers[p.SID] = &remoteServer{name: p.Name, sid: p.SID, desc: p.Description, uplink: s.cfg.Server.SID, hops: 1, via: l}
	if r := s.links.retry[strings.ToLower(p.Name)]; r != nil {
		r.wait, r.lastErr = 0, ""
	}
	for _, line := range l.hs.Burst(others, s.burstBody(l)) {
		l.send(line)
	}
	for _, line := range s.operBurst(l) {
		l.send(line)
	}
	s.saslAnnounce(l)
	if t := s.saslTarget(); t != "" {
		if r := s.serverByName(t); r != nil && r.via != l {
			l.send(":" + r.sid + " MD client " + r.name + " saslmechlist :" + s.links.saslMechs)
		}
	}
	s.linkBroadcast(l, fmt.Sprintf(":%s SID %s 2 %s :%s", s.cfg.Server.SID, p.Name, p.SID, p.Description))
	dir := "they connected"
	if l.out {
		dir = "we connected"
	}
	s.snotice("Link with %s (%s, %s) established: %s, %s", p.Name, p.SID, l.addr, orStr(p.Software, "unknown software"), dir)
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
			l.netjoin.end()
			l.synced = time.Since(l.since)
			s.snotice("Link with %s: sync done in %s", r.name, l.synced.Round(time.Millisecond))
			s.sendPlaces(l)
		}
		s.linkBroadcast(l, ":"+r.sid+" EOS")
	case "SQUIT":
		s.linkSQUIT(l, m)
	case "ERROR":
		s.dropLink(l, "they closed the link: "+param(len(m.Params)-1))
	case "NETINFO":
		s.linkNetinfo(l, m)
	case "MD":
		s.linkMD(l, m)
	case "SASL":
		s.linkSASL(l, m)
	case "WHOIS":
		s.remoteWhois(l, m)
	case "MOTD", "VERSION", "ADMIN", "TIME", "STATS":
		s.remoteQuery(l, m)
	case "SVSNOOP":
		s.remoteNoop(l, m)
	case "BEEADMIN":
		s.remoteAdmin(l, m)
	case "BEEOPER":
		s.remoteOper(l, m)
	case "BEEOPERR":
		s.operAnswered(l, m)
	case "BEEPLACE":
		s.remotePlace(l, m.Source, m.Params)
	case "SINFO", "SMOD", "GLOBOPS", "SENDUMODE", "SENDSNO", "SLOG", "SWHOIS", "BOTMOTD", "REPUTATION":
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
	s.linkBroadcast(l, fmt.Sprintf(":%s SID %s %d %s :%s", up.sid, name, r.hops+1, sid, desc))
	s.snotice("Server %s (%s) joined the network behind %s", name, sid, up.name)
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
	gone := s.splitOff(r.sid, s.links.servers[r.uplink].nameOr(r.uplink), r.name)
	s.linkBroadcast(l, fmt.Sprintf(":%s SQUIT %s :%s", r.uplink, r.name, reason))
	s.snotice("Server %s split from %s (%s)%s", r.name, s.links.servers[r.uplink].nameOr(r.uplink), reason, splitCount(gone))
}

func (r *remoteServer) nameOr(fallback string) string {
	if r == nil {
		return fallback
	}
	return r.name
}

func splitCount(n int) string {
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf(", %d servers in all", n)
}

// splitOff removes a server and everything behind it, with the users on
// them (quit reason "near far"); it returns how many servers went.
func (s *Server) splitOff(sid, near, far string) int {
	gone := map[string]bool{sid: true}
	for changed := true; changed; {
		changed = false
		for k, r := range s.links.servers {
			if !gone[k] && gone[r.uplink] {
				gone[k], changed = true, true
			}
		}
	}
	s.quitSplit(gone, near, far)
	for k := range gone {
		delete(s.links.servers, k)
	}
	return len(gone)
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
		s.snotice("Warning, link with %s: %s", l.peer.Name, strings.Join(why, "; "))
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
		s.snotice("Link with %s refused: %s", name, reason)
		return
	}
	behind := map[string]bool{}
	for sid, r := range s.links.servers {
		if r.via == l {
			behind[sid] = true
		}
	}
	s.quitSplit(behind, s.cfg.Server.Name, l.peer.Name)
	for sid := range behind {
		delete(s.links.servers, sid)
	}
	gone := len(behind)
	s.linkBroadcast(nil, fmt.Sprintf(":%s SQUIT %s :%s", s.cfg.Server.SID, l.peer.Name, reason))
	s.snotice("Link with %s lost after %s: %s%s", l.peer.Name, since(time.Since(l.since)), reason, splitCount(gone))
}

// orStr is a unless it is empty, else b.
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// since writes a duration the short way: 45s, 12m, 3h20m, 2d4h.
func since(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// linkTick handles handshake timeouts, pings and autoconnect.
func (s *Server) linkTick(now time.Time) {
	for l := range s.links.conns {
		switch {
		case !l.up && now.Sub(l.started) > linkHandshakeTimeout:
			s.dropLink(l, "handshake timed out")
		case l.up && l.pingSent.IsZero() && (now.Sub(l.lastRecv) > linkPingIdle ||
			// Lag: measured as soon as the link is synced, then every
			// linkPingIdle even when the link is busy, so it stays current.
			l.eos && (l.lastPong.IsZero() || now.Sub(l.lastPong) > linkPingIdle)):
			l.pingSent = time.Now()
			l.send("PING :" + s.cfg.Server.Name)
		case l.up && !l.pingSent.IsZero() && now.Sub(l.pingSent) > linkPingTimeout:
			s.dropLink(l, fmt.Sprintf("Ping timeout: %d seconds", int(now.Sub(l.lastRecv).Seconds())))
		}
	}
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
		r.dialing, busy = true, true
		r.next = now.Add(linkRetryMin)
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
	for key := range s.links.retry {
		if s.cfg.Link.Server(key) == nil {
			delete(s.links.retry, key)
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
		l.s.post(func() { l.s.handleLinkLine(l, line) })
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
