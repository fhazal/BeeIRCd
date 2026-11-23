package server

import (
	"github.com/fhazal/beeircd/internal/link"
)

// READONLY (OperServ SET READONLY) is services state, but every server
// must refuse what it would store: a server without services learns it
// through BEE READONLY <on|off> from the server that runs them (sent on a
// change and in the burst, passed on like other BEE lines).

// readOnly reports whether services are read-only: ours, or the one the
// servers behind a link run when ours are off.
func (s *Server) readOnly() bool {
	if !s.svc.Off() {
		return s.svc.ReadOnly()
	}
	for _, on := range s.roBy {
		if on {
			return true
		}
	}
	return false
}

// ReadOnlyChanged is called by services after READONLY was switched.
func (h host) ReadOnlyChanged(on bool) {
	s := h.s
	s.linkReadOnly(nil, s.cfg.Server.SID, on)
}

// linkReadOnly passes the READONLY state of server sid to the BeeIRCd
// servers (all but from).
func (s *Server) linkReadOnly(from *linkConn, sid string, on bool) {
	it := readOnlyBee(sid, on)
	for _, o := range s.upLinks(from) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
}

func readOnlyBee(sid string, on bool) *link.Bee {
	v := "off"
	if on {
		v = "on"
	}
	return &link.Bee{Source: sid, Sub: "READONLY", Args: []string{v}}
}

// remoteReadOnly is BEE READONLY <on|off> from the server that runs services.
func (s *Server) remoteReadOnly(l *linkConn, it *link.Bee) {
	if len(it.Args) < 1 || (it.Args[0] != "on" && it.Args[0] != "off") || s.links.servers[it.Source] == nil {
		return
	}
	on := it.Args[0] == "on"
	if s.roBy[it.Source] == on {
		return
	}
	if s.roBy == nil {
		s.roBy = map[string]bool{}
	}
	if on {
		s.roBy[it.Source] = true
	} else {
		delete(s.roBy, it.Source)
	}
	s.linkReadOnly(l, it.Source, on)
}

// readOnlyBurst is the READONLY lines of a burst: ours, and those of
// servers behind other links.
func (s *Server) readOnlyBurst(l *linkConn) []string {
	cd := l.codecOf()
	var out []string
	if !s.svc.Off() && s.svc.ReadOnly() {
		out = append(out, cd.Encode(readOnlyBee(s.cfg.Server.SID, true))...)
	}
	for sid, on := range s.roBy {
		if r := s.links.servers[sid]; on && r != nil && !s.behind(l, sid) {
			out = append(out, cd.Encode(readOnlyBee(sid, true))...)
		}
	}
	return out
}
