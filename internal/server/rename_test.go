package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// IRCv3 draft/channel-rename.

// operOn opers nick on s (netadmin class), for tests.
func operOn(t *testing.T, s *Server, c *tc) {
	t.Helper()
	s.do(func() { s.operUp(s.clients[s.net.UserByNick(c.nick).UID], &store.Oper{Class: "netadmin"}) })
	c.expect(" 381 " + c.nick + " ")
}

// renameAgain clears the cooldown on channel key (folded), as if ten
// minutes went by.
func renameAgain(s *Server, key string) { s.do(func() { delete(s.renamedAt, key) }) }

func TestRename(t *testing.T) {
	s := testServer(t, draftsOn)
	a := capLogin(t, s, "alice", renameCap)
	b := login(t, s, "bob")
	c := login(t, s, "carol")
	joinAll("#old", a, b)
	a.send("TOPIC #old :the old place")
	b.expect("TOPIC #old :the old place")
	a.send("MODE #old +k honey")
	b.expect("MODE #old +k honey")
	joinAll("#taken", c)

	// Who may, and what can't be done.
	a.send("RENAME #old")
	a.expect(" 461 alice RENAME ")
	b.send("RENAME #old #new")
	b.expect(" 482 bob #old :You're not channel operator")
	c.send("RENAME #old #new")
	c.expect(" 442 carol #old ")
	a.send("RENAME #nope #new")
	a.expect(" 403 alice #nope ")
	a.send("RENAME #old &new")
	a.expect("FAIL RENAME CANNOT_RENAME #old &new :")
	a.send("RENAME #old #bad,name")
	a.expect("FAIL RENAME CANNOT_RENAME #old #bad,name :")
	a.send("RENAME #old #taken")
	a.expect("FAIL RENAME CHANNEL_NAME_IN_USE #old #taken :#taken is already in use")
	a.send("RENAME #old #TAKEN")
	a.expect("FAIL RENAME CHANNEL_NAME_IN_USE #old #TAKEN :")

	a.send("RENAME #old #new :tidy up")
	a.expect(":alice!~alice@127.0.0.1 RENAME #old #new :tidy up")
	// Without the cap: a part and a join, with the topic and names.
	b.expect(":bob!~bob@127.0.0.1 PART #old :tidy up")
	b.expect(":bob!~bob@127.0.0.1 JOIN #new")
	b.expect(" 332 bob #new :the old place")
	b.expect(" 353 bob ")
	b.expect(" 366 bob #new ")
	a.send("PRIVMSG #new :still here")
	b.expect(":alice!~alice@127.0.0.1 PRIVMSG #new :still here")
	b.send("MODE #new")
	if l := b.expect(" 324 bob #new "); !strings.Contains(l, "k") || !strings.Contains(l, "honey") {
		t.Errorf("modes after the rename: %s", l)
	}
	b.send("JOIN #old") // the old name is free: a new, empty channel
	b.expect(" 366 bob #old ")
	b.send("PART #old")
	b.expect(" PART #old")

	// One rename per channel per ten minutes, opers included.
	a.send("RENAME #new #newer")
	a.expect("FAIL RENAME CANNOT_RENAME #new #newer :#new was renamed a moment ago; it can be renamed again in 10 minutes")
	operOn(t, s, c)
	c.send("RENAME #new #newer")
	c.expect("FAIL RENAME CANNOT_RENAME #new #newer :#new was renamed")

	// An oper may rename from outside the channel.
	renameAgain(s, "#new")
	c.send("RENAME #new #newer")
	c.expect("Renamed #new to #newer.")
	a.expect(":carol!~carol@127.0.0.1 RENAME #new #newer")
	b.expect(" JOIN #newer")

	// A change of case only: the cap client sees it, the others nothing.
	renameAgain(s, "#newer")
	a.send("RENAME #newer #Newer")
	a.expect("RENAME #newer #Newer")
	b.quiet(" PART ")
	var name string
	var members int
	s.do(func() {
		if ch := s.net.Channel("#newer"); ch != nil {
			name, members = ch.Name, len(ch.Members)
		}
	})
	if name != "#Newer" || members != 2 {
		t.Errorf("after the case change: %q with %d members", name, members)
	}
}

// With the drafts off there is no cap and no command.
func TestRenameDraftsOff(t *testing.T) {
	s := testServer(t, nil)
	r := raw(t, s)
	r.send("CAP LS 302")
	if l := r.expect("CAP * LS :"); strings.Contains(l, renameCap) {
		t.Errorf("cap offered with the drafts off: %s", l)
	}
	a := login(t, s, "alice")
	joinAll("#a", a)
	a.send("RENAME #a #b")
	a.expect(" 421 alice RENAME ")
}

// History and read markers move with the channel.
func TestRenameHistoryAndMarkers(t *testing.T) {
	s := testServer(t, draftsOn)
	a := capLogin(t, s, "Bee", append(append([]string{}, histCaps...), readMarkerCap, renameCap)...)
	a.send("NS REGISTER " + pw)
	a.expect(":irc.test.local MODE Bee +r")
	guest := capLogin(t, s, "guest", readMarkerCap)
	b := login(t, s, "bob")
	joinAll("#old", a, guest, b)
	a.send("MODE #old +H 50:1d")
	b.expect("MODE #old +H 50:1d")
	b.send("PRIVMSG #old :kept line")
	a.expect("PRIVMSG #old :kept line")
	a.send("MARKREAD #old timestamp=2026-01-16T01:02:03.456Z")
	a.expect("MARKREAD #old timestamp=2026-01-16T01:02:03.456Z")
	guest.send("MARKREAD #old timestamp=2026-01-16T05:00:00.000Z")
	guest.expect("MARKREAD #old timestamp=2026-01-16T05:00:00.000Z")

	a.send("RENAME #old #new")
	a.expect("RENAME #old #new")
	a.send("CHATHISTORY LATEST #new * 10")
	a.expect(":bob!~bob@127.0.0.1 PRIVMSG #new :kept line")
	a.expect("BATCH -")
	a.send("MARKREAD #new")
	a.expect("MARKREAD #new timestamp=2026-01-16T01:02:03.456Z")
	guest.send("MARKREAD #new")
	guest.expect("MARKREAD #new timestamp=2026-01-16T05:00:00.000Z")

	var ms []store.ReadMarker
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		ms, _ = s.store.ReadMarkers()
		if len(ms) == 1 && ms[0].Target == "#new" {
			break
		}
	}
	if len(ms) != 1 || ms[0].Target != "#new" || ms[0].Account != "bee" {
		t.Errorf("stored markers %+v", ms)
	}
}

// A registered channel: who may rename it, and its ChanServ data moves.
func TestRenameRegistered(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.do(func() { s.cfg.Server.IRCv3Drafts = true })
	b := boss(t, s)
	f := founded(t, s, "Bee")
	e := registered(t, s, "ember")
	joinAll("#bchat", e)
	f.send("CS AOP #bchat ADD ember")
	e.expect(csSrc + "MODE #bchat +o ember")

	// A chanop without SET access can't.
	e.send("RENAME #bchat #hive")
	e.expect(" 482 ember #bchat :only the founder of #bchat or someone with SET access can rename it")

	b.send("OS FORBID ADD CHAN #warez* no piracy")
	b.expect("forbidden now")
	f.send("RENAME #bchat #warez1")
	f.expect("FAIL RENAME CANNOT_RENAME #bchat #warez1 :#warez1 is forbidden by the network: no piracy")
	joinAll("#boss", b)
	b.send("CS REGISTER #boss")
	b.expect("You are its Founder.")
	b.send("PART #boss")
	b.expect(" PART #boss")
	f.send("RENAME #bchat #boss")
	f.expect("FAIL RENAME CHANNEL_NAME_IN_USE #bchat #boss :")

	f.send("RENAME #bchat #hive :new home")
	f.expect(":Bee!~bee@127.0.0.1 PART #bchat :new home")
	f.expect(" JOIN #hive")
	e.expect(" JOIN #hive")

	var reg, old *store.Channel
	s.do(func() {
		reg, _ = s.store.ChannelByName("#hive")
		old, _ = s.store.ChannelByName("#bchat")
	})
	if reg == nil || old != nil || reg.Name != "#hive" || reg.Founder != "Bee" || reg.Description != "Be kind, stay curious" {
		t.Fatalf("registration after the rename: %+v (old %+v)", reg, old)
	}
	e.send("CS ACCESS #hive LIST")
	e.expect(csLine("ember", "Access list of #hive"))
	if l := e.expect("AOP"); !strings.Contains(l, "ember") {
		t.Errorf("access list: %s", l)
	}
	e.send("MODE #hive")
	if l := e.expect(" 324 ember #hive "); !strings.Contains(strings.Fields(l)[4], "r") {
		t.Errorf("+r lost: %s", l)
	}
	e.send("CS INFO #hive")
	e.expect("Be kind, stay curious")

	// READONLY stops renames, even an oper's.
	renameAgain(s, "#hive")
	readOnlyOn(t, b)
	f.send("RENAME #hive #comb")
	f.expect("FAIL RENAME CANNOT_RENAME #hive #comb :Services are in read-only mode")
	b.send("RENAME #hive #comb")
	b.expect("FAIL RENAME CANNOT_RENAME #hive #comb :Services are in read-only mode")
	// An unregistered channel too: its read markers and AJOINs would move.
	b.send("JOIN #loose")
	b.expect(" JOIN #loose")
	b.send("RENAME #loose #free")
	b.expect("FAIL RENAME CANNOT_RENAME #loose #free :Services are in read-only mode")
}

// The Hive Panel's Rename button goes the same way as RENAME.
func TestRenameFromPanel(t *testing.T) {
	s, _, _ := fileServer(t, "")
	boss(t, s)
	a := login(t, s, "alice")
	joinAll("#lobby", a)
	ask := func(name, to string) *control.Response {
		return panelAsk(t, s, control.Request{Cmd: control.CmdChanRename, As: "Boss", IP: "127.0.0.1", Name: name, Value: to, Reason: "moved"})
	}
	if r := ask("#lobby", "#hall"); r.Done == nil || r.Done.Text != "#lobby is now #hall." {
		t.Fatalf("panel rename: %+v", r)
	}
	a.expect(":alice!~alice@127.0.0.1 PART #lobby :moved")
	a.expect(":alice!~alice@127.0.0.1 JOIN #hall")
	if r := ask("#hall", "#again"); !strings.Contains(r.Error, "renamed a moment ago") {
		t.Errorf("cooldown from the panel: %+v", r)
	}
	if r := ask("#nowhere", "#else"); r.Error == "" {
		t.Errorf("no such channel from the panel: %+v", r)
	}
}

// joinLinked has c join name on n, after the other side has the channel.
func joinLinked(t *testing.T, other *linkNode, c *tc, name string) {
	t.Helper()
	waitLink(t, other.name+" knows "+name, func() bool {
		known := false
		other.s.onLoop(func() { known = other.s.net.Channel(name) != nil })
		return known
	})
	joinAll(name, c)
}

// channelOn is how n sees channel name: "" when it doesn't exist, else
// its name and member count.
func channelOn(n *linkNode, name string) (got string, members int) {
	n.s.onLoop(func() {
		if ch := n.s.net.Channel(name); ch != nil {
			got, members = ch.Name, len(ch.Members)
		}
	})
	return got, members
}

// A BeeIRCd leaf follows a rename made on the hub.
func TestRenameLinkBee(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	alice := capLogin(t, hub.s, "alice", renameCap)
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	joinAll("#x", alice)
	joinLinked(t, leaf, bob, "#x")
	alice.expect(":bob!~bob@")
	alice.send("TOPIC #x :linked topic")
	bob.expect("TOPIC #x :linked topic")

	alice.send("RENAME #x #y :moving")
	alice.expect(":alice!~alice@127.0.0.1 RENAME #x #y :moving")
	bob.expect(":bob!~bob@127.0.0.1 PART #x :moving")
	bob.expect(":bob!~bob@127.0.0.1 JOIN #y")
	bob.expect(" 332 bob #y :linked topic")
	bob.send("PRIVMSG #y :hi from the leaf")
	alice.expect("PRIVMSG #y :hi from the leaf")
	if old, _ := channelOn(leaf, "#x"); old != "" {
		t.Errorf("the leaf still has #x")
	}
	if got, n := channelOn(leaf, "#y"); got != "#y" || n != 2 {
		t.Errorf("the leaf has %q with %d members", got, n)
	}
	// The cooldown is kept on the leaf too.
	var cooling bool
	leaf.s.onLoop(func() { _, cooling = leaf.s.renamedAt["#y"] })
	if !cooling {
		t.Error("no cooldown on the leaf")
	}
}

// A link that can't follow a rename gets the channel again under the new
// name; a rename that would leave someone behind it in the old one is
// refused, for an old BeeIRCd and for UnrealIRCd or InspIRCd alike.
func TestRenameLinkRefused(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.peer.Software = "BeeIRCd-1.4.0"
		}
	})
	alice := login(t, hub.s, "alice")
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")

	// Nobody on the leaf: the leaf sees alice leave #solo and #only made.
	joinAll("#solo", alice)
	waitLink(t, "the leaf knows #solo", func() bool { got, _ := channelOn(leaf, "#solo"); return got != "" })
	alice.send("RENAME #solo #only")
	alice.expect(" JOIN #only")
	waitLink(t, "the leaf follows", func() bool {
		old, _ := channelOn(leaf, "#solo")
		got, n := channelOn(leaf, "#only")
		return old == "" && got == "#only" && n == 1
	})

	// bob on the old leaf: refused.
	joinLinked(t, leaf, bob, "#only")
	alice.expect(":bob!~bob@")
	renameAgain(hub.s, "#only")
	alice.send("RENAME #only #two")
	alice.expect("FAIL RENAME CANNOT_RENAME #only #two :bob is on leaf.test.local, which runs BeeIRCd-1.4.0 and can't rename channels; renaming now would split #only in two")

	// The same behind an UnrealIRCd link.
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.peer.Bee, l.peer.Software = false, "UnrealIRCd-6.1.8"
		}
	})
	alice.send("RENAME #only #two")
	alice.expect("FAIL RENAME CANNOT_RENAME #only #two :bob is on leaf.test.local, which runs UnrealIRCd-6.1.8 and can't rename channels")
	if got, _ := channelOn(hub, "#only"); got != "#only" {
		t.Errorf("hub has %q after a refused rename", got)
	}
}

// holdLoop stops n's loop until the returned func is called with what it
// does first once going again: lines that arrive meanwhile wait behind it.
func holdLoop(n *linkNode) func(first func()) {
	held, gate := make(chan struct{}), make(chan func())
	go n.s.onLoop(func() {
		close(held)
		(<-gate)()
	})
	<-held
	return func(first func()) { gate <- first }
}

// sameChannel waits until every node has channel name the same way and
// none still has old; it returns that state.
func sameChannel(t *testing.T, name, old string, nodes ...*linkNode) string {
	t.Helper()
	var states []string
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		states = states[:0]
		first := chanState(nodes[0].s, name)
		agree := true
		for _, n := range nodes {
			st := chanState(n.s, name)
			if o := chanState(n.s, old); o != "none" {
				agree = false
				st += " (" + old + " is still there: " + o + ")"
			}
			states = append(states, n.name+": "+st)
			agree = agree && st == first
		}
		if agree {
			return first
		}
	}
	t.Fatalf("the servers don't agree on %s:\n%s", name, strings.Join(states, "\n"))
	return ""
}

// A rename onto a name someone on the other server made the same moment:
// the two channels become one, the same on both servers, by the SJOIN
// rules (the older one keeps its modes, ranks and age; the same age
// merges).
func TestRenameLinkClash(t *testing.T) {
	for _, tt := range []struct {
		name  string
		age   int64     // the new channel's TS minus the renamed one's
		ranks [2]string // alice's and charlie's ranks after
		modes string
	}{
		{"renamed one older", 100, [2]string{"o", ""}, "nst"},
		{"same age", 0, [2]string{"o", "o"}, "nst"},
		{"renamed one younger", -100, [2]string{"", "o"}, "nt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub, leaf := linkedPair(t)
			for _, n := range []*linkNode{hub, leaf} {
				n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
			}
			alice := capLogin(t, hub.s, "alice", renameCap)
			bob := login(t, leaf.s, "bob")
			carol := capLogin(t, leaf.s, "carol", renameCap)
			charlie := login(t, leaf.s, "charlie")
			for _, nick := range []string{"bob", "carol", "charlie"} {
				hub.seen(t, nick)
			}
			joinAll("#old", alice)
			alice.send("MODE #old +s")
			alice.expect("MODE #old +s")
			alice.send("TOPIC #old :the old place")
			alice.expect("TOPIC #old :the old place")
			joinLinked(t, leaf, bob, "#old")
			joinAll("#old", carol)
			alice.expect(":carol!")
			var ts int64
			hub.s.onLoop(func() { ts = hub.s.net.Channel("#old").TS })
			cu := leaf.user("charlie").UID

			// The rename leaves the hub while charlie makes #new on the leaf.
			release := holdLoop(leaf)
			alice.send("RENAME #old #new :moving")
			alice.expect(":alice!~alice@127.0.0.1 RENAME #old #new :moving")
			release(func() { leaf.s.emit(&state.Join{UID: cu, Channel: "#new", TS: ts + tt.age}) })
			charlie.expect(":charlie!~charlie@127.0.0.1 JOIN #new")

			got := sameChannel(t, "#new", "#old", hub, leaf)
			for i, uid := range []string{hub.user("alice").UID, cu} {
				if want := tt.ranks[i] + ":" + uid; !strings.Contains(got, "["+want) && !strings.Contains(got, " "+want) {
					t.Errorf("want %s in %s", want, got)
				}
			}
			wantTS := ts
			if tt.age < 0 {
				wantTS = ts + tt.age
			}
			for _, want := range []string{"ts=" + itoa(int(wantTS)) + " ", "modes=" + tt.modes + " ", `topic="the old place"`} {
				if !strings.Contains(got, want) {
					t.Errorf("want %s in %s", want, got)
				}
			}
			if n := strings.Count(got, ":0"); n != 4 {
				t.Errorf("want 4 members, have %d: %s", n, got)
			}

			// What people see: carol (with the cap) the rename and charlie
			// coming in; bob a part, a join, the topic and the names.
			carol.expect("RENAME #old #new :moving")
			carol.expect(":charlie!~charlie@127.0.0.1 JOIN #new")
			bob.expect(":bob!~bob@127.0.0.1 PART #old :moving")
			bob.expect(":bob!~bob@127.0.0.1 JOIN #new")
			bob.expect(" 332 bob #new :the old place")
			if names := bob.expect(" 353 bob "); !strings.Contains(names, "charlie") || !strings.Contains(names, "alice") {
				t.Errorf("bob's names: %s", names)
			}
			joined := map[string]bool{}
			for len(joined) < 3 {
				line := charlie.expect(" JOIN #new")
				joined[line[1:strings.IndexByte(line, '!')]] = true
			}
			if !joined["alice"] || !joined["bob"] || !joined["carol"] {
				t.Errorf("charlie saw joins of %v", joined)
			}
			alice.expect(":charlie!~charlie@127.0.0.1 JOIN #new")
			// It works as one channel.
			charlie.send("PRIVMSG #new :one channel")
			alice.expect("PRIVMSG #new :one channel")
			carol.expect("PRIVMSG #new :one channel")
		})
	}
}

// The same on a hub between two leaves: the hub, where #new was just
// made, merges and passes the rename on; the far leaf, which heard of
// #new first, merges the same way.
func TestRenameLinkClashTree(t *testing.T) {
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
	for _, n := range []*linkNode{hub, a, b} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	alice := capLogin(t, a.s, "alice", renameCap)
	bob := login(t, b.s, "bob")
	charlie := login(t, hub.s, "charlie")
	for _, n := range []*linkNode{a, b} {
		for _, nick := range []string{"alice", "bob", "charlie"} {
			n.seen(t, nick)
		}
	}
	joinAll("#old", alice)
	alice.send("MODE #old +s")
	alice.expect("MODE #old +s")
	joinLinked(t, b, bob, "#old")
	alice.expect(":bob!")
	var ts int64
	a.s.onLoop(func() { ts = a.s.net.Channel("#old").TS })
	cu := hub.user("charlie").UID

	release := holdLoop(hub)
	alice.send("RENAME #old #new :moving")
	alice.expect("RENAME #old #new :moving")
	release(func() { hub.s.emit(&state.Join{UID: cu, Channel: "#new", TS: ts + 100}) })
	charlie.expect(":charlie!~charlie@127.0.0.1 JOIN #new")

	got := sameChannel(t, "#new", "#old", hub, a, b)
	if strings.Count(got, ":0") != 3 || !strings.Contains(got, "modes=nst ") || !strings.Contains(got, "ts="+itoa(int(ts))+" ") {
		t.Errorf("merged channel: %s", got)
	}
	bob.expect("PART #old :moving")
	bob.expect("JOIN #new")
	charlie.expect(":alice!")
	charlie.send("PRIVMSG #new :one channel")
	alice.expect("PRIVMSG #new :one channel")
	bob.expect("PRIVMSG #new :one channel")
}

// A channel renamed while the servers are split is one channel under the
// new name after the relink, with everyone in it, whichever side renamed
// it: the side that renamed it renames the other's copy too.
func TestRenameLinkDuringSplit(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	alice := capLogin(t, hub.s, "alice", renameCap)
	bob := login(t, leaf.s, "bob")
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	hub.seen(t, "bob")
	leaf.seen(t, "alice")
	joinAll("#a", alice) // alice's channel, renamed on the hub
	alice.send("TOPIC #a :before the split")
	alice.expect("TOPIC #a :before the split")
	joinLinked(t, leaf, bob, "#a")
	alice.expect(":bob!~bob@127.0.0.1 JOIN #a")
	joinAll("#r", bob) // bob's, renamed on the leaf
	joinLinked(t, hub, alice, "#r")
	bob.expect(":alice!~alice@127.0.0.1 JOIN #r")
	var tsA, tsR int64
	hub.s.onLoop(func() { tsA, tsR = hub.s.net.Channel("#a").TS, hub.s.net.Channel("#r").TS })

	op.send("SQUIT leaf.test.local testing")
	waitLink(t, "split", func() bool { return hub.user("bob") == nil && leaf.user("alice") == nil })
	alice.send("RENAME #a #b :moving")
	alice.expect(":alice!~alice@127.0.0.1 RENAME #a #b :moving")
	bob.send("RENAME #r #s :moving too")
	bob.expect(":bob!~bob@127.0.0.1 PART #r :moving too")
	bob.expect(":bob!~bob@127.0.0.1 JOIN #s")
	if got, _ := channelOn(leaf, "#a"); got != "#a" {
		t.Fatal("the rename crossed the split")
	}

	leaf.s.onLoop(func() {
		for _, r := range leaf.s.links.retry {
			r.next = time.Time{}
		}
	})
	waitLink(t, "relink", func() bool { return hub.user("bob") != nil && leaf.user("alice") != nil })
	au, bu := hub.user("alice").UID, hub.user("bob").UID
	for _, tt := range []struct {
		name, old string
		ts        int64
	}{{"#b", "#a", tsA}, {"#s", "#r", tsR}} {
		got := sameChannel(t, tt.name, tt.old, hub, leaf)
		for _, want := range []string{"ts=" + itoa(int(tt.ts)) + " ", ":" + au, ":" + bu} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: want %s in %s", tt.name, want, got)
			}
		}
		if n := strings.Count(got, ":0"); n != 2 {
			t.Errorf("%s: want 2 members, have %d: %s", tt.name, n, got)
		}
	}
	if got := chanState(leaf.s, "#b"); !strings.Contains(got, `topic="before the split"`) {
		t.Errorf("the topic didn't come along: %s", got)
	}

	// What people see: bob his old #a become #b, alice (with the cap) the
	// leaf's #r become #s; each the other come in.
	bob.expect(":alice!~alice@127.0.0.1 JOIN #s")
	bob.expect(":bob!~bob@127.0.0.1 PART #a :moving")
	bob.expect(":bob!~bob@127.0.0.1 JOIN #b")
	bob.expect(" 332 bob #b :before the split")
	if names := bob.expect(" 353 bob "); !strings.Contains(names, "alice") {
		t.Errorf("bob's names: %s", names)
	}
	alice.expect(":bob!~bob@127.0.0.1 JOIN #b")
	alice.expect("RENAME #r #s :moving too")
	alice.expect(":bob!~bob@127.0.0.1 JOIN #s")
	bob.send("PRIVMSG #b :one channel")
	alice.expect("PRIVMSG #b :one channel")
	alice.send("PRIVMSG #s :this one too")
	bob.expect("PRIVMSG #s :this one too")
	if !hub.logHas("Channel #a came back from leaf.test.local") || !leaf.logHas("Channel #r came back from hub.test.local") {
		t.Error("no note to opers of the rename after the split")
	}
}

func TestRenameAware(t *testing.T) {
	for sw, want := range map[string]bool{
		"BeeIRCd-1.5.0":             true,
		"BeeIRCd 1.5.2 (abc1234)":   true,
		"BeeIRCd-2.0.0":             true,
		"BeeIRCd-1.10.1":            true,
		"BeeIRCd-0.0.0-dev":         true,
		"BeeIRCd 0.0.0-dev (dirty)": true,
		"BeeIRCd-1.4.9":             false,
		"BeeIRCd 1.4.0 (abc1234)":   false,
		"BeeIRCd-0.9.0":             false,
		"BeeIRCd":                   false,
		"UnrealIRCd-6.1.8":          false,
		"InspIRCd-4":                false,
		"":                          false,
	} {
		if got := renameAware(sw); got != want {
			t.Errorf("renameAware(%q) = %v, want %v", sw, got, want)
		}
	}
}

// READONLY is the hub's (services) state, but the leaf refuses what would
// be stored too: RENAME and METADATA SET, with the read-only reason.
func TestReadOnlyOnLeaf(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	alice := capLogin(t, hub.s, "alice", "batch", metadataCap)
	operOn(t, hub.s, alice)
	bob := capLogin(t, leaf.s, "bob", renameCap, "batch", metadataCap)
	hub.seen(t, "bob")
	bob.send("PRIVMSG NickServ :REGISTER " + pw)
	bob.expect("bob is now registered")
	bob.send("JOIN #loose")
	bob.expect(" 366 bob #loose ")

	alice.send("PRIVMSG OperServ :SET READONLY ON")
	alice.expect("Services are now in read-only mode")
	waitLink(t, "the leaf knows READONLY", func() bool {
		var on bool
		leaf.s.onLoop(func() { on = leaf.s.readOnly() })
		return on
	})
	bob.send("RENAME #loose #free")
	bob.expect("FAIL RENAME CANNOT_RENAME #loose #free :Services are in read-only mode")
	bob.send("METADATA * SET color #ff0000")
	bob.expect("FAIL METADATA KEY_NO_PERMISSION bob color :Services are in read-only mode")
	bob.send("PRIVMSG NickServ :SET GREET hello")
	bob.expect("read-only mode")

	alice.send("PRIVMSG OperServ :SET READONLY OFF")
	alice.expect("no longer in read-only mode")
	waitLink(t, "the leaf knows READONLY is off", func() bool {
		var on bool
		leaf.s.onLoop(func() { on = leaf.s.readOnly() })
		return !on
	})
	bob.send("RENAME #loose #free")
	bob.expect(" RENAME #loose #free")
}
