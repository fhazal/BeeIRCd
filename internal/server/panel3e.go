package server

// Step 3e: the Hive Panel's Linked servers page: the network map, every
// link with its state, Connect and Split (like /CONNECT and /SQUIT), and
// the [[link.server]] blocks, changed in beeircd.conf and applied with a
// rehash. Flag c, like /CONNECT and /SQUIT.

import (
	"sort"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
)

func (s *Server) panelLinks() *control.Links {
	out := &control.Links{Role: s.cfg.Link.Role, Self: s.cfg.Server.Name, Status: s.linkStatus()}
	if s.cfg.Link.Linked() {
		out.Fingerprint, out.LinkPort = s.links.fp, s.cfg.Link.Port
	}
	users := map[string]int{}
	for _, u := range s.net.Users() {
		if !u.Service {
			users[strings.ToLower(u.Server)]++
		}
	}
	lag := map[string]float64{}
	for _, st := range out.Status {
		lag[strings.ToLower(st.Name)] = st.LagMS
	}
	out.Map = append(out.Map, control.MapServer{Name: s.cfg.Server.Name, SID: s.cfg.Server.SID, Self: true,
		Users: users[strings.ToLower(s.cfg.Server.Name)], Desc: s.cfg.Server.Description})
	var rest []control.MapServer
	for _, r := range s.links.servers {
		up := ""
		if r.uplink != s.cfg.Server.SID {
			up = s.links.servers[r.uplink].nameOr(r.uplink)
		}
		rest = append(rest, control.MapServer{Name: r.name, SID: r.sid, Uplink: up, Users: users[strings.ToLower(r.name)],
			LagMS: lag[strings.ToLower(r.name)], Desc: r.desc})
	}
	sort.Slice(rest, func(i, j int) bool { return irc.Fold(rest[i].Name) < irc.Fold(rest[j].Name) })
	out.Map = append(out.Map, rest...)
	out.Places = s.mapPlaces()
	out.Blocks = []control.LinkBlock{}
	for _, b := range s.cfg.Link.Servers {
		out.Blocks = append(out.Blocks, control.LinkBlock{Name: b.Name, Host: b.Host, Port: b.Port,
			Fingerprint: b.Fingerprint, Autoconnect: b.Autoconnect, HasPassword: b.Password != ""})
	}
	return out
}

func (s *Server) panelLinkConnect(po *control.PanelOper, ip, name string) control.Response {
	text := s.linkNowBy(po.Account+" (Hive Panel)", name)
	if !strings.HasPrefix(text, "Connecting to ") {
		return control.Response{Error: text}
	}
	s.panelEvent(po.Account, ip, "connected "+name)
	return control.Response{Done: &control.Done{Text: text}}
}

func (s *Server) panelLinkSplit(po *control.PanelOper, ip, name, reason string) control.Response {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		reason = "Split by " + po.Account
	}
	text, ok := s.squitBy(po.Account+" (Hive Panel)", s.cfg.Server.SID, name, reason)
	if !ok {
		return control.Response{Error: name + " isn't linked."}
	}
	if text == "" {
		text = name + " is split off."
	}
	s.panelEvent(po.Account, ip, "split "+name+" ("+reason+")")
	return control.Response{Done: &control.Done{Text: text}}
}

// panelLinkSave adds or changes a [[link.server]] block and rehashes.
func (s *Server) panelLinkSave(po *control.PanelOper, ip string, b *control.LinkBlock, isNew bool) control.Response {
	fail := func(t string) control.Response { return control.Response{Error: t} }
	switch {
	case b == nil:
		return fail("Bad request.")
	case !s.cfg.Link.Linked():
		return fail("This server doesn't link: set [link] role = hub or leaf first (beeircd install does it).")
	case s.cfg.Path == "":
		return fail("This server wasn't started from a config file, so link blocks can't be saved.")
	}
	name := strings.TrimSpace(b.Name)
	old := s.cfg.Link.Server(name)
	switch {
	case isNew && old != nil:
		return fail("There is a link block for " + old.Name + " already. Open it to change it.")
	case !isNew && old == nil:
		return fail("There is no link block for " + name + " (any more).")
	}
	ls := config.LinkServer{Name: name, Host: strings.TrimSpace(b.Host), Port: b.Port,
		Fingerprint: strings.TrimSpace(b.Fingerprint), Autoconnect: b.Autoconnect, SendQMB: 20}
	if old != nil {
		ls.Name, ls.SendQMB, ls.Password = old.Name, old.SendQMB, old.Password
	}
	if ls.Host == "" {
		ls.Port = 0
	} else if ls.Port == 0 {
		ls.Port = config.LinkPort
	}
	if b.Password != "" {
		ls.Password = b.Password
	} else if b.NoPassword {
		ls.Password = ""
	}
	if err := ls.Check(s.cfg.Server.Name); err != nil {
		return fail(upperFirst(err.Error()) + ".")
	}
	kv := []config.KV{{Key: "name", Value: config.Quote(ls.Name)}}
	if ls.Host != "" {
		kv = append(kv, config.KV{Key: "host", Value: config.Quote(ls.Host)}, config.KV{Key: "port", Value: strconv.Itoa(ls.Port)})
	}
	kv = append(kv, config.KV{Key: "fingerprint", Value: config.Quote(ls.Fingerprint)})
	if ls.Password != "" {
		kv = append(kv, config.KV{Key: "password", Value: config.Quote(ls.Password)})
	}
	kv = append(kv, config.KV{Key: "autoconnect", Value: strconv.FormatBool(ls.Autoconnect)})
	if ls.SendQMB != 20 {
		kv = append(kv, config.KV{Key: "sendq_mb", Value: strconv.Itoa(ls.SendQMB)})
	}
	if _, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		d.SetBlock("link.server", kv)
		return nil
	}); err != nil {
		s.log.Printf("Hive Panel link block: %v", err)
		return fail("Not saved: " + err.Error())
	}
	if _, err := s.rehash("the Hive Panel (" + po.Account + ")"); err != nil {
		return fail("Saved, but the rehash failed: " + err.Error())
	}
	what := "changed the link block for " + ls.Name
	if old == nil {
		what = "added a link block for " + ls.Name
	}
	s.panelEvent(po.Account, ip, what)
	s.snotice("Hive Panel: %s %s", po.Account, what)
	text := "Saved and applied."
	if ls.Autoconnect && !s.linkKnown(ls.Name) {
		text += " It connects by itself."
	}
	return control.Response{Done: &control.Done{Text: text}}
}

// panelLinkDel removes a [[link.server]] block (the link goes with it).
func (s *Server) panelLinkDel(po *control.PanelOper, ip, name string) control.Response {
	old := s.cfg.Link.Server(strings.TrimSpace(name))
	if old == nil || s.cfg.Path == "" {
		return control.Response{Error: "There is no link block for " + name + " (any more)."}
	}
	n := old.Name
	if _, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		d.DelBlock("link.server", "name", n)
		return nil
	}); err != nil {
		return control.Response{Error: "Not saved: " + err.Error()}
	}
	if _, err := s.rehash("the Hive Panel (" + po.Account + ")"); err != nil {
		return control.Response{Error: "Saved, but the rehash failed: " + err.Error()}
	}
	s.panelEvent(po.Account, ip, "removed the link block for "+n)
	s.snotice("Hive Panel: %s removed the link block for %s", po.Account, n)
	return control.Response{Done: &control.Done{Text: "The link block for " + n + " is removed."}}
}

func upperFirst(t string) string {
	if t == "" {
		return t
	}
	return strings.ToUpper(t[:1]) + t[1:]
}
