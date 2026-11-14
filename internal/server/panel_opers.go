package server

// The Hive Panel's Opers page. The same oper entries and the same
// rules as AdminServ OPER (flag a): an oper only gives flags they have,
// only takes away flags they have, removes only opers whose flags they all
// have; the owner ([services] admin) is set in beeircd.conf and locked.
// One more rule here: nobody removes themselves or takes their own flag a
// (they would lock themselves out of the panel). Every change goes into
// AdminServ HISTORY, so the toast's Undo (and AS UNDO) can put it back.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

func (s *Server) operOnline() map[int64]bool {
	on := map[int64]bool{}
	for _, c := range s.clients {
		if c.isOper() {
			on[c.operAccount] = true
		}
	}
	for _, g := range s.links.remoteOpers {
		on[g.id] = true
	}
	return on
}

func (s *Server) panelOpers() (*control.Opers, string) {
	list, err := s.store.Opers()
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, "Something went wrong on the server."
	}
	on := s.operOnline()
	out := &control.Opers{}
	if admin := s.cfg.Services.Admin; admin != "" {
		a, _ := s.store.AccountByNick(admin)
		row := control.OperRow{Account: admin, Class: "netadmin", All: allFlags, Hosts: s.adminHosts(), Owner: true,
			Password: s.cfg.Services.AdminPassword != ""}
		if a != nil {
			row.Account, row.Online = a.Name, on[a.ID]
		}
		out.Rows = append(out.Rows, row)
	}
	sort.Slice(list, func(i, j int) bool { return irc.Fold(list[i].Account) < irc.Fold(list[j].Account) })
	for i := range list {
		o := &list[i]
		v := s.viewOf(o)
		out.Rows = append(out.Rows, control.OperRow{Account: o.Account, Class: v.class, Flags: o.Flags, All: v.flags,
			Hosts: o.Hosts, Title: v.title, Online: on[o.AccountID], Password: o.PassHash != "", Disabled: o.Disabled,
			AddedBy: o.AddedBy, AddedAt: o.AddedAt})
	}
	for _, oc := range s.cfg.Classes() {
		out.Classes = append(out.Classes, control.OperClass{Name: oc.Name, Flags: oc.Flags})
	}
	return out, ""
}

// lacking is the letters of want that have doesn't hold.
func lacking(have, want string) string {
	var b strings.Builder
	for i := 0; i < len(want); i++ {
		if strings.IndexByte(have, want[i]) < 0 {
			b.WriteByte(want[i])
		}
	}
	return b.String()
}

// flagWords is "flag k" or "flags g and k".
func flagWords(l string) string {
	if len(l) == 1 {
		return "flag " + l
	}
	parts := strings.Split(l, "")
	return "flags " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// panelOperTarget finds the entry req.Name names.
func (s *Server) panelOperTarget(name string) (*store.Account, *store.Oper, string) {
	a, err := s.store.AccountByNick(strings.TrimSpace(name))
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, nil, "Something went wrong on the server."
	}
	if a == nil {
		return nil, nil, strings.TrimSpace(name) + " isn't a registered nick. They register first: /ns register <password>"
	}
	if s.isAdminAccount(a) {
		return nil, nil, a.Name + " is the owner (the server admin in beeircd.conf); change it there."
	}
	cur, err := s.store.OperByAccount(a.ID)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, nil, "Something went wrong on the server."
	}
	return a, cur, ""
}

// panelOperSave adds or changes an entry for po. hash is the new oper
// password, already hashed ("" = keep / none).
func (s *Server) panelOperSave(po *control.PanelOper, ip string, req control.Request, hash string) control.Response {
	fail := func(f string, a ...any) control.Response { return control.Response{Error: fmt.Sprintf(f, a...)} }
	a, cur, problem := s.panelOperTarget(req.Name)
	if problem != "" {
		return fail("%s", problem)
	}
	switch {
	case req.New && cur != nil:
		return fail("%s is an oper already. Open their row to change them.", a.Name)
	case !req.New && cur == nil:
		return fail("%s isn't an oper (any more).", a.Name)
	}
	// An entry with flags po lacks is out of po's reach (as AdminServ
	// OPER): its password or hosts would let po sign in as it.
	if cur != nil {
		if l := lacking(po.Flags, s.viewOf(cur).flags); l != "" {
			return fail("You can't change %s: they have %s and you don't.", a.Name, flagWords(l))
		}
	}
	o := &store.Oper{AccountID: a.ID, Account: a.Name, AddedBy: po.Account, AddedAt: time.Now()}
	if cur != nil {
		cp := *cur
		o = &cp
	}
	// class
	o.Class = ""
	if c := strings.TrimSpace(req.Class); c != "" {
		oc, ok := s.cfg.Class(c)
		if !ok {
			return fail("There is no class %s.", c)
		}
		o.Class = oc.Name
	}
	// own flags
	flags, bad := cleanFlags(strings.TrimSpace(req.Flags))
	if bad != 0 {
		return fail("%c isn't an oper flag. Flags: a admin, c links, g global, h hosts, k kill, s services.", bad)
	}
	o.Flags = flags
	if o.Class == "" && o.Flags == "" {
		return fail("Give a class or at least one flag.")
	}
	// hosts
	hosts, ok := operMasks(strings.FieldsFunc(req.Hosts, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' }))
	if !ok {
		return fail("Allowed from: masks like *@192.168.50.* or ~ember@*.example.net, up to 20.")
	}
	o.Hosts = hosts
	if hash != "" {
		o.PassHash = hash
	}
	// the rules
	newAll, oldAll := s.viewOf(o).flags, ""
	if cur != nil {
		oldAll = s.viewOf(cur).flags
	}
	if l := lacking(po.Flags, lacking(oldAll, newAll)); l != "" {
		return fail("You can't give %s: you don't have it yourself.", flagWords(l))
	}
	if l := lacking(po.Flags, lacking(newAll, oldAll)); l != "" {
		return fail("You can't take %s away: you don't have it yourself.", flagWords(l))
	}
	if irc.EqualFold(a.Name, po.Account) && strings.IndexByte(newAll, 'a') < 0 {
		return fail("You can't take flag a from yourself here: you would be locked out of the panel.")
	}
	if cur != nil && encodeOper(cur) == encodeOper(o) && hash == "" {
		return fail("Nothing changed.")
	}
	if err := s.store.SaveOper(o); err != nil {
		s.log.Printf("services database: %v", err)
		return fail("Something went wrong on the server.")
	}
	who := po.Account + " (Hive Panel)"
	var id int64
	if cur == nil || encodeOper(cur) != encodeOper(o) {
		id = s.recordBy(who, "oper", a.Name, encodeOper(cur), encodeOper(o))
	}
	if hash != "" {
		s.svc.ClearLockout(operLockKey(a.ID))
		if cur != nil {
			s.recordBy(who, "oper", a.Name, "password", "password")
		}
	}
	s.recheckOpers(a.ID)
	what := "changed the oper entry of " + a.Name
	if cur == nil {
		what = "made " + a.Name + " an oper"
	}
	s.panelEvent(po.Account, ip, what+" ("+orNone(newAll)+")")
	s.snotice("Hive Panel: %s %s: class %s, flags %s, from %s", po.Account, what, orNone(o.Class), orNone(newAll), strings.Join(hosts, " "))
	return control.Response{Changed: &control.Changed{Name: a.Name, Old: oldAll, New: newAll, ID: id}}
}

// panelOperDel removes an entry for po.
func (s *Server) panelOperDel(po *control.PanelOper, ip, name string) control.Response {
	a, cur, problem := s.panelOperTarget(name)
	if problem != "" {
		return control.Response{Error: problem}
	}
	if cur == nil {
		return control.Response{Error: a.Name + " isn't an oper (any more)."}
	}
	if irc.EqualFold(a.Name, po.Account) {
		return control.Response{Error: "You can't remove yourself here."}
	}
	all := s.viewOf(cur).flags
	if l := lacking(po.Flags, all); l != "" {
		return control.Response{Error: fmt.Sprintf("You can't remove %s: they have %s and you don't.", a.Name, flagWords(l))}
	}
	if _, err := s.store.DeleteOper(a.ID); err != nil {
		s.log.Printf("services database: %v", err)
		return control.Response{Error: "Something went wrong on the server."}
	}
	if s.panelGone == nil {
		s.panelGone = map[int64]string{}
	}
	s.panelGone[a.ID] = cur.PassHash // so an undo brings the password back too
	id := s.recordBy(po.Account+" (Hive Panel)", "oper", a.Name, encodeOper(cur), "")
	s.recheckOpers(a.ID)
	s.panelEvent(po.Account, ip, "removed "+a.Name+" as an oper")
	s.snotice("Hive Panel: %s removed %s as an oper", po.Account, a.Name)
	return control.Response{Changed: &control.Changed{Name: a.Name, Old: all, ID: id}}
}

// panelOperUndo puts an oper entry back as it was before HISTORY entry ch,
// when po could have made that change.
func (s *Server) panelOperUndo(po *control.PanelOper, ip string, ch store.Change) control.Response {
	if ch.Old == "password" || ch.New == "password" {
		return control.Response{Error: "Password changes can't be undone. Set a new one."}
	}
	a, cur, problem := s.panelOperTarget(ch.Key)
	if problem != "" {
		return control.Response{Error: problem}
	}
	if encodeOper(cur) != ch.New {
		return control.Response{Error: a.Name + "'s oper entry has changed again since."}
	}
	var o *store.Oper
	if ch.Old != "" {
		var r operRecord
		if json.Unmarshal([]byte(ch.Old), &r) != nil {
			return control.Response{Error: "That change can't be read."}
		}
		o = &store.Oper{AccountID: a.ID, Account: a.Name, Class: r.Class, Flags: r.Flags, Title: r.Title, Vhost: r.Vhost,
			Swhois: r.Swhois, Hosts: r.Hosts, Disabled: r.Disabled, AddedBy: po.Account, AddedAt: time.Now()}
		if cur != nil {
			o.PassHash, o.DisabledBy, o.AddedBy, o.AddedAt = cur.PassHash, cur.DisabledBy, cur.AddedBy, cur.AddedAt
		} else {
			o.PassHash = s.panelGone[a.ID]
		}
	}
	before, after := "", ""
	if cur != nil {
		before = s.viewOf(cur).flags
	}
	if o != nil {
		after = s.viewOf(o).flags
	}
	// po must cover the entry as it is and as it would be (as AS UNDO).
	if l := lacking(po.Flags, before); l != "" {
		return control.Response{Error: fmt.Sprintf("You can't change %s: they have %s and you don't.", a.Name, flagWords(l))}
	}
	if l := lacking(po.Flags, after); l != "" {
		return control.Response{Error: fmt.Sprintf("You can't undo that: it gives %s and you don't have it.", flagWords(l))}
	}
	if irc.EqualFold(a.Name, po.Account) && strings.IndexByte(after, 'a') < 0 {
		return control.Response{Error: "You can't take flag a from yourself here."}
	}
	var err error
	if o == nil {
		_, err = s.store.DeleteOper(a.ID)
	} else {
		err = s.store.SaveOper(o)
	}
	if err != nil {
		s.log.Printf("services database: %v", err)
		return control.Response{Error: "Something went wrong on the server."}
	}
	delete(s.panelGone, a.ID)
	s.recordBy(po.Account+" (Hive Panel)", "oper", a.Name, ch.New, encodeOper(o))
	s.recheckOpers(a.ID)
	s.panelEvent(po.Account, ip, "put the oper entry of "+a.Name+" back")
	return control.Response{Changed: &control.Changed{Name: a.Name, Old: before, New: after}}
}
