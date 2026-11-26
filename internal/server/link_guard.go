package server

// Every line from a link passes here first. A linked
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
	"BEESESS": true,
}

// operOnly are commands a user sends only as an IRC operator.
var operOnly = map[string]bool{
	"CHGHOST": true, "CHGIDENT": true, "CHGNAME": true, "SAJOIN": true, "SAPART": true,
	"SANICK": true, "SAQUIT": true, "NICKLOCK": true, "NICKUNLOCK": true, "TSCTL": true, "SDESC": true,
	"SAMODE": true,
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
		// InspIRCd sends a user's account (a SASL login done before it
		// connected) as METADATA after the UID: the same trust as the
		// account in an UnrealIRCd UID line, so its own server may.
		if m.Command == "SVSLOGIN" && l.bridge != nil && srv != nil && len(m.Params) >= 2 && uidServer(m.Params[1]) == srv.sid {
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
	// MODE on a user: services change someone on another server with
	// SVS2MODE; a plain MODE may only touch the users behind this link (it
	// could take the cloak off ours with -x, or mark them +r).
	if m.Command == "MODE" && len(m.Params) >= 2 && !strings.HasPrefix(m.Params[0], "#") && !s.fromServices(srv) {
		if t := s.userByUIDOrNick(m.Params[0]); t != nil && s.linkOf(t.UID) != l {
			s.ignoreOnce(l, "svs:MODE", "refused MODE on %s from %s: only services change a user on another server", t.Nick, m.Source)
			return false
		}
	}
	if operOnly[m.Command] {
		switch {
		case u == nil && !s.fromServices(srv) && m.Command != "SDESC":
			// A server may give its own description (remoteSdesc).
			s.ignoreOnce(l, "svs:"+m.Command, "refused %s from %s: only services may send it as a server", m.Command, m.Source)
			return false
		case u != nil && !u.Modes['o']:
			s.ignoreOnce(l, "oper:"+m.Command, "refused %s from %s: not an IRC operator", m.Command, u.Nick)
			return false
		}
	}
	return true
}

// maxLinkHostLen is the longest host a linked server may give a user.
const maxLinkHostLen = 64

// validLinkHost says whether a host from a linked server is safe to show:
// letters, digits, '.', '-', ':' (IPv6), '_' and '/' (InspIRCd vhosts), at
// most 64, not starting with ':' (it would end a protocol line's params).
func validLinkHost(h string) bool {
	if h == "" || len(h) > maxLinkHostLen || h[0] == ':' {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(".-:_/", c) >= 0) {
			return false
		}
	}
	return true
}

// validLinkIdent says whether an ident from a linked server is safe: at
// most maxIdentLen printable ASCII, without '@', '!', '*' or a leading ':'.
// Looser than SETIDENT's own rule, since other servers allow more.
func validLinkIdent(id string) bool {
	if id == "" || len(id) > maxIdentLen || id[0] == ':' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c <= ' ' || c >= 0x7f || c == '@' || c == '!' || c == '*' {
			return false
		}
	}
	return true
}
