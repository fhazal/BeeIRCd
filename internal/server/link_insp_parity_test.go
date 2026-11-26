package server

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// recBridge stands in for the InspIRCd bridge: what goes out is kept (and
// not sent), what comes in runs as it is.
type recBridge struct {
	mu  sync.Mutex
	out []string
}

func (r *recBridge) Out(line string) []string {
	r.mu.Lock()
	r.out = append(r.out, line)
	r.mu.Unlock()
	return nil
}

func (r *recBridge) In(line string) (run, reply []string) { return []string{line}, nil }

// sent says whether a line containing want went out.
func (r *recBridge) sent(want string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.out {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// bridged makes the hub's link to the leaf an InspIRCd one, with rec as
// its bridge.
func bridged(t *testing.T, hub *linkNode) *recBridge {
	t.Helper()
	rec := &recBridge{}
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.bridge = rec
		}
	})
	return rec
}

// Server queries towards a server behind an InspIRCd link: MOTD and WHOIS
// go out (the bridge turns them into InspIRCd's form); VERSION and TRACE,
// which InspIRCd 4 has no remote form of, are answered here with their
// ending numeric; WHOIS for someone not behind that link is answered
// here; InspIRCd's IDLE answer becomes the whole WHOIS, with the idle time.
func TestLinkInspQueries(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	rec := bridged(t, hub)
	aliceUID, bobUID := hub.user("alice").UID, hub.user("bob").UID

	alice.send("VERSION leaf.test.local")
	alice.expect("VERSION can't reach leaf.test.local")
	alice.expectNumeric("402", "leaf.test.local")
	alice.send("TRACE leaf.test.local")
	alice.expect("TRACE can't reach leaf.test.local")
	alice.expectNumeric("262", "leaf.test.local")

	alice.send("MOTD leaf.test.local")
	alice.send("WHOIS leaf.test.local bob")
	alice.send("WHOIS leaf.test.local alice")
	alice.expectNumeric("311", "alice")
	alice.expectNumeric("318", "alice")
	if !rec.sent(":"+aliceUID+" MOTD :leaf.test.local") || !rec.sent(":"+aliceUID+" WHOIS 0LF :bob") {
		t.Errorf("MOTD/WHOIS not sent on: %q", rec.out)
	}
	if rec.sent("WHOIS 0LF :alice") {
		t.Error("WHOIS for someone here went to the InspIRCd link")
	}

	hub.fromLink(t, ":"+bobUID+" IDLE "+aliceUID+" 1700000000 42")
	alice.expectNumeric("311", "bob")
	alice.expectNumeric("317", "bob", "42", "1700000000")
	alice.expectNumeric("318", "bob")

	// Their user asks us: the answer goes back on the link.
	hub.fromLink(t, ":"+bobUID+" TIME :hub.test.local")
	waitLink(t, "TIME answered", func() bool { return rec.sent(" 391 " + bobUID + " hub.test.local") })
}

// The link hook gives our client's own signon and idle time (for IDLE
// from InspIRCd), nothing for anyone else.
func TestLinkIdleHook(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, hub.s, "alice")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	var on, idle int64
	var ok, bobOK bool
	var want int64
	aliceUID, bobUID := hub.user("alice").UID, hub.user("bob").UID
	hub.s.onLoop(func() {
		c := hub.s.clients[aliceUID]
		c.lastMsg = time.Now().Add(-90 * time.Second)
		want = c.user.SignOn.Unix()
		on, idle, ok = hub.s.linkLocal().Idle(aliceUID)
		_, _, bobOK = hub.s.linkLocal().Idle(bobUID)
	})
	if !ok || on != want || idle < 89 || idle > 95 {
		t.Errorf("alice: signon %d (want %d) idle %d ok %v", on, want, idle, ok)
	}
	if bobOK {
		t.Error("an idle time for someone on another server")
	}
}

// A KNOCK here goes out on an InspIRCd link too (the bridge turns it into
// ENCAP * KNOCK), and one from their side reaches our ops.
func TestLinkInspKnock(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	carol := login(t, hub.s, "carol")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	alice.send("JOIN #hive")
	alice.expectNumeric("366")
	alice.send("MODE #hive +i")
	alice.expect("MODE #hive +i")
	rec := bridged(t, hub)
	carolUID, bobUID := hub.user("carol").UID, hub.user("bob").UID

	carol.send("KNOCK #hive :let me in")
	alice.expect("[Knock] by carol!")
	waitLink(t, "KNOCK sent on", func() bool { return rec.sent(":" + carolUID + " KNOCK #hive :let me in") })

	hub.fromLink(t, ":"+bobUID+" KNOCK #hive :me too")
	alice.expect("[Knock] by bob!")
}

// A server notice from a server behind an InspIRCd link reaches our opers
// with that letter, as "From <server>: text"; one from an UnrealIRCd link
// (other letters) or from a user is ignored.
func TestLinkInspSnotice(t *testing.T) {
	hub, _ := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	op.send("MODE op +s +fL")
	op.send("PING :umode")
	op.expect(" :umode")
	hub.fromLink(t, ":0LF SENDSNO f :not from a bridge")
	bridged(t, hub)
	hub.fromLink(t, ":0LF SENDSNO f :someone was filtered")
	if l := op.expect("someone was filtered"); !strings.Contains(l, "*** FILTER: From leaf.test.local: someone was filtered") {
		t.Errorf("got %q", l)
	}
	hub.fromLink(t, ":0LF SENDSNO L :Netsplit complete")
	op.expect("REMOTELINK: From leaf.test.local: Netsplit complete")
	op.quiet("not from a bridge")
}

// SDESC goes out on an InspIRCd link (the bridge makes it SINFO desc), and
// their server's new description (SDESC from that server) is kept here.
func TestLinkInspSdesc(t *testing.T) {
	hub, _ := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	rec := bridged(t, hub)
	opUID := hub.user("op").UID
	op.send("SDESC :Hub of the hive")
	waitLink(t, "SDESC sent on", func() bool { return rec.sent(":" + opUID + " SDESC :Hub of the hive") })

	hub.fromLink(t, ":0LF SDESC :Leaf of the hive")
	waitLink(t, "their description kept", func() bool {
		desc := ""
		hub.s.onLoop(func() {
			if r := hub.s.links.servers["0LF"]; r != nil {
				desc = r.desc
			}
		})
		return desc == "Leaf of the hive"
	})
}

// UNINVITE of someone behind an InspIRCd link goes out on it, and one from
// their side reaches our user.
func TestLinkInspUninvite(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	carol := login(t, hub.s, "carol")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	alice.send("JOIN #hive")
	alice.expectNumeric("366")
	rec := bridged(t, hub)
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.peer.Bee = false // as a real InspIRCd link
		}
	})
	aliceUID, bobUID, carolUID := hub.user("alice").UID, hub.user("bob").UID, hub.user("carol").UID

	alice.send("UNINVITE bob #hive")
	alice.expectNumeric("494", "#hive", "bob")
	waitLink(t, "UNINVITE sent on", func() bool { return rec.sent(":" + aliceUID + " UNINVITE " + bobUID + " #hive") })

	alice.send("INVITE carol #hive")
	carol.expect("INVITE carol #hive")
	hub.fromLink(t, ":"+bobUID+" UNINVITE "+carolUID+" #hive")
	carol.expect(" 493 carol :You were uninvited from #hive by bob")
}

// REHASH and CONNECT for a server behind an InspIRCd link go out on it
// (the bridge makes them InspIRCd's REHASH and RCONNECT); INFO and
// LUSERS, which InspIRCd 4 answers only for its own users, end with 402.
func TestLinkInspRoutedQueries(t *testing.T) {
	hub, _ := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	rec := bridged(t, hub)
	opUID := hub.user("op").UID

	op.send("REHASH leaf.test.local")
	op.send("CONNECT other.test.local 7000 leaf.test.local")
	waitLink(t, "REHASH and CONNECT sent on", func() bool {
		return rec.sent(":"+opUID+" REHASH :leaf.test.local") && rec.sent(":"+opUID+" CONNECT other.test.local 7000 :leaf.test.local")
	})
	for _, cmd := range []string{"INFO", "LUSERS"} {
		op.send(cmd + " leaf.test.local")
		op.expect(cmd + " can't reach leaf.test.local")
		op.expectNumeric("402", "leaf.test.local")
	}
}

// SAMODE on someone behind an InspIRCd link goes out as services' SVS2MODE
// (the bridge makes it InspIRCd's server MODE), and $mask messages cross
// the link both ways.
func TestLinkInspSamodeServerMask(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	home := login(t, hub.s, "home")
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	rec := bridged(t, hub)
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.peer.Bee = false // as a real InspIRCd link
		}
	})
	opUID, bobUID := hub.user("op").UID, hub.user("bob").UID

	op.send("SAMODE bob +i")
	op.send("NOTICE $*.test.local :hello everyone")
	home.expect("NOTICE $*.test.local :hello everyone")
	waitLink(t, "SVS2MODE and the $mask NOTICE sent on", func() bool {
		return rec.sent(" SVS2MODE "+bobUID+" +i") && rec.sent(":"+opUID+" NOTICE $*.test.local :hello everyone")
	})

	hub.fromLink(t, ":"+bobUID+" NOTICE $hub.test.local :from their side")
	home.expect("NOTICE $hub.test.local :from their side")
}
