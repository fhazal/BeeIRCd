package server

// Step 3l: servers place themselves on the network map. A server's place
// is its owner's [[map.place]] for it, else the city of its time zone
// (tzplace: Asia/Kuala_Lumpur is Kuala Lumpur; UTC says nothing). Nothing
// is looked up online. BeeIRCd servers tell each other their own place:
//
//	:<sid> BEEPLACE <server> <lat> <lon> <from> :<place name>
//
// from is "tz" (its time zone) or "owner" (its owner placed it). Sent to
// BeeIRCd links only, when a link has synced and when a place changes;
// passed on like other server lines. A [[map.place]] here always wins over
// what a server says about itself.

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/tzplace"
)

// heardPlace is what a server said about its own place.
type heardPlace struct {
	sid  string
	p    control.MapPlace
	from string // "tz" or "owner"
}

var localZone = sync.OnceValue(tzplace.Local)

// selfPlace is where this server says it is.
func (s *Server) selfPlace() (control.MapPlace, string, bool) {
	if p := s.cfg.Map.Place(s.cfg.Server.Name); p != nil {
		return control.MapPlace{Server: s.cfg.Server.Name, Name: p.Name, Lat: p.Lat, Lon: p.Lon}, "owner", true
	}
	zone := s.links.zone // set by tests; else the machine's
	if zone == "" {
		zone = localZone()
	}
	if tp, ok := tzplace.Lookup(zone); ok {
		return control.MapPlace{Server: s.cfg.Server.Name, Name: tp.Name, Lat: tp.Lat, Lon: tp.Lon}, "tz", true
	}
	return control.MapPlace{}, "", false
}

func placeLine(sid string, p control.MapPlace, from string) string {
	f := func(v float64) string { return strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64) }
	return ":" + sid + " BEEPLACE " + p.Server + " " + f(p.Lat) + " " + f(p.Lon) + " " + from + " :" + p.Name
}

// sendPlaces gives a BeeIRCd link that just synced our place and the ones
// we heard from servers not behind it.
func (s *Server) sendPlaces(l *linkConn) {
	if !l.peer.Bee {
		return
	}
	if p, from, ok := s.selfPlace(); ok {
		l.send(placeLine(s.cfg.Server.SID, p, from))
	}
	for _, h := range s.links.places {
		if r := s.links.servers[h.sid]; r != nil && r.via != l {
			l.send(placeLine(h.sid, h.p, h.from))
		}
	}
}

// announcePlace tells every BeeIRCd link where we are now.
func (s *Server) announcePlace() {
	p, from, ok := s.selfPlace()
	if !ok {
		return
	}
	line := placeLine(s.cfg.Server.SID, p, from)
	for o := range s.links.conns {
		if o.up && !o.closed && o.eos && o.peer.Bee {
			o.send(line)
		}
	}
}

// remotePlace is BEEPLACE from a link: a server behind it says where it is.
func (s *Server) remotePlace(l *linkConn, source string, params []string) {
	if len(params) < 5 {
		return
	}
	src := s.sourceServer(l, source)
	if src == nil || !strings.EqualFold(src.name, params[0]) {
		return // a server only places itself
	}
	lat, e1 := strconv.ParseFloat(params[1], 64)
	lon, e2 := strconv.ParseFloat(params[2], 64)
	from, name := params[3], strings.Join(strings.Fields(params[4]), " ")
	if e1 != nil || e2 != nil || math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 ||
		(from != "tz" && from != "owner") || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 60 {
		return
	}
	if s.links.places == nil {
		s.links.places = map[string]heardPlace{}
	}
	s.links.places[strings.ToLower(src.name)] = heardPlace{sid: src.sid, from: from,
		p: control.MapPlace{Server: src.name, Name: name, Lat: lat, Lon: lon}}
	line := placeLine(src.sid, s.links.places[strings.ToLower(src.name)].p, from)
	for o := range s.links.conns {
		if o != l && o.up && !o.closed && o.eos && o.peer.Bee {
			o.send(line)
		}
	}
}

// mapPlaces is every place the map can show: our owner's [[map.place]]
// first, then what servers said about themselves, then our own time zone.
func (s *Server) mapPlaces() []control.MapPlace {
	out := make([]control.MapPlace, 0, len(s.cfg.Map.Places)+len(s.links.places)+1)
	have := map[string]bool{}
	for _, p := range s.cfg.Map.Places {
		out = append(out, control.MapPlace{Server: p.Server, Name: p.Name, Lat: p.Lat, Lon: p.Lon})
		have[strings.ToLower(p.Server)] = true
	}
	for k, h := range s.links.places {
		if !have[k] {
			p := h.p
			p.From = "its " + map[string]string{"tz": "time zone", "owner": "owner"}[h.from]
			out = append(out, p)
			have[k] = true
		}
	}
	if !have[strings.ToLower(s.cfg.Server.Name)] {
		if p, from, ok := s.selfPlace(); ok && from == "tz" {
			p.From = "its time zone"
			out = append(out, p)
		}
	}
	return out
}
