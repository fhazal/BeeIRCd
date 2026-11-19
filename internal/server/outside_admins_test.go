package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/store"
)

// The owner switches [link] outside_admins live from the Hive Panel; a
// non-owner panel oper and a bad value are refused.
func TestOutsideAdminsPanel(t *testing.T) {
	s, _, _ := fileServer(t, "")
	owner := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	admin := &control.PanelOper{Account: "fern", Flags: allFlags}
	set := func(po *control.PanelOper, v string) (r control.Response) {
		s.onLoop(func() { r = s.panelSet(po, "127.0.0.1", "outside-admins", v) })
		return
	}
	cur := func() (v string, l *control.Links) {
		s.onLoop(func() { v, l = s.cfg.Link.OutsideAdmins, s.panelLinks() })
		return
	}
	if v, l := cur(); v != config.OutsideNetwork || l.OutsideAdmins != "network" {
		t.Fatalf("default %q / %q", v, l.OutsideAdmins)
	}
	if r := set(owner, "none"); r.Error != "" || r.Changed == nil {
		t.Fatalf("owner none: %+v", r)
	}
	if v, l := cur(); v != config.OutsideNone || l.OutsideAdmins != "none" {
		t.Fatalf("after none %q / %q", v, l.OutsideAdmins)
	}
	if r := set(owner, "Network"); r.Error != "" {
		t.Fatalf("owner network: %+v", r)
	}
	if v, _ := cur(); v != config.OutsideNetwork {
		t.Fatalf("after network %q", v)
	}
	if r := set(admin, "none"); !strings.Contains(r.Error, "Only the network owner can change outside-admins.") {
		t.Errorf("non-owner: %+v", r)
	}
	if r := set(owner, "sideways"); r.Error == "" {
		t.Errorf("bad value taken: %+v", r)
	}
	if v, _ := cur(); v != config.OutsideNetwork {
		t.Errorf("a refused change moved it to %q", v)
	}
	// UNDO in the panel goes through the same check.
	ch := set(owner, "none")
	if ch.Changed == nil || ch.Changed.ID == 0 {
		t.Fatalf("no HISTORY entry: %+v", ch)
	}
	id := ch.Changed.ID
	var r control.Response
	s.onLoop(func() { r = s.panelUndo(admin, "127.0.0.1", id) })
	if !strings.Contains(r.Error, "Only the network owner") {
		t.Errorf("non-owner undo: %+v", r)
	}
	s.onLoop(func() { r = s.panelUndo(owner, "127.0.0.1", id) })
	if r.Error != "" {
		t.Errorf("owner undo: %+v", r)
	}
	if v, _ := cur(); v != config.OutsideNetwork {
		t.Errorf("undo left %q", v)
	}
}

// AdminServ SET OUTSIDE-ADMINS: the owner may, an admin who isn't the owner
// may not, an admin from another server never.
func TestOutsideAdminsAdminServ(t *testing.T) {
	hub, leaf := linkedPair(t)
	k := reachOwner(t, hub)
	hop := registeredOn(t, hub.s, "hop")
	k.send("AS OPER hop ADD CLASS netadmin HOST *@* PASSWORD " + emberPW)
	k.expect("hop is an oper now")
	hop.send("OPER hop " + emberPW)
	hop.expect(" 381 hop ")
	leaf.seen(t, "hop")
	k.send("AS OPER hop SET REACH NETWORK")
	hop.expect("Your admin reach is now the whole network")
	waitReach(t, leaf, "hop", store.ReachNetwork)

	// Not the owner, even with network reach.
	hop.send("AS SET outside-admins none")
	hop.expect("Only the network owner (Kumbang on hub.test.local) can change outside-admins.")
	// From outside: refused on the leaf, for the admin and for the owner.
	hop.send("AS SERVER leaf.test.local SET outside-admins none")
	hop.expect("outside-admins on leaf.test.local is changed only by people on it")
	k.send("AS SERVER leaf.test.local SET outside-admins none")
	k.expect("outside-admins on leaf.test.local is changed only by people on it")
	if got := leaf.s.cfg.Link.OutsideAdmins; got != config.OutsideNetwork {
		t.Fatalf("leaf outside_admins moved to %q", got)
	}
}

// On a server with a config file: the owner sets it at AdminServ (live),
// an admin who isn't the owner can't, a bad value is refused, and UNDO
// follows the same rule.
func TestOutsideAdminsOwnerAtAdminServ(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	fern := registered(t, s, "fern")
	b.send("AS OPER fern ADD CLASS netadmin HOST *@* PASSWORD " + emberPW)
	b.expect("fern is an oper now")
	fern.send("OPER fern " + emberPW)
	fern.expect(" 381 fern ")

	fern.send("AS SET outside-admins none")
	fern.expect("Only the network owner (Boss on irc.test.local) can change outside-admins.")
	b.send("AS SET outside-admins maybe")
	b.expect("outside-admins is network or none.")
	b.send("AS SET outside-admins none")
	b.expect("outside-admins is now none (was network)")
	if got := s.cfg.Link.OutsideAdmins; got != config.OutsideNone {
		t.Fatalf("outside_admins %q", got)
	}
	b.send("AS HISTORY 1")
	line := b.expect("outside-admins")
	id := strings.Fields(line[strings.Index(line, "#")+1:])[0]
	fern.send("AS UNDO " + id)
	fern.expect("Only the network owner")
	if got := s.cfg.Link.OutsideAdmins; got != config.OutsideNone {
		t.Fatalf("a non-owner's undo moved it to %q", got)
	}
	b.send("AS UNDO " + id)
	b.expect("outside-admins is now network")
	b.send("AS SET outside-admins NETWORK")
	b.expect("outside-admins is now network")
}
