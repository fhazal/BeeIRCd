package server

// Step 3k: the Hive Panel's network map. The owner (any oper with flag c,
// like the rest of Linked servers) puts each server where it is; the
// places are [[map.place]] blocks in beeircd.conf. Nothing is looked up
// from an address and no person is ever placed.

import (
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
)

// mapServerKnown reports whether name is a server the map can show: us,
// one linked now, one with a link block, or one placed before (it may be
// split off just now).
func (s *Server) mapServerKnown(name string) (string, bool) {
	if strings.EqualFold(name, s.cfg.Server.Name) {
		return s.cfg.Server.Name, true
	}
	for _, r := range s.links.servers {
		if strings.EqualFold(r.name, name) {
			return r.name, true
		}
	}
	if b := s.cfg.Link.Server(name); b != nil {
		return b.Name, true
	}
	if p := s.cfg.Map.Place(name); p != nil {
		return p.Server, true
	}
	return "", false
}

func (s *Server) panelMapPlace(po *control.PanelOper, ip string, req control.Request) control.Response {
	fail := func(t string) control.Response { return control.Response{Error: t} }
	if s.cfg.Path == "" {
		return fail("This server wasn't started from a config file, so places can't be saved.")
	}
	name, ok := s.mapServerKnown(strings.TrimSpace(req.Name))
	if !ok {
		return fail(strings.TrimSpace(req.Name) + " isn't a server of this network.")
	}
	if req.Lat == nil || req.Lon == nil {
		if s.cfg.Map.Place(name) == nil {
			return fail(name + " isn't on the map.")
		}
		if _, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
			d.DelBlock("map.place", "server", s.cfg.Map.Place(name).Server)
			return nil
		}); err != nil {
			return fail("Not saved: " + err.Error())
		}
		kept := s.cfg.Map.Places[:0:0]
		for _, p := range s.cfg.Map.Places {
			if !strings.EqualFold(p.Server, name) {
				kept = append(kept, p)
			}
		}
		s.cfg.Map.Places = kept
		s.panelEvent(po.Account, ip, "took "+name+" off the map")
		return control.Response{Done: &control.Done{Text: name + " is off the map."}}
	}
	p := config.MapPlace{Server: name, Name: strings.Join(strings.Fields(req.Value), " "), Lat: *req.Lat, Lon: *req.Lon}
	if err := p.Check(); err != nil {
		return fail(upperFirst(strings.TrimPrefix(err.Error(), "[[map.place]] "+name+": ")) + ".")
	}
	old := s.cfg.Map.Place(name)
	if old == nil && len(s.cfg.Map.Places) >= config.MaxMapPlaces {
		return fail("The map holds 1000 places at most.")
	}
	if old != nil {
		p.Server = old.Server
	}
	if _, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		d.SetBlock("map.place", p.KV())
		return nil
	}); err != nil {
		s.log.Printf("Hive Panel map place: %v", err)
		return fail("Not saved: " + err.Error())
	}
	// Round as saved, so the map shows what the file holds.
	if fresh, err := config.Load(s.cfg.Path); err == nil {
		if q := fresh.Map.Place(name); q != nil {
			p = *q
		}
	}
	if old != nil {
		*old = p
	} else {
		s.cfg.Map.Places = append(s.cfg.Map.Places, p)
	}
	where := p.Name
	if where == "" {
		where = "the map"
	}
	s.panelEvent(po.Account, ip, "put "+name+" on the map ("+where+")")
	return control.Response{Done: &control.Done{Text: name + " is on the map: " + where + "."}}
}
