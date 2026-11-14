package server

// Session exceptions over BeeIRCd links. They live with the services, so
// a server whose services are off (a leaf) knew none and held everyone to
// the default session_limit (found when SKINTERNET moved over: the leaf
// refused bouncers the hub let in). The services server sends its whole
// list, when a link has synced and whenever it changes:
//
//	:<sid> BEESESS START
//	:<sid> BEESESS ADD <mask> <limit> <expires unix|0> :<reason>
//	:<sid> BEESESS END
//
// A server takes the list at END, in place of the one before, and passes
// it on to its other BeeIRCd links. Only the services server may send it.

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// exceptLines is the list as BEESESS lines from sid.
func exceptLines(sid string, ex []store.SessionExcept) []string {
	out := []string{":" + sid + " BEESESS START"}
	for _, e := range ex {
		exp := int64(0)
		if !e.Expires.IsZero() {
			exp = e.Expires.Unix()
		}
		out = append(out, ":"+sid+" BEESESS ADD "+e.Mask+" "+strconv.Itoa(e.Max)+" "+strconv.FormatInt(exp, 10)+" :"+irc.Truncate(e.Reason, 200))
	}
	return append(out, ":"+sid+" BEESESS END")
}

// exceptsKey sums up a list, to send it only when it changed.
func exceptsKey(ex []store.SessionExcept) string {
	var b strings.Builder
	for _, e := range ex {
		b.WriteString(e.Mask + " " + strconv.Itoa(e.Max) + " " + strconv.FormatInt(e.Expires.Unix(), 10) + "\n")
	}
	return b.String()
}

// sendExcepts gives a BeeIRCd link that just synced the session
// exceptions: ours when we run the services, else the ones we were sent
// (unless they came from that link).
func (s *Server) sendExcepts(l *linkConn) {
	if !l.peer.Bee {
		return
	}
	if !s.svc.Off() {
		for _, line := range exceptLines(s.cfg.Server.SID, s.svc.SessionExcepts()) {
			l.send(line)
		}
		return
	}
	if from := s.links.exceptsFrom; from != "" {
		if r := s.links.servers[from]; r != nil && r.via != l {
			for _, line := range exceptLines(from, s.svc.LinkedExcepts()) {
				l.send(line)
			}
		}
	}
}

// syncExcepts sends our session exceptions to every BeeIRCd link when
// they changed (runs after services reload them).
func (s *Server) syncExcepts() {
	if s.svc.Off() {
		return
	}
	ex := s.svc.SessionExcepts()
	key := exceptsKey(ex)
	if key == s.links.exceptsSent {
		return
	}
	s.links.exceptsSent = key
	lines := exceptLines(s.cfg.Server.SID, ex)
	for o := range s.links.conns {
		if o.up && !o.closed && o.eos && o.peer.Bee {
			for _, line := range lines {
				o.send(line)
			}
		}
	}
}

// remoteExcepts is BEESESS from l (the link guard lets only the services
// server send it).
func (s *Server) remoteExcepts(l *linkConn, m irc.Message) {
	src := s.sourceServer(l, m.Source)
	if src == nil || len(m.Params) == 0 || !s.svc.Off() {
		return
	}
	switch strings.ToUpper(m.Params[0]) {
	case "START":
		l.exceptsIn = []store.SessionExcept{}
	case "ADD":
		if l.exceptsIn == nil || len(m.Params) < 4 || len(l.exceptsIn) >= 10000 {
			return
		}
		mask := strings.ToLower(m.Params[1])
		n, e1 := strconv.Atoi(m.Params[2])
		exp, e2 := strconv.ParseInt(m.Params[3], 10, 64)
		if e1 != nil || e2 != nil || n < 0 || mask == "" || strings.ContainsAny(mask, "@! ") {
			return
		}
		e := store.SessionExcept{Mask: mask, Max: n, SetBy: src.name}
		if exp > 0 {
			e.Expires = time.Unix(exp, 0)
		}
		if len(m.Params) > 4 {
			e.Reason = m.Params[4]
		}
		l.exceptsIn = append(l.exceptsIn, e)
	case "END":
		if l.exceptsIn == nil {
			return
		}
		ex := l.exceptsIn
		l.exceptsIn = nil
		s.svc.SetLinkedExcepts(ex)
		s.links.exceptsFrom = src.sid
		lines := exceptLines(src.sid, ex)
		for o := range s.links.conns {
			if o != l && o.up && !o.closed && o.eos && o.peer.Bee {
				for _, line := range lines {
					o.send(line)
				}
			}
		}
	}
}
