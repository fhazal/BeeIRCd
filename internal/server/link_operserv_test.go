package server

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// netadminOn opers nick on n (netadmin class), for tests.
func netadminOn(t *testing.T, n *linkNode, c *tc, nick string) {
	t.Helper()
	n.s.onLoop(func() { n.s.operUp(n.s.clients[n.s.net.UserByNick(nick).UID], &store.Oper{Class: "netadmin"}) })
	c.expect(" 381 " + nick + " ")
}

// STATS UPLINK, JUPE, NOOP and DEFCON on a hub with a leaf.
func TestLinkOperServTools(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")

	op.send("OS STATS UPLINK")
	if l := op.expect("Link leaf.test.local"); !strings.Contains(l, "up") {
		t.Errorf("uplink %q", l)
	}

	// NOOP on the leaf: its opers lose it and can't oper again.
	lop := login(t, leaf.s, "lop")
	netadminOn(t, leaf, lop, "lop")
	op.send("OS NOOP SET leaf.test.local")
	op.expect("Nobody can be an IRC operator on leaf.test.local now.")
	lop.expect("NOOP: nobody may be an IRC operator")
	lop.send("OPER lop whatever")
	lop.expect(" 491 lop ")
	op.send("OS NOOP REVOKE leaf.test.local")
	op.expect("IRC operators are back on leaf.test.local.")
	waitLink(t, "noop lifted", func() bool {
		var on bool
		leaf.s.onLoop(func() { on = leaf.s.links.noop })
		return !on
	})

	// DEFCON 4: no registrations, also from the leaf; 2: no new people.
	op.send("OS DEFCON 4")
	op.expect("Services are now at DEFCON 4")
	kim := login(t, leaf.s, "kim")
	hub.seen(t, "kim")
	kim.send("NS REGISTER " + pw)
	kim.expect("Services are in DefCon mode, please try again later.")
	op.send("OS DEFCON 2")
	op.expect("Services are now at DEFCON 2")
	kim.expect("The Defcon level is now at: 2")
	late := raw(t, leaf.s)
	late.nick = "late"
	late.send("NICK late")
	late.send("USER late 0 * :Late")
	if l := late.expect("ERROR"); !strings.Contains(l, "DEFCON 2") {
		t.Errorf("late %q", l)
	}
	op.send("OS DEFCON 5")
	op.expect("Services are now at DEFCON 5")

	// JUPE the leaf: it splits and can't come back until DEL.
	op.send("OS JUPE leaf.test.local testing jupes")
	op.expect("leaf.test.local is juped.")
	waitLink(t, "leaf split", func() bool { return hub.user("kim") == nil })
	if !hub.logHas("JUPED") && !leaf.logHas("Juped") {
		t.Log("no jupe reason in logs")
	}
	op.send("OS JUPE LIST")
	op.expect("leaf.test.local (JUPED by op: testing jupes)")
	op.send("OS JUPE DEL leaf.test.local")
	op.expect("The jupe on leaf.test.local is lifted.")
	leaf.s.onLoop(func() { // skip the leaf's reconnect wait
		for _, r := range leaf.s.links.retry {
			r.next = time.Time{}
		}
	})
	waitLink(t, "leaf back", func() bool { return hub.user("kim") != nil })
}

// SERVER ADD/SET/DEL rewrite beeircd.conf; LINK and UNLINK act at once.
func TestLinkServerBlocks(t *testing.T) {
	port := freePort(t)
	s, path, _ := fileServer(t, "\n[link]\nrole = \"hub\"\naddress = \"127.0.0.1\"\nport = "+strconv.Itoa(port)+"\n")
	b := login(t, s, "Boss")
	s.onLoop(func() { s.operUp(s.clients[s.net.UserByNick("Boss").UID], &store.Oper{Class: "netadmin"}) })
	b.expect(" 381 Boss ")
	b.send("OS SERVER LIST")
	b.expect("No link blocks yet")
	fp := "iGbKErLtGToIR5WUi4fhBLfaPhxmZngpT/sLquE7WX0="
	b.send("OS SERVER ADD leaf.test.local " + fp + " 127.0.0.1 1")
	b.expect("Added the link block for leaf.test.local. Saved to beeircd.conf.")
	b.send("OS SERVER SET leaf.test.local AUTOCONNECT OFF")
	b.expect("Changed AUTOCONNECT for leaf.test.local.")
	b.send("OS SERVER SET leaf.test.local PORT 7002")
	b.expect("Changed PORT for leaf.test.local.")
	text, _ := os.ReadFile(path)
	for _, want := range []string{"[[link.server]]", `name = "leaf.test.local"`, "port = 7002", `fingerprint = "` + fp + `"`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(string(text), "autoconnect") {
		t.Errorf("autoconnect still there:\n%s", text)
	}
	b.send("OS SERVER LIST")
	b.expect("leaf.test.local: down (127.0.0.1:7002)")
	b.send("OS SERVER ADD bad name")
	b.expect("Not saved: ")
	b.send("AS SERVER LINK leaf.test.local")
	b.expect("Connecting to leaf.test.local (127.0.0.1:7002).")
	b.send("OS SERVER DEL leaf.test.local")
	b.expect("Removed the link block for leaf.test.local.")
	text, _ = os.ReadFile(path)
	if strings.Contains(string(text), "leaf.test.local") {
		t.Errorf("block still there:\n%s", text)
	}
}

// /as server <leaf> ...: STATUS always (admins); commands only with
// [link] remote_admin = true on the leaf. AdminServ on the hub refuses
// people from the leaf unless the hub allows it too.
func TestLinkRemoteAdmin(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	leaf.seen(t, "op")
	op.send("AS SERVER leaf.test.local STATUS")
	op.expect("[leaf.test.local AdminServ] leaf.test.local: BeeIRCd ")
	op.send("AS SERVER leaf.test.local SHOW max-users")
	op.expect("remote_admin = false")
	leaf.s.onLoop(func() { leaf.s.cfg.Link.RemoteAdmin = true })
	op.send("AS SERVER leaf.test.local SHOW max-users")
	op.expect("[leaf.test.local AdminServ]   max-users")
	op.send("AS SERVER leaf.test.local RESTART")
	op.expect("RESTART can't be run on another server")

	lop := login(t, leaf.s, "lop")
	netadminOn(t, leaf, lop, "lop")
	hub.seen(t, "lop")
	lop.send("AS SHOW max-users") // the hub's AdminServ: not for leaf people
	lop.expect("only takes commands from people on it")
	lop.send("AS SERVER leaf.test.local STATUS")
	lop.expect("[leaf.test.local AdminServ] leaf.test.local: BeeIRCd ")
}
