package server

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// The unrealircd.org/issued-by tag, as UnrealIRCd's issued-by-tag module:
// a line caused by an oper command, an RPC call or services carries who is
// behind it, for IRC operators with message-tags only. The values are
// UnrealIRCd's: OPER:nick@server[:oper name], RPC:user@server[:issuer],
// SERVICES:nick@server or SERVICES:server. s.issuer holds it while the
// command, call or services action runs; s.out copies it into each
// message. Lines from a server link that carry it pass it on.

const issuedByTag = "unrealircd.org/issued-by"

// operIssuer is the tag value for oper c's command.
func (s *Server) operIssuer(c *Client) string {
	v := "OPER:" + c.user.Nick + "@" + s.cfg.Server.Name
	if c.user.OperName != "" {
		v += ":" + c.user.OperName
	}
	return v
}

// rpcIssuer is the tag value for a call from rc.
func (s *Server) rpcIssuer(rc *rpcConn) string {
	v := "RPC:" + rc.name + "@" + s.cfg.Server.Name
	if rc.issuer != "" {
		v += ":" + rc.issuer
	}
	return v
}

// servicesName is the server the services bots are on.
func (s *Server) servicesName() string {
	if name, _, ok := s.cfg.ServicesServer(); ok {
		return name
	}
	return s.cfg.Server.Name
}

// servicesIssuerOf is the tag value for services acting as who (a bot's
// nick or UID; another or "" names the services server).
func (s *Server) servicesIssuerOf(who string) string {
	if u := s.userByUIDOrNick(who); who != "" && u != nil && u.Service {
		return "SERVICES:" + u.Nick + "@" + orStr(u.Server, s.servicesName())
	}
	return "SERVICES:" + s.servicesName()
}

// servicesDid marks what services do now as theirs (when nothing else is
// behind it already); call the result when done.
func (s *Server) servicesDid(who string) func() {
	if s.issuer != "" {
		return func() {}
	}
	s.issuer = s.servicesIssuerOf(who)
	return func() { s.issuer = "" }
}

// servicesIssuer is the tag value for an event services emit, "" for one
// that isn't an action on someone (a bot joining, a login, ...).
func (s *Server) servicesIssuer(ev state.Event) string {
	var src, about string
	switch e := ev.(type) {
	case *state.Kick:
		src = e.Source
	case *state.ChannelMode:
		src = e.Source
	case *state.UserMode:
		src = e.Source
	case *state.Topic:
		src = e.Source
	case *state.Invite:
		src = e.Source
	case *state.Join:
		about = e.UID
	case *state.Part:
		about = e.UID
	case *state.NickChange:
		about = e.UID
	case *state.UserQuit:
		about = e.UID
	case *state.HostChange:
		about = e.UID
	default:
		return ""
	}
	if about != "" {
		if u := s.net.UserByUID(about); u == nil || u.Service {
			return "" // a bot of its own
		}
	}
	return s.servicesIssuerOf(src)
}

// issuedTags are the tags of a line for c alone: the issued-by tag when c
// is an oper with message-tags and something is behind it.
func (s *Server) issuedTags(c *Client) map[string]string {
	if s.issuer == "" || !c.isOper() || !c.caps["message-tags"] {
		return nil
	}
	return map[string]string{issuedByTag: s.issuer}
}

// issuedLine adds the issued-by tag to a line for link o. InspIRCd links
// (through the bridge) don't get it.
func (s *Server) issuedLine(o *linkConn, line string) string {
	if s.issuer == "" || o.bridge != nil || strings.Contains(line, issuedByTag+"=") {
		return line
	}
	tag := issuedByTag + "=" + irc.EscapeTag(s.issuer)
	if strings.HasPrefix(line, "@") {
		return "@" + tag + ";" + line[1:]
	}
	return "@" + tag + " " + line
}
