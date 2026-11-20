package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
)

// Admin reach: home (own server only) or network, given by the network
// owner; [link] outside_admins; the lock-out guard.

// reachOwner makes Kumbang the network owner on the hub, registered and
// opered.
func reachOwner(t *testing.T, hub *linkNode) *tc {
	t.Helper()
	h, _ := passwd.Hash(operPW)
	hub.s.onLoop(func() { hub.s.cfg.Services.Admin, hub.s.cfg.Services.AdminPassword = "Kumbang", h })
	k := login(t, hub.s, "Kumbang")
	k.send("NS REGISTER " + pw)
	k.expect("is now registered to you.")
	k.send("OPER Kumbang " + operPW)
	k.expect(" 381 Kumbang :You are now an IRC operator")
	return k
}

// waitReach waits until n sees nick with reach want.
func waitReach(t *testing.T, n *linkNode, nick, want string) {
	t.Helper()
	waitLink(t, n.name+" sees "+nick+" with reach "+want, func() bool {
		u := n.user(nick)
		if u == nil {
			return false
		}
		got := ""
		n.s.onLoop(func() { got = u.OperReach })
		return got == want
	})
}

// A leaf oper (entry on the hub, BEEOPER) uses the hub's AdminServ only
// with network reach, which only the owner gives.
func TestReachLeafOperOnHub(t *testing.T) {
	hub, leaf := linkedPair(t)
	k := reachOwner(t, hub)
	lop := login(t, leaf.s, "lop")
	lop.send("NS REGISTER " + pw)
	lop.expect("is now registered to you.")
	k.send("AS OPER lop ADD CLASS netadmin HOST *@* PASSWORD " + emberPW)
	k.expect("lop is an oper now")
	lop.send("OPER lop " + emberPW)
	lop.expect(" 381 lop ")
	hub.seen(t, "lop")
	waitLink(t, "the hub sees lop opered", func() bool {
		u := hub.user("lop")
		ok := false
		hub.s.onLoop(func() { ok = u != nil && u.Modes['o'] && strings.IndexByte(u.OperFlags, 'a') >= 0 })
		return ok
	})

	lop.send("AS SHOW max-users")
	lop.expect("Your admin reach is leaf.test.local only. Whole-network reach is given by the network owner (Kumbang on hub.test.local): /as oper lop set reach network")
	lop.send("AS OPER lop SET REACH NETWORK") // every hub command is refused
	lop.expect("Your admin reach is leaf.test.local only.")

	// A leaf that claims network reach its hub never gave is not believed.
	var faked string
	u := hub.user("lop")
	hub.s.onLoop(func() {
		u.OperReach = store.ReachNetwork
		faked = hub.s.reachOf(u.UID)
		u.OperReach = ""
	})
	if faked != "" {
		t.Fatalf("hub believed a faked network reach: %q", faked)
	}

	k.send("AS OPER lop SET REACH NETWORK")
	k.expect("reach network.")
	lop.expect("Your admin reach is now the whole network")
	waitReach(t, hub, "lop", store.ReachNetwork)
	k.send("AS OPER LIST")
	k.expect("reach: network (network owner)")
	k.expect("reach: network")

	lop.send("AS SHOW max-users")
	lop.expect("  max-users")
	lop.send("AS OPER lop SET TITLE Leafy")
	lop.expect("title Leafy, reach network.")
	k.expect("Outside AdminServ from lop (lop on leaf.test.local): OPER lop SET TITLE Leafy")
	k.send("AS HISTORY")
	k.expect("lop on leaf.test.local")

	// Only the owner changes reach, network admins included.
	lop.send("AS OPER lop SET REACH HOME")
	lop.expect("Only the network owner (Kumbang on hub.test.local) gives or takes whole-network admin reach.")
	// The lock-out guard.
	lop.send("AS SERVER DEL leaf.test.local")
	lop.expect("The link blocks on hub.test.local is changed only by people on it")
	lop.send("AS OPER Kumbang SET FLAGS a")
	lop.expect("The config admin's oper entry on hub.test.local is changed only by people on it")

	// Back to home: the hub refuses again; UNDO of a reach change is the
	// owner's too.
	k.send("AS OPER lop SET REACH HOME")
	k.expect("lop: ")
	lop.expect("Your admin reach is now leaf.test.local only")
	waitReach(t, hub, "lop", "")
	lop.send("AS SHOW max-users")
	lop.expect("Your admin reach is leaf.test.local only.")
}

// BEEADMIN (/as server <leaf> ...): refused with outside_admins = none;
// with network, only for admins with network reach.
func TestReachBeeAdmin(t *testing.T) {
	hub, leaf := linkedPair(t)
	k := reachOwner(t, hub)
	hop := registeredOn(t, hub.s, "hop")
	k.send("AS OPER hop ADD CLASS netadmin HOST *@* PASSWORD " + emberPW)
	k.expect("hop is an oper now")
	hop.send("OPER hop " + emberPW)
	hop.expect(" 381 hop ")
	leaf.seen(t, "hop")
	waitReach(t, leaf, "Kumbang", store.ReachNetwork) // the owner always has it

	hop.send("AS SERVER leaf.test.local SHOW max-users")
	hop.expect("Your admin reach is hub.test.local only.")
	k.send("AS SERVER leaf.test.local SHOW max-users")
	k.expect("[leaf.test.local AdminServ]   max-users")

	k.send("AS OPER hop SET REACH NETWORK")
	hop.expect("Your admin reach is now the whole network")
	waitReach(t, leaf, "hop", store.ReachNetwork)
	hop.send("AS SERVER leaf.test.local SHOW max-users")
	hop.expect("[leaf.test.local AdminServ]   max-users")

	leaf.s.onLoop(func() { leaf.s.cfg.Link.OutsideAdmins = config.OutsideNone })
	hop.send("AS SERVER leaf.test.local SHOW max-users")
	hop.expect("leaf.test.local doesn't take admin commands from other servers (outside_admins = none). Ask whoever runs it.")
	hop.send("AS SERVER leaf.test.local STATUS")
	hop.expect("outside admins none")
}

// registeredOn is registered for servers not named irc.test.local.
func registeredOn(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := login(t, s, nick)
	c.send("NS REGISTER " + pw)
	c.expect("is now registered to you.")
	return c
}

// On one server: SET REACH is the owner's; network entries are changed
// by network admins only; the owner opered from another nick may set
// their own look; the Hive Panel follows the same rules.
func TestReachOwnerOnly(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	fern := registered(t, s, "fern")
	registered(t, s, "gale")
	b.send("AS OPER fern ADD CLASS netadmin HOST *@* PASSWORD " + emberPW)
	b.expect("fern is an oper now")
	b.send("AS OPER gale ADD CLASS admin HOST *@* PASSWORD " + emberPW)
	b.expect("gale is an oper now")
	fern.send("OPER fern " + emberPW)
	fern.expect(" 381 fern ")

	fern.send("AS OPER gale SET REACH NETWORK")
	fern.expect("Only the network owner (Boss on irc.test.local) gives or takes whole-network admin reach.")
	b.send("AS OPER gale SET REACH sideways")
	b.expect("Syntax: OPER gale SET REACH HOME|NETWORK")
	b.send("AS OPER gale SET REACH NETWORK")
	b.expect("reach network.")
	if o, _ := s.store.OperByAccount(accountIDOf(t, s, "gale")); !o.Network() {
		t.Fatalf("gale reach %+v", o)
	}
	// fern (home) can't touch a network entry any more.
	fern.send("AS OPER gale SET TITLE Gale")
	fern.expect("their admin reach is the whole network and yours isn't")
	// UNDO of the reach change: the owner's too.
	fern.send("AS HISTORY 1")
	line := fern.expect("OPER   gale")
	id := strings.Fields(line[strings.Index(line, "#")+1:])[0]
	fern.send("AS UNDO " + id)
	fern.expect("yours isn't")
	b.send("AS UNDO " + id)
	b.expect("back to how it was")
	if o, _ := s.store.OperByAccount(accountIDOf(t, s, "gale")); o.Network() {
		t.Fatalf("undo kept network reach %+v", o)
	}

	// The owner, opered from another nick without logging in, sets their
	// own look.
	b2 := login(t, s, "queen")
	b2.send("OPER Boss " + operPW)
	b2.expect(" 381 queen ")
	b2.send("AS OPER Boss SET TITLE Queen Bee")
	b2.expect("title is now Queen Bee")

	// The Hive Panel.
	ask := func(as string, r control.Request) *control.Response {
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}
	o := ask("Boss", control.Request{Cmd: control.CmdOpers}).Opers
	if o == nil || !o.OwnerView || o.Rows[0].Reach != store.ReachNetwork {
		t.Fatalf("owner view: %+v", o)
	}
	if o := ask("fern", control.Request{Cmd: control.CmdOpers}).Opers; o == nil || o.OwnerView {
		t.Fatalf("fern view: %+v", o)
	}
	save := control.Request{Cmd: control.CmdOperSave, Name: "gale", Class: "admin", Hosts: "*@*", Reach: "network"}
	if r := ask("fern", save); !strings.Contains(r.Error, "Only the network owner") {
		t.Errorf("fern gives reach: %+v", r)
	}
	if r := ask("Boss", save); r.Error != "" {
		t.Fatalf("owner gives reach: %+v", r)
	}
	o = ask("Boss", control.Request{Cmd: control.CmdOpers}).Opers
	for _, row := range o.Rows {
		if row.Account == "gale" && row.Reach != store.ReachNetwork {
			t.Errorf("gale row %+v", row)
		}
	}
	if r := ask("fern", control.Request{Cmd: control.CmdOperSave, Name: "gale", Class: "admin", Hosts: "*@127.0.0.1"}); !strings.Contains(r.Error, "yours isn't") {
		t.Errorf("fern changes a network entry: %+v", r)
	}
	if r := ask("fern", control.Request{Cmd: control.CmdOperDel, Name: "gale"}); !strings.Contains(r.Error, "yours isn't") {
		t.Errorf("fern removes a network entry: %+v", r)
	}
}

func accountIDOf(t *testing.T, s *Server, nick string) int64 {
	t.Helper()
	a, err := s.store.AccountByNick(nick)
	if err != nil || a == nil {
		t.Fatalf("account %s: %v", nick, err)
	}
	return a.ID
}

// A leaf can't give its own admins network reach: the hub keeps and
// passes on only the reach its own records give, and a leaf takes reach
// only from the services server.
func TestReachClaimFromLeafNotTrusted(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	a := newLinkNode(t, "a.test.local", "0AA", "leaf")
	b := newLinkNode(t, "b.test.local", "0BB", "leaf")
	hub.expects(a, false, "")
	hub.expects(b, false, "")
	a.expects(hub, true, "")
	b.expects(hub, true, "")
	hub.start(t)
	a.start(t)
	b.start(t)
	waitLink(t, "tree", func() bool { return len(a.servers()) == 3 && len(b.servers()) == 3 })
	login(t, a.s, "mal")
	hub.seen(t, "mal")
	b.seen(t, "mal")
	uid := hub.user("mal").UID
	reach := func(n *linkNode) (r string) {
		n.s.onLoop(func() { r = n.s.net.UserByUID(uid).OperReach })
		return
	}

	// a (modified) says mal has network reach; the hub gave none.
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+` bee-operlook :{"t":"boss","r":"network"}`)
	waitLink(t, "b gets mal's title", func() bool {
		u := b.user("mal")
		ok := false
		b.s.onLoop(func() { ok = u != nil && u.OperTitle == "boss" })
		return ok
	})
	if r := reach(hub); r != "" {
		t.Errorf("the hub took mal's reach %q from a", r)
	}
	if r := reach(b); r != "" {
		t.Errorf("b got mal's reach %q through the hub", r)
	}
	// From the services server, b takes it.
	b.fromLinkTo(t, "hub.test.local", ":0HB MD client "+uid+` bee-operlook :{"t":"boss","r":"network"}`)
	if r := reach(b); r != store.ReachNetwork {
		t.Errorf("b refused reach from the services server: %q", r)
	}
}
