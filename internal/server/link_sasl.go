package server

// SASL over server links, in UnrealIRCd 6's own
// format (src/modules/sasl.c), so a BeeIRCd leaf can log people in with
// the services on a BeeIRCd hub or with Anope, and a BeeIRCd services
// server answers UnrealIRCd leaves.
//
// The services server says it takes SASL with
//   :<sid> MD client <its name> saslmechlist :PLAIN,EXTERNAL
// A server with services off sends its client's AUTHENTICATE there:
//   :<us> SASL <services> <uid> H <host> <ip> <P|S>     (once)
//   :<us> SASL <services> <uid> S <mech> [certfp]       (the mechanism)
//   :<us> SASL <services> <uid> C <base64>              (each line)
//   :<us> SASL <services> <uid> D A                     (abort)
// and services answer with C (a challenge), M (mechanisms), D S / D F,
// and SVSLOGIN <server> <uid> <account> before D S.

import (
	"bytes"
	"encoding/base64"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

// saslMechList is what our services accept.
const saslMechList = "PLAIN,EXTERNAL"

// saslWait is how long a client waits for services over a link.
const saslWait = 30 * time.Second

// saslSession is one login a services server runs for a client on
// another server.
type saslSession struct {
	from         *linkConn
	server       string // the client's server (name), where answers go
	host, ip     string
	mech, certfp string
	buf          strings.Builder
	busy         bool
	gone         bool
}

// saslAnnounce tells l that our services take SASL.
func (s *Server) saslAnnounce(l *linkConn) {
	if si, ok := s.servicesInfo(); ok {
		l.send(":" + si.SID + " MD client " + si.Name + " saslmechlist :" + saslMechList)
		l.send(":" + si.SID + " EOS")
	}
}

// linkMD reads the MD lines we use: which server takes SASL.
func (s *Server) linkMD(l *linkConn, m irc.Message) {
	if len(m.Params) < 3 || m.Params[0] != "client" {
		return
	}
	switch m.Params[2] {
	case "operclass", "bee-operflags", "bee-operlook":
		s.remoteOperMD(l, m)
		return
	case "saslmechlist":
	default:
		return
	}
	r := s.serverByName(m.Params[1])
	if r == nil {
		r = s.links.servers[m.Params[1]] // UnrealIRCd names it by SID in a burst
	}
	if r == nil || r.via != l {
		return
	}
	if s.cfg.Services.On() {
		// Services run here: this is the network's one SASL server. Another
		// server saying it is would get the services' rights (fromServices).
		s.ignoreOnce(l, "svs:saslmechlist", "refused saslmechlist from %s: services run on this server", r.name)
		return
	}
	if n := s.cfg.Link.SaslServer; n != "" && !strings.EqualFold(n, r.name) {
		s.ignoreOnce(l, "svs:saslmechlist", "refused saslmechlist from %s: [link] sasl_server is %s", r.name, n)
		return
	}
	mechs := ""
	if len(m.Params) > 3 {
		mechs = m.Params[3]
	}
	if mechs == "" {
		if strings.EqualFold(s.links.saslServer, r.name) {
			s.links.saslServer, s.links.saslMechs = "", ""
		}
		s.links.saslLast = saslGone{}
	} else if cur := s.serverByName(s.links.saslServer); cur != nil && cur != r {
		// One SASL server per network: a second one saying so would get
		// the services' rights over our users (fromServices).
		s.ignoreOnce(l, "svs:saslmechlist", "refused saslmechlist from %s: %s is the services server", r.name, cur.name)
		return
	} else {
		s.links.saslServer, s.links.saslMechs = r.name, mechs
		s.links.saslLast = saslGone{}
	}
	s.linkBroadcast(l, m.String())
}

// saslGone is the SASL server that left: its name and SID, the direct
// link it came in by, and its mechanisms.
type saslGone struct{ name, sid, peer, mechs string }

// saslServerGone forgets the SASL server when r leaves the network, so a
// server that comes in later under its name gets none of its rights. It
// is kept in saslLast for saslServerBack.
func (s *Server) saslServerGone(r *remoteServer) {
	if r != nil && strings.EqualFold(r.name, s.links.saslServer) {
		g := saslGone{name: r.name, sid: r.sid, mechs: s.links.saslMechs}
		if r.via != nil {
			g.peer = r.via.peer.Name
		}
		s.links.saslLast = g
		s.links.saslServer, s.links.saslMechs = "", ""
	}
}

// saslServerBack makes r the SASL server again when it is the one that
// left, back with the same SID through a link to the same server, and no
// other server took SASL meanwhile. InspIRCd does not send saslmechlist
// again after a relink, so without this logins fail until services say it.
func (s *Server) saslServerBack(r *remoteServer) {
	g := s.links.saslLast
	if r == nil || r.via == nil || s.links.saslServer != "" || s.cfg.Services.On() {
		return
	}
	if n := s.cfg.Link.SaslServer; n != "" {
		// Named in the config: it takes SASL as soon as it is linked, with
		// the mechanisms it last said, until it says new ones.
		if !strings.EqualFold(r.name, n) {
			return
		}
		mechs := saslMechList
		if strings.EqualFold(g.name, r.name) && g.mechs != "" {
			mechs = g.mechs
		}
		s.links.saslServer, s.links.saslMechs, s.links.saslLast = r.name, mechs, saslGone{}
		s.linkBroadcast(r.via, ":"+r.sid+" MD client "+r.name+" saslmechlist :"+mechs)
		return
	}
	if g.name == "" {
		return
	}
	if !strings.EqualFold(r.name, g.name) || r.sid != g.sid || g.peer == "" || !strings.EqualFold(r.via.peer.Name, g.peer) {
		return
	}
	s.links.saslServer, s.links.saslMechs, s.links.saslLast = r.name, g.mechs, saslGone{}
	s.linkBroadcast(r.via, ":"+r.sid+" MD client "+r.name+" saslmechlist :"+g.mechs)
}

// saslTarget is the server our clients log in with, "" when none is
// linked.
func (s *Server) saslTarget() string {
	if t := s.links.saslServer; t != "" && s.serverByName(t) != nil {
		return t
	}
	return ""
}

// saslRelay is AUTHENTICATE on a server with services off: the lines go
// to the services server and its answers come back in linkSASL.
func (s *Server) saslRelay(c *Client, arg string) {
	target := s.saslTarget()
	if target == "" {
		c.resetSASL()
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed: services are unavailable right now")
		return
	}
	o := s.serverByName(target).via
	send := func(mode, data string) {
		o.send(":" + s.cfg.Server.SID + " SASL " + target + " " + c.uid + " " + mode + " " + data)
	}
	if arg == "*" {
		if c.saslMech != "" {
			send("D", "A")
		}
		c.resetSASL()
		s.reply(c, irc.ErrSaslAborted, "SASL authentication aborted")
		return
	}
	if c.saslMech == "" {
		c.saslMech = strings.ToUpper(arg)
		secure := "P"
		if c.tls {
			secure = "S"
		}
		send("H", c.host+" "+irc.DisplayIP(c.ip)+" "+secure)
		mech := c.saslMech
		if c.certfp != "" {
			mech += " " + c.certfp
		}
		send("S", mech)
		c.saslSeq++
		seq := c.saslSeq
		time.AfterFunc(saslWait, func() {
			s.post(func() {
				if !c.closed && c.saslMech != "" && c.saslSeq == seq {
					send("D", "A")
					c.resetSASL()
					s.reply(c, irc.ErrSaslFail, "SASL authentication failed: services did not answer")
				}
			})
		})
		return
	}
	if len(arg) > saslChunk {
		send("D", "A")
		c.resetSASL()
		s.reply(c, irc.ErrSaslTooLong, "SASL message too long")
		return
	}
	send("C", arg)
}

// linkSASL is a SASL line from l: for our services, for one of our
// clients, or passing through.
func (s *Server) linkSASL(l *linkConn, m irc.Message) {
	if len(m.Params) < 4 {
		return
	}
	target, uid, mode, data := m.Params[0], m.Params[1], m.Params[2], m.Params[3]
	si, svcOK := s.servicesInfo()
	switch {
	case svcOK && (strings.EqualFold(target, si.Name) || target == si.SID),
		!svcOK && !s.svc.Off() && (strings.EqualFold(target, s.cfg.Server.Name) || target == s.cfg.Server.SID):
		s.saslServe(l, m.Source, uid, mode, data, m.Params[4:])
	case strings.EqualFold(target, s.cfg.Server.Name) || target == s.cfg.Server.SID || s.ours(uid):
		s.saslAnswer(uid, mode, data)
	default:
		if r := s.serverByName(target); r != nil && r.via != l {
			r.via.send(m.String())
		} else if r := s.links.servers[target]; r != nil && r.via != l {
			r.via.send(m.String())
		}
	}
}

// saslAnswer passes the services server's answer to our client.
func (s *Server) saslAnswer(uid, mode, data string) {
	c := s.clients[uid]
	if c == nil || c.closed || c.saslMech == "" {
		return
	}
	if s.authRelayAnswer(c, mode, data) {
		return
	}
	switch mode {
	case "C":
		c.send("AUTHENTICATE " + data)
	case "M":
		s.reply(c, irc.RplSaslMechs, data, "are available SASL mechanisms")
	case "D":
		c.resetSASL()
		switch data {
		case "S":
			s.reply(c, irc.RplSaslSuccess, "SASL authentication successful")
		case "A":
			s.reply(c, irc.ErrSaslAborted, "SASL authentication aborted")
		default:
			s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		}
		s.tryRegister(c)
	}
}

// remoteLogin is SVSLOGIN for someone not on the network yet: one of our
// clients logging in during connect, or one further along.
func (s *Server) remoteLogin(l *linkConn, line string, target, uid, account string) {
	if c := s.clients[uid]; c != nil && !c.registered {
		if account == "" {
			c.saslAccount = nil
			return
		}
		c.saslAccount = &store.Account{Name: account}
		mask := c.target() + "!" + orStr(c.ident, "*") + "@" + c.host
		s.reply(c, irc.RplLoggedIn, mask, account, "You are now logged in as "+account)
		return
	}
	if r := s.serverByName(target); r != nil && r.via != l {
		r.via.send(line)
	} else if r := s.links.servers[target]; r != nil && r.via != l {
		r.via.send(line)
	}
}

// dropSaslVia ends the logins our services run for clients behind l (it
// split); their server fails them at their next line or its timer.
func (s *Server) dropSaslVia(l *linkConn) {
	for uid, ss := range s.links.sasl {
		if ss.from == l {
			ss.gone = true
			delete(s.links.sasl, uid)
		}
	}
}

// saslServe runs a login for a client on another server (our services).
func (s *Server) saslServe(l *linkConn, from, uid, mode, data string, extra []string) {
	if s.links.sasl == nil {
		s.links.sasl = map[string]*saslSession{}
	}
	ss := s.links.sasl[uid]
	src := s.cfg.Server.SID
	if si, ok := s.servicesInfo(); ok {
		src = si.SID
	}
	server := from
	if r := s.links.servers[from]; r != nil {
		server = r.name
	}
	reply := func(mode, data string) { l.send(":" + src + " SASL " + server + " " + uid + " " + mode + " " + data) }
	done := func(ok bool) {
		if ss != nil {
			ss.gone = true
		}
		delete(s.links.sasl, uid)
		if ok {
			reply("D", "S")
		} else {
			reply("D", "F")
		}
	}
	switch mode {
	case "H":
		ss = &saslSession{from: l, server: server}
		if len(extra) > 0 {
			ss.host, ss.ip = data, extra[0]
		}
		s.links.sasl[uid] = ss
		return
	case "S":
		if ss == nil {
			ss = &saslSession{from: l, server: server}
			s.links.sasl[uid] = ss
		}
		ss.mech = strings.ToUpper(data)
		if len(extra) > 0 {
			ss.certfp = extra[0]
		}
		if ss.mech != "PLAIN" && ss.mech != "EXTERNAL" {
			reply("M", saslMechList)
			done(false)
			return
		}
		reply("C", "+")
		return
	case "D":
		if ss != nil {
			ss.gone = true
		}
		delete(s.links.sasl, uid)
		return
	case "C":
	default:
		return
	}
	if ss == nil || ss.mech == "" || ss.busy {
		done(false)
		return
	}
	if data != "+" {
		ss.buf.WriteString(data)
	}
	if ss.buf.Len() > saslMax {
		done(false)
		return
	}
	if len(data) == saslChunk {
		return // more is coming
	}
	raw, err := base64.StdEncoding.DecodeString(ss.buf.String())
	ss.buf.Reset()
	who := ss.host + " (" + ss.ip + ") on " + server
	login := func(a *store.Account) {
		if u := s.net.UserByUID(uid); u != nil {
			s.svc.Login(uid, a) // on the network already: a normal login
		} else {
			// They are not on the network yet: their server sends the
			// account in its UID line, which we take because of this.
			s.grantLogin(uid, a.Name, true)
			l.send(":" + src + " SVSLOGIN " + server + " " + uid + " " + a.Name)
		}
		done(true)
	}
	if ss.mech == "EXTERNAL" {
		authz := string(raw)
		a := s.store.AccountByCert(ss.certfp)
		switch {
		case err != nil || ss.certfp == "":
			done(false)
		case a == nil || authz != "" && !irc.EqualFold(authz, a.Name) || a.Suspended(s.svc.KeepNow()):
			s.svc.LogSASLExternal(who, "", ss.certfp, false)
			done(false)
		default:
			s.svc.LogSASLExternal(who, a.Name, ss.certfp, true)
			login(a)
		}
		return
	}
	parts := bytes.Split(raw, []byte{0})
	if err != nil || len(parts) != 3 || len(parts[1]) == 0 {
		done(false)
		return
	}
	authz, authc, pw := string(parts[0]), string(parts[1]), string(parts[2])
	if authz != "" && !irc.EqualFold(authz, authc) {
		done(false)
		return
	}
	a, err := s.store.AccountByNick(authc)
	if err == nil && a == nil && s.svc.OutsideCreates(authc) {
		s.svc.OutsideLogin(&ss.busy, func() bool { return ss.gone || l.closed }, authc, ss.ip, pw, func(made *store.Account, r services.PwResult) {
			if ss.gone || l.closed {
				return
			}
			if r == services.PwOK {
				login(made)
				return
			}
			done(false)
		})
		return
	}
	if err != nil || a == nil || a.Suspended(s.svc.KeepNow()) {
		done(false)
		return
	}
	s.svc.CheckLogin(&ss.busy, func() bool { return ss.gone || l.closed }, a, ss.ip, pw, func(r services.PwResult) {
		if ss.gone || l.closed {
			return
		}
		if r == services.PwOK {
			login(a)
			return
		}
		done(false)
	})
}
