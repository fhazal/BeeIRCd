package server

// Stage 2g link guard: every line from a link passes here first. A linked
// server may only speak for itself, the servers behind it and their users
// (found by the rogue-server tests: a leaf could send messages, topics,
// kicks and modes as a user on the hub). Services commands (SVS*, a
// server's CHGHOST/CHGIDENT/CHGNAME) only come from the services server
// or the server it hangs off; a user's CHG*/SA* only from an IRC operator.

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// servicesOnly are commands only services send.
var servicesOnly = map[string]bool{
	"SVSLOGIN": true, "SVSKILL": true, "SVSJOIN": true, "SVSPART": true,
	"SVSNICK": true, "SVSMODE": true, "SVS2MODE": true, "SVSNOOP": true,
}

// operOnly are commands a user sends only as an IRC operator.
var operOnly = map[string]bool{
	"CHGHOST": true, "CHGIDENT": true, "CHGNAME": true, "SAJOIN": true, "SAPART": true,
}

// linkSource finds who sent m on l: a server behind l, or a user of one
// (by UID or nick). ok false: nobody l may speak for.
func (s *Server) linkSource(l *linkConn, src string) (srv *remoteServer, u *state.User, ok bool) {
	if src == "" {
		r := s.links.servers[l.peer.SID]
		return r, nil, r != nil
	}
	if strings.EqualFold(src, s.cfg.Server.Name) || src == s.cfg.Server.SID {
		return nil, nil, false
	}
	if isUID(src) {
		u = s.net.UserByUID(src)
	} else if r := s.sourceServer(l, src); r != nil {
		return r, nil, true
	} else if !strings.Contains(src, ".") {
		u = s.net.UserByNick(src)
	}
	if u == nil || !s.behind(l, uidServer(u.UID)) {
		return nil, nil, false
	}
	return s.links.servers[uidServer(u.UID)], u, true
}

// fromServices says whether srv is the network's services server or the
// server it hangs off (BeeIRCd sends services changes from its own SID).
func (s *Server) fromServices(srv *remoteServer) bool {
	t := s.links.saslServer
	if t == "" || srv == nil {
		return false
	}
	svc := s.serverByName(t)
	return svc != nil && (srv == svc || srv.sid == svc.uplink)
}

// linkGate says whether to run m from l; a refused line is logged once
// per link and kind.
func (s *Server) linkGate(l *linkConn, m irc.Message) bool {
	switch m.Command {
	case "PING", "PONG", "ERROR":
		return true // the link itself
	}
	srv, u, ok := s.linkSource(l, m.Source)
	if !ok && m.Command == "BEEADMIN" && isUID(m.Source) && s.ours(m.Source) && s.fromServices(s.links.servers[l.peer.SID]) {
		return true // our own admin's AdminServ command, sent round through the services server
	}
	if !ok {
		if isUID(m.Source) && s.net.UserByUID(m.Source) == nil && !s.ours(m.Source) {
			return false // a user that just left (a KILL crossing): drop quietly
		}
		s.ignoreOnce(l, "spoof:"+m.Command, "refused %s from %s: not a server or user behind this link", m.Command, m.Source)
		return false
	}
	if servicesOnly[m.Command] && !s.fromServices(srv) {
		// A clash rename to the user's UID is how BeeIRCd settles a nick
		// collision; any server may send that one.
		if m.Command == "SVSNICK" && len(m.Params) >= 2 && m.Params[0] == m.Params[1] {
			return true
		}
		s.ignoreOnce(l, "svs:"+m.Command, "refused %s from %s: only services may send it", m.Command, m.Source)
		return false
	}
	// A server may KILL (collisions, server kills); a user only as an IRC
	// operator or a services bot.
	if m.Command == "KILL" && u != nil && !u.Modes['o'] && !u.Service && !s.fromServices(srv) {
		s.ignoreOnce(l, "oper:KILL", "refused KILL from %s: not an IRC operator", u.Nick)
		return false
	}
	if operOnly[m.Command] {
		switch {
		case u == nil && !s.fromServices(srv):
			s.ignoreOnce(l, "svs:"+m.Command, "refused %s from %s: only services may send it as a server", m.Command, m.Source)
			return false
		case u != nil && !u.Modes['o']:
			s.ignoreOnce(l, "oper:"+m.Command, "refused %s from %s: not an IRC operator", m.Command, u.Nick)
			return false
		}
	}
	return true
}
