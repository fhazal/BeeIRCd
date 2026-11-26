package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// A linked server sends a host or ident change with characters that break
// nick!user@host (or a very long one): the hub keeps the old one and tells
// its opers; a fine change still goes through.
func TestLinkBadHostChange(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	b := hub.user("bob").UID
	before := hub.user("bob")
	for _, line := range []string{
		":B SETHOST evil@host.example",
		":B SETHOST evil!host.example",
		":B SETHOST *.example",
		":B SETHOST :two words",
		":B SETHOST " + strings.Repeat("a", 65),
		":B SETHOST ctl\x01host",
		":B SETIDENT a@b",
		":B SETIDENT a*b",
		":B SETIDENT waytoolongident",
	} {
		hub.fromLink(t, strings.Replace(line, ":B ", ":"+b+" ", 1))
	}
	time.Sleep(100 * time.Millisecond)
	if u := hub.user("bob"); u.Host != before.Host || u.Ident != before.Ident {
		t.Fatalf("bad change taken: %s@%s", u.Ident, u.Host)
	}
	if !hub.logHas("bad host") || !hub.logHas("bad ident") {
		t.Errorf("no notice about the refused changes")
	}
	if len(hub.servers()) == 0 || hub.servers()[leaf.sid] == "" {
		t.Fatal("link dropped")
	}
	hub.fromLink(t, ":"+b+" SETHOST user/bob_x.example")
	hub.fromLink(t, ":"+b+" SETIDENT bobby")
	waitLink(t, "good change", func() bool {
		u := hub.user("bob")
		return u.Host == "user/bob_x.example" && u.Ident == "bobby"
	})
	hub.fromLink(t, ":"+b+" SETHOST 2001:db8::1")
	waitLink(t, "IPv6 host", func() bool { return hub.user("bob").Host == "2001:db8::1" })
}

// A user introduction with a bad host or ident keeps the link and the
// user, with a safe host and ident in place of the bad ones.
func TestLinkBadHostInUID(t *testing.T) {
	hub, leaf := linkedPair(t)
	l := leaf.sid
	hub.fromLink(t, ":"+l+" UID mallory 1 1794444176 ev@l bad!host.example "+l+"ZZZZZZ 0 +i * * fwAAAQ== :x")
	hub.seen(t, "mallory")
	u := hub.user("mallory")
	if !validLinkHost(u.Host) || !validLinkHost(u.RealHost) || !validLinkIdent(u.Ident) {
		t.Fatalf("bad values kept: %s@%s (real %s)", u.Ident, u.Host, u.RealHost)
	}
	if u.Host != "127.0.0.1" {
		t.Errorf("host %q, want the user's IP", u.Host)
	}
	// cloaked (+x) with a bad vhost: the IP would uncover them, so the
	// valid cloak shows instead
	hub.fromLink(t, ":"+l+" UID trudy 1 1794444176 trudy real.example "+l+"ZZZZZY 0 +ix bad@vhost cloak.example fwAAAQ== :x")
	hub.seen(t, "trudy")
	if u := hub.user("trudy"); u.Host != "cloak.example" {
		t.Errorf("trudy shows %q, want the cloak", u.Host)
	}
	if !hub.logHas("bad host") {
		t.Error("no notice about the bad host")
	}
	if hub.servers()[l] == "" {
		t.Fatal("link dropped")
	}
}

func TestValidLinkHostIdent(t *testing.T) {
	for h, want := range map[string]bool{
		"host.example": true, "user/bob_x": true, "2001:db8::1": true, "a-b.c": true,
		"": false, "a@b": false, "a!b": false, "a*b": false, "a b": false, ":x": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false, "é.example": false,
	} {
		if validLinkHost(h) != want {
			t.Errorf("validLinkHost(%q) = %v", h, !want)
		}
	}
	for id, want := range map[string]bool{
		"bob": true, "~bob": true, "b_o-b.x": true, "1234567890": true,
		"": false, "12345678901": false, "a@b": false, "a!b": false, "a*b": false, "a b": false, ":x": false,
	} {
		if validLinkIdent(id) != want {
			t.Errorf("validLinkIdent(%q) = %v", id, !want)
		}
	}
}

// A user behind a link holding one of our services nicks is killed (on
// both sides), the link stays up and our bot keeps the nick: dropping the
// link only looped on reconnect.
func TestLinkServiceNickCollision(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	leaf.blocks[0].Autoconnect = false
	hub.start(t)
	leaf.start(t)
	// the leaf keeps services nicks for services, so rename by hand (as a
	// network with its own services would have it)
	fake := login(t, leaf.s, "fakens")
	leaf.s.onLoop(func() {
		u := leaf.s.net.UserByNick("fakens")
		leaf.s.emit(&state.NickChange{UID: u.UID, Nick: "NickServ", TS: time.Now().Unix()})
	})
	fake.expect("NICK NickServ")
	bot := hub.user("NickServ")
	if bot == nil || !bot.Service {
		t.Fatal("no NickServ bot on the hub")
	}
	leaf.linkNow()
	waitLink(t, "both synced", func() bool {
		return hub.logHas("Received end of netburst from") && leaf.logHas("Received end of netburst from")
	})
	fake.expect("KILL")
	waitLink(t, "fake NickServ gone on the leaf", func() bool {
		u := leaf.user("NickServ")
		return u != nil && u.UID == bot.UID
	})
	if u := hub.user("NickServ"); u == nil || u.UID != bot.UID {
		t.Fatalf("hub NickServ is %+v, want our bot %s", u, bot.UID)
	}

	// live: a user there renames to ChanServ
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	b := hub.user("bob").UID
	cs := hub.user("ChanServ")
	hub.fromLink(t, ":"+b+" NICK ChanServ 1794444176")
	waitLink(t, "bob killed on both", func() bool { return hub.user(b) == nil && leaf.user("bob") == nil })
	if u := hub.user("ChanServ"); u == nil || u.UID != cs.UID {
		t.Fatalf("hub ChanServ is %+v, want our bot", u)
	}
	time.Sleep(200 * time.Millisecond)
	if hub.servers()[leaf.sid] == "" {
		t.Fatal("link dropped")
	}
}

// Another services package's bot (+S) on our services nick still drops
// the link: killing it would only loop, as its services bring it back.
func TestLinkServiceBotCollisionDrops(t *testing.T) {
	hub, leaf := linkedPair(t)
	l := leaf.sid
	bot := hub.user("NickServ")
	hub.fromLink(t, ":"+l+" UID NickServ 1 1794444176 services leaf.test.local "+l+"ZZZZZZ 0 +iS * * fwAAAQ== :Nickname Services")
	waitLink(t, "link dropped", func() bool { return hub.logHas("has its own services") })
	if u := hub.user("NickServ"); u == nil || u.UID != bot.UID {
		t.Fatalf("hub NickServ is %+v, want our bot", u)
	}
}

// A link dropped for two sets of services waits 10 minutes before the next
// autoconnect try and tells opers once an hour, not on every try.
func TestLinkTwoServicesBacksOff(t *testing.T) {
	hub, leaf := linkedPair(t)
	l := leaf.sid
	count := func() int { return strings.Count(hub.log.String(), "runs its own services") }
	var next0 time.Time
	hub.s.onLoop(func() { next0 = time.Now() })
	hub.fromLink(t, ":"+l+" UID NickServ 1 1794444176 services leaf.test.local "+l+"ZZZZZZ 0 +iS * * fwAAAQ== :Nickname Services")
	waitLink(t, "link dropped", func() bool { return count() == 1 })
	var r *linkRetry
	hub.s.onLoop(func() { r = hub.s.links.retry["leaf.test.local"] })
	if r == nil {
		t.Fatal("no retry state for leaf")
	}
	hub.s.onLoop(func() {
		if d := r.next.Sub(next0); d < linkServicesRetry-time.Minute || d > linkServicesRetry+time.Minute {
			t.Errorf("next try in %v, want about %v", d, linkServicesRetry)
		}
		// a second drop within the hour: no new notice, still 10 minutes
		r.next = time.Now()
		hub.s.dropServicesLink(nil, "leaf.test.local", "NickServ", "x")
		if d := time.Until(r.next); d < linkServicesRetry-time.Minute {
			t.Errorf("second drop: next try in %v", d)
		}
	})
	if n := count(); n != 1 {
		t.Fatalf("%d notices, want 1", n)
	}
	// an hour later it speaks up again
	hub.s.onLoop(func() {
		r.svcNoticed = time.Now().Add(-linkServicesNoticeEvery - time.Second)
		hub.s.dropServicesLink(nil, "leaf.test.local", "NickServ", "x")
	})
	if n := count(); n != 2 {
		t.Fatalf("%d notices, want 2", n)
	}
	// a rehash tries right away again
	hub.s.onLoop(func() {
		hub.s.linkRehash()
		if r.svcNick != "" || time.Until(r.next) > time.Second {
			t.Errorf("rehash left svcNick=%q next in %v", r.svcNick, time.Until(r.next))
		}
	})
}

// A link that comes up and finishes its burst clears the two-services wait.
func TestLinkTwoServicesClearedOnBurst(t *testing.T) {
	hub, leaf := linkedPair(t)
	_ = leaf
	hub.s.onLoop(func() {
		r := hub.s.links.retry["leaf.test.local"]
		if r == nil {
			r = &linkRetry{}
			hub.s.links.retry["leaf.test.local"] = r
		}
		r.svcNick, r.svcNoticed = "NickServ", time.Now()
	})
	hub.s.onLoop(func() {
		hub.s.linkServicesOK("leaf.test.local")
		if r := hub.s.links.retry["leaf.test.local"]; r.svcNick != "" || !r.svcNoticed.IsZero() {
			t.Errorf("state kept: %+v", r)
		}
	})
}
