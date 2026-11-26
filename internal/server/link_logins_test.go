package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// lastHost is NickServ's "Last host" of account on n.
func (n *linkNode) lastHost(account string) string {
	var h string
	n.s.onLoop(func() {
		if a, _ := n.s.store.AccountByNick(account); a != nil {
			h = a.LastHost
		}
	})
	return h
}

// relink splits leaf off hub (an oper's SQUIT) and waits until it is back
// with nick on it.
func relink(t *testing.T, hub, leaf *linkNode, op *tc, nick string) {
	t.Helper()
	op.send("SQUIT leaf.test.local testing")
	waitLink(t, "leaf split", func() bool { return hub.user(nick) == nil })
	leaf.s.onLoop(func() {
		for _, r := range leaf.s.links.retry {
			r.next = time.Time{}
		}
	})
	waitLink(t, "leaf back", func() bool { return hub.user(nick) != nil })
}

// A server that is not the services server put someone's account in its
// UID lines (QA, 1.7.1): the users came in logged in to it. Now the hub
// with the services keeps them logged out, tells the link so, tells the
// opers once and leaves the account's "Last host" alone. The same goes for
// a user the link bursts again after a split.
func TestLinkLoginSpoofIgnored(t *testing.T) {
	hub, leaf := linkedPair(t)
	registeredOn(t, hub.s, "bee")
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	op.send("MODE op +s")
	op.send("PING :umode")
	op.expect(" :umode")
	before := hub.lastHost("bee")
	hub.s.onLoop(func() { hub.s.links.debug = true })

	ts := time.Now().Unix()
	hub.fromLink(t, fmt.Sprintf(":0LF UID acctspoof 1 %d qa fake.host 0LFZZZZZ4 bee +i * * fwAAAQ== :x", ts))
	hub.fromLink(t, fmt.Sprintf(":0LF UID acctspoof2 1 %d qa fake.host 0LFZZZZZ5 bee +i * * fwAAAQ== :x", ts))
	for _, nick := range []string{"acctspoof", "acctspoof2"} {
		u := hub.user(nick)
		if u == nil {
			t.Fatalf("%s did not come in", nick)
		}
		if u.Account != "" {
			t.Errorf("%s came in logged in to %q", nick, u.Account)
		}
	}
	op.expect("*** LINK: Ignored a login to bee for acctspoof from leaf.test.local")
	op.quiet("Ignored a login") // one notice, not one per user
	for _, want := range []string{
		"ignored a login to bee for acctspoof (0LFZZZZZ4)",
		"ignored a login to bee for acctspoof2 (0LFZZZZZ5)",
		"SVSLOGIN * 0LFZZZZZ4 0",
		"SVSLOGIN * 0LFZZZZZ5 0",
	} {
		if !hub.logHas(want) {
			t.Errorf("no %q in the hub's log", want)
		}
	}
	if after := hub.lastHost("bee"); after != before || strings.Contains(after, "fake.host") {
		t.Errorf("last host went from %q to %q", before, after)
	}

	// A login our services gave to an older account "bee" (dropped, then
	// registered again by someone else) is not a login to this one.
	hub.s.onLoop(func() {
		hub.s.grantLogin("0LFZZZZZ6", "bee", true)
		g := hub.s.links.logins["0LFZZZZZ6"]
		g.id++
		hub.s.links.logins["0LFZZZZZ6"] = g
	})
	hub.fromLink(t, fmt.Sprintf(":0LF UID acctspoof3 1 %d qa fake.host 0LFZZZZZ6 bee +i * * fwAAAQ== :x", ts))
	if u := hub.user("acctspoof3"); u == nil || u.Account != "" {
		t.Errorf("acctspoof3 came in as %+v", u)
	}

	// The leaf itself thinks bob is logged in to bee (it never got that
	// from the services): after a split its burst says so.
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	leaf.s.onLoop(func() {
		leaf.s.emitQuiet(&state.Login{UID: leaf.s.net.UserByNick("bob").UID, Account: "bee"})
	})
	relink(t, hub, leaf, op, "bob")
	if a := hub.user("bob").Account; a != "" {
		t.Errorf("bob is logged in to %q on the hub", a)
	}
	waitLink(t, "the leaf takes the logout", func() bool { return leaf.user("bob").Account == "" })
	if !hub.logHas("ignored a login to bee for bob (") {
		t.Error("the hub did not log the drop for bob")
	}
}

// Logins our services gave to people on a leaf, with NickServ and with
// SASL, come back with them after a split.
func TestLinkLoginSurvivesSplit(t *testing.T) {
	hub, leaf := linkedPair(t)
	dan := login(t, leaf.s, "dan")
	hub.seen(t, "dan")
	dan.send("NS REGISTER " + pw)
	dan.expect("is now registered to you.")

	c := raw(t, leaf.s)
	c.nick = "*"
	c.send("CAP REQ :sasl")
	c.send("NICK sam")
	c.send("USER sam 0 * :Sam")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + saslPlain("dan", pw))
	c.expect(" 903 ")
	c.send("CAP END")
	c.expect(" 001 sam ")

	logged := func(n *linkNode, nick string) bool { u := n.user(nick); return u != nil && u.Account == "dan" }
	waitLink(t, "both logged in on both", func() bool {
		return logged(hub, "dan") && logged(hub, "sam") && logged(leaf, "dan") && logged(leaf, "sam")
	})

	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	relink(t, hub, leaf, op, "dan")
	waitLink(t, "sam back", func() bool { return hub.user("sam") != nil })
	for _, nick := range []string{"dan", "sam"} {
		if !logged(hub, nick) {
			t.Errorf("%s lost the login on the hub: %+v", nick, hub.user(nick))
		}
		if !logged(leaf, nick) {
			t.Errorf("%s lost the login on the leaf: %+v", nick, leaf.user(nick))
		}
	}
	if hub.logHas("ignored a login") {
		t.Errorf("the hub dropped a real login:\n%s", hub.log.String())
	}
}

// passBridge hands lines on as they are. On the hub's link it stands for
// the InspIRCd bridge, so the hub treats the link as an InspIRCd one.
type passBridge struct{}

func (passBridge) Out(line string) []string             { return []string{line} }
func (passBridge) In(line string) (run, reply []string) { return []string{line}, nil }

// inspBridgeTo is the hub's real InspIRCd bridge to peer (sid), after the
// handshake.
func inspBridgeTo(t *testing.T, peer, sid string) link.Bridge {
	t.Helper()
	const fp = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCA="
	blk := config.LinkServer{Name: peer, Host: "h", Fingerprint: fp, Password: "pw", Protocol: "inspircd"}
	me := link.Local{
		Name: "hub.test.local", SID: "0HB", Description: "hub", Network: "BeeNet", Version: "1.7.1-test",
		ChanModes: "beI,k,l,imnpst", UserModes: "iowx", Prefix: "(ov)@+", Casemapping: "ascii", Booted: 1,
		Now:   func() int64 { return time.Now().Unix() },
		Known: func(x string) bool { return strings.EqualFold(x, "hub.test.local") || x == "0HB" },
		Block: func(string) *config.LinkServer { return &blk },
	}
	h := link.NewInsp(me, true, &blk, fp)
	h.Start()
	for _, line := range []string{
		"CAPAB START 1206",
		"CAPAB CAPABILITIES :CASEMAPPING=ascii MAXNICK=30 PROTOCOL=1206",
		"CAPAB CHANMODES :list:ban=b simple:inviteonly=i param:key=k param-set:limit=l prefix:30000:op=@o prefix:10000:voice=+v simple:noextmsg=n simple:topiclock=t simple:moderated=m simple:secret=s",
		"CAPAB USERMODES :simple:invisible=i simple:oper=o param-set:snomask=s simple:cloak=x simple:wallops=w",
		"CAPAB END",
		"SERVER " + peer + " pw " + sid + " :InspIRCd test",
	} {
		m, err := irc.Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Handle(m); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
	if !h.Done() {
		t.Fatal("the InspIRCd handshake did not finish")
	}
	return h.(link.Bridger).Bridge()
}

// InspIRCd sends the account as METADATA accountname, in its burst and
// on its own later. From a server that is not the services server it is
// ignored unless our services gave that login.
func TestLinkInspAccountnameIgnored(t *testing.T) {
	hub, _ := linkedPair(t)
	registeredOn(t, hub.s, "bee")
	b := inspBridgeTo(t, "leaf.test.local", "0LF")
	var l *linkConn
	hub.s.onLoop(func() {
		for c := range hub.s.links.conns {
			l = c
		}
		l.bridge = passBridge{}
		hub.s.links.debug = true
		hub.s.grantLogin("0LFZZZZI2", "bee", true) // as SASL with our services does
	})
	feed := func(line string) {
		t.Helper()
		run, _ := b.In(line)
		hub.s.onLoop(func() {
			for _, x := range run {
				m, err := irc.Parse(x)
				if err != nil {
					t.Errorf("bridge gave %q: %v", x, err)
					continue
				}
				hub.s.linkUpLine(l, m)
			}
		})
	}

	ts := time.Now().Unix()
	feed(fmt.Sprintf(":0LF UID 0LFZZZZI1 %d inspy real.host real.host inspy inspy 192.0.2.1 %d +i :Insp User", ts, ts))
	feed(":0LF METADATA 0LFZZZZI1 accountname :bee")
	feed(fmt.Sprintf(":0LF UID 0LFZZZZI2 %d inspy2 real.host real.host inspy inspy 192.0.2.2 %d +i :Insp User", ts, ts))
	feed(":0LF METADATA 0LFZZZZI2 accountname :bee")
	feed(":0LF METADATA #ih mlock :") // ends the last UID's METADATA lines
	u, u2 := hub.user("inspy"), hub.user("inspy2")
	if u == nil || u2 == nil {
		t.Fatalf("users did not come in: %v %v", u, u2)
	}
	if u.Account != "" {
		t.Errorf("inspy came in logged in to %q", u.Account)
	}
	if u2.Account != "bee" {
		t.Errorf("inspy2 (our services' login) came in as %q", u2.Account)
	}

	// After the burst the account comes on its own line.
	feed(":0LF METADATA 0LFZZZZI1 accountname :bee")
	if a := hub.user("inspy").Account; a != "" {
		t.Errorf("inspy is logged in to %q", a)
	}
	if n := strings.Count(hub.log.String(), "ignored a login to bee for inspy (0LFZZZZI1)"); n != 2 {
		t.Errorf("logged %d drops for inspy, want 2:\n%s", n, hub.log.String())
	}
	if !hub.logHas("SVSLOGIN * 0LFZZZZI1 0") {
		t.Error("no logout went to the link")
	}
	if hub.logHas("for inspy2 (") {
		t.Error("inspy2's login was dropped")
	}
}

// loggedInPeople is a hub and a leaf with dan (NickServ) and sam (SASL) on
// the leaf, both logged in to dan's account, on both servers. It returns
// their UIDs.
func loggedInPeople(t *testing.T) (hub, leaf *linkNode, danUID, samUID string) {
	t.Helper()
	hub, leaf = linkedPair(t)
	dan := login(t, leaf.s, "dan")
	hub.seen(t, "dan")
	dan.send("NS REGISTER " + pw)
	dan.expect("is now registered to you.")
	c := raw(t, leaf.s)
	c.nick = "*"
	c.send("CAP REQ :sasl")
	c.send("NICK sam")
	c.send("USER sam 0 * :Sam")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + saslPlain("dan", pw))
	c.expect(" 903 ")
	c.send("CAP END")
	c.expect(" 001 sam ")
	logged := func(n *linkNode, nick string) bool { u := n.user(nick); return u != nil && u.Account == "dan" }
	waitLink(t, "both logged in on both", func() bool {
		return logged(hub, "dan") && logged(hub, "sam") && logged(leaf, "dan") && logged(leaf, "sam")
	})
	return hub, leaf, hub.user("dan").UID, hub.user("sam").UID
}

// coldRestart stops the hub for good and starts it again on the same
// database (edit may change the saved records in between); the leaf links
// again and the test waits for its people to be back on the hub.
func coldRestart(t *testing.T, hub, leaf *linkNode, edit func(*store.Store)) {
	t.Helper()
	stopServer(hub.s)
	waitLink(t, "leaf lost the hub", func() bool { return len(leaf.servers()) == 0 })
	if edit != nil {
		db, err := store.Open(hub.db)
		if err != nil {
			t.Fatal(err)
		}
		edit(db)
		db.Close()
	}
	hub.start(t)
	waitLink(t, "leaf back", func() bool {
		leaf.s.onLoop(func() {
			for _, r := range leaf.s.links.retry {
				r.next = time.Time{}
			}
		})
		return hub.user("sam") != nil && hub.user("dan") != nil
	})
}

// Logins our services gave to people on a leaf come back with them after
// the hub itself stopped and started again (no hand-over): the leaf's UIDs
// are the same, so the burst matches the saved records.
func TestLinkLoginSurvivesColdRestart(t *testing.T) {
	hub, leaf, danUID, samUID := loggedInPeople(t)
	coldRestart(t, hub, leaf, func(db *store.Store) {
		all, err := db.LinkLogins()
		if err != nil || len(all) != 2 {
			t.Fatalf("saved at stop: %+v, %v", all, err)
		}
		for _, l := range all {
			if (l.UID != danUID && l.UID != samUID) || l.Account != "dan" || l.AccountID == 0 {
				t.Errorf("saved record %+v", l)
			}
		}
	})
	for _, nick := range []string{"dan", "sam"} {
		if u := hub.user(nick); u == nil || u.Account != "dan" {
			t.Errorf("%s lost the login on the hub after a restart: %+v", nick, u)
		}
		if u := leaf.user(nick); u == nil || u.Account != "dan" {
			t.Errorf("%s lost the login on the leaf after a restart: %+v", nick, u)
		}
	}
	if hub.logHas("ignored a login") {
		t.Errorf("the hub dropped a real login:\n%s", hub.log.String())
	}
	// Back on the network: no longer waiting to expire.
	hub.s.onLoop(func() {
		for uid, g := range hub.s.links.logins {
			if !g.gone.IsZero() {
				t.Errorf("%s still counted as gone: %+v", uid, g)
			}
		}
	})
}

// After a restart, a record that is too old, or belongs to an account that
// was dropped and registered again, does not give a login back.
func TestLinkLoginColdRestartRefuses(t *testing.T) {
	hub, leaf, danUID, samUID := loggedInPeople(t)
	coldRestart(t, hub, leaf, func(db *store.Store) {
		all, _ := db.LinkLogins()
		var save []store.LinkLogin
		for _, l := range all {
			switch l.UID {
			case samUID:
				l.Gone = time.Now().Add(-loginGrantKeep - time.Hour) // left long ago
			case danUID:
				l.AccountID += 100 // another account of that name by now
			}
			save = append(save, l)
		}
		if err := db.SaveLinkLogins(save, nil, false); err != nil {
			t.Fatal(err)
		}
	})
	for _, nick := range []string{"dan", "sam"} {
		if u := hub.user(nick); u == nil || u.Account != "" {
			t.Errorf("%s got a login back that should be refused: %+v", nick, u)
		}
	}
	waitLink(t, "the leaf takes the logouts", func() bool {
		d, s := leaf.user("dan"), leaf.user("sam")
		return d != nil && s != nil && d.Account == "" && s.Account == ""
	})
	for _, uid := range []string{danUID, samUID} {
		if !hub.logHas("(" + uid + "): our services did not log them in") {
			t.Errorf("no log line for %s:\n%s", uid, hub.log.String())
		}
	}
	// The expired record is gone from the database after the next write.
	hub.s.onLoop(func() {
		if _, ok := hub.s.links.logins[samUID]; ok {
			t.Errorf("expired record for %s kept in memory", samUID)
		}
		hub.s.flushLogins(time.Now(), true)
	})
	all, err := hub.s.store.LinkLogins()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range all {
		if l.UID == samUID {
			t.Errorf("expired record for %s kept in the database", samUID)
		}
	}
}

// What the server loads at start: records that were on the network count as
// gone from now, ones kept longer than the wait are dropped (and deleted
// from the database), the id and the account are kept.
func TestLoginsLoadedAtColdStart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "services.db")
	useDB := func(c *config.Config) { c.Services.Database = db }
	s1 := testServer(t, useDB)
	now := time.Now()
	s1.onLoop(func() {
		s1.setLogin("0LFAAAAAA", loginGrant{account: "Bee", id: 3})
		s1.setLogin("0LFAAAAAB", loginGrant{account: "Ann", id: 4, gone: now.Add(-time.Hour)})
		s1.setLogin("0LFAAAAAC", loginGrant{account: "Cy", id: 5, gone: now.Add(-loginGrantKeep - time.Hour)})
	})
	stopServer(s1) // the stop writes them
	s2 := testServer(t, useDB)
	s2.onLoop(func() {
		g := s2.links.logins
		if len(g) != 2 {
			t.Errorf("loaded %+v, want Bee and Ann", g)
		}
		if a := g["0LFAAAAAA"]; a.account != "Bee" || a.id != 3 || a.gone.IsZero() || time.Since(a.gone) > time.Minute {
			t.Errorf("Bee: %+v", a)
		}
		if a := g["0LFAAAAAB"]; a.account != "Ann" || a.id != 4 || time.Since(a.gone) < 59*time.Minute || time.Since(a.gone) > 61*time.Minute {
			t.Errorf("Ann: %+v", a)
		}
		if _, ok := g["0LFAAAAAC"]; ok {
			t.Error("Cy is past the wait but was loaded")
		}
		s2.flushLogins(time.Now(), true)
	})
	all, err := s2.store.LinkLogins()
	if err != nil || len(all) != 2 {
		t.Fatalf("database after load: %+v, %v", all, err)
	}
	for _, l := range all {
		if l.Gone.IsZero() {
			t.Errorf("%s saved as on the network after a cold start", l.UID)
		}
	}
}

// A netsplit that takes many people at once is written in one batch, and
// the database matches the memory after it.
func TestLoginsNetsplitWrittenInOneBatch(t *testing.T) {
	s := testServer(t, nil)
	const n = 5000
	uid := func(i int) string { return fmt.Sprintf("0LF%06d", i) }
	same := func(when string) {
		t.Helper()
		all, err := s.store.LinkLogins()
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]store.LinkLogin{}
		for _, l := range all {
			got[l.UID] = l
		}
		if len(got) != len(s.links.logins) {
			t.Errorf("%s: %d records in the database, %d in memory", when, len(got), len(s.links.logins))
		}
		for u, g := range s.links.logins {
			l, ok := got[u]
			if !ok || l.Account != g.account || l.AccountID != g.id || l.Gone.IsZero() != g.gone.IsZero() ||
				(!g.gone.IsZero() && l.Gone.Unix() != g.gone.Unix()) {
				t.Errorf("%s: %s is %+v in the database, %+v in memory", when, u, l, g)
				return
			}
		}
	}
	s.onLoop(func() {
		for i := range n {
			s.grantLogin(uid(i), "Bee", false)
		}
		if len(s.links.loginsDirty) != n {
			t.Errorf("%d changes noted, want %d", len(s.links.loginsDirty), n)
		}
		s.flushLogins(time.Now(), false)
		if len(s.links.loginsDirty) != 0 {
			t.Errorf("changes left after a flush: %d", len(s.links.loginsDirty))
		}
		same("after granting")
		// The split takes everyone, then some really quit.
		s.netsplit = &netjoinBatch{}
		for i := range n {
			s.noteLogins(&state.UserQuit{UID: uid(i), Reason: "split"}, nil)
		}
		s.netsplit = nil
		for i := 0; i < n; i += 5 {
			s.noteLogins(&state.UserQuit{UID: uid(i), Reason: "quit"}, nil)
		}
		s.flushLogins(time.Now(), false)
		same("after the split")
		if len(s.links.logins) != n-n/5 {
			t.Errorf("%d records after the split, want %d", len(s.links.logins), n-n/5)
		}
		for _, g := range s.links.logins {
			if g.gone.IsZero() {
				t.Error("a record not marked gone after the split")
				break
			}
		}
	})
}
