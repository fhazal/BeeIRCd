package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// linkedPair is a hub (with services) and a leaf (without), linked and
// synced.
func linkedPair(t *testing.T) (hub, leaf *linkNode) {
	t.Helper()
	hub = newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf = newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	hub.start(t)
	leaf.start(t)
	waitLink(t, "sync", func() bool { return hub.logHas("sync done") && leaf.logHas("sync done") })
	return hub, leaf
}

// seen waits until n knows nick.
func (n *linkNode) seen(t *testing.T, nick string) {
	t.Helper()
	waitLink(t, n.name+" sees "+nick, func() bool { return n.user(nick) != nil })
}

func TestLinkLive(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	leaf.seen(t, "alice")

	alice.send("JOIN #x")
	alice.expect("366")
	bob.send("JOIN #x")
	bob.expect("366")
	alice.expect(":bob!~bob@")

	alice.send("PRIVMSG #x :hello leaf")
	bob.expect(":alice!~alice@127.0.0.1 PRIVMSG #x :hello leaf")
	bob.send("PRIVMSG alice :hi hub")
	alice.expect(":bob!~bob@127.0.0.1 PRIVMSG alice :hi hub")
	bob.send("NOTICE alice :a notice")
	alice.expect("NOTICE alice :a notice")

	alice.send("MODE #x +o bob")
	bob.expect("MODE #x +o bob")
	bob.send("TOPIC #x :leaf topic")
	alice.expect("TOPIC #x :leaf topic")
	bob.send("MODE #x +b *!*@spam.example")
	alice.expect("MODE #x +b *!*@spam.example")
	alice.send("MODE #x +l 20")
	bob.expect("MODE #x +l 20")

	bob.send("NICK bobby")
	alice.expect(":bob!~bob@127.0.0.1 NICK bobby")
	hub.seen(t, "bobby")
	bob.send("AWAY :gone")
	bob.expect("306")
	waitLink(t, "away", func() bool { u := hub.user("bobby"); return u != nil && u.Away == "gone" })
	bob.send("SETNAME :New Name")
	waitLink(t, "setname", func() bool { u := hub.user("bobby"); return u != nil && u.Realname == "New Name" })

	if hs, ls := chanState(hub.s, "#x"), chanState(leaf.s, "#x"); hs != ls {
		t.Errorf("#x differs:\nhub  %s\nleaf %s", hs, ls)
	}

	alice.send("KICK #x bobby :out")
	bob.expect("KICK #x bobby :out")
	bob.send("PART #nowhere")
	alice.send("INVITE bobby #x")
	bob.expect("INVITE bobby #x")

	carol := login(t, leaf.s, "carol")
	carol.send("JOIN #x")
	alice.expect(":carol!")
	carol.send("QUIT :bye now")
	alice.expect("QUIT :Quit: bye now")
	waitLink(t, "carol gone", func() bool { return hub.user("carol") == nil })
}

// A message crosses the hub between two leaves; a nick change and a
// channel reach the far side too.
func TestLinkLiveTree(t *testing.T) {
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
	waitLink(t, "tree", func() bool { return len(a.servers()) == 2 && len(b.servers()) == 2 })
	ann := login(t, a.s, "ann")
	ben := login(t, b.s, "ben")
	a.seen(t, "ben")
	b.seen(t, "ann")
	ann.send("PRIVMSG ben :across")
	ben.expect(":ann!~ann@127.0.0.1 PRIVMSG ben :across")
	ann.send("JOIN #t")
	ann.expect("366")
	ben.send("JOIN #t")
	ann.expect(":ben!")
	ben.send("PRIVMSG #t :to the channel")
	ann.expect("PRIVMSG #t :to the channel")
	ann.send("NICK annie")
	ben.expect("NICK annie")
	if x, y, z := chanState(hub.s, "#t"), chanState(a.s, "#t"), chanState(b.s, "#t"); x != y || y != z {
		t.Errorf("#t differs:\nhub %s\na   %s\nb   %s", x, y, z)
	}
}

// Services run on the hub only; people on the leaf use them over the
// link.
func TestLinkServicesOnHub(t *testing.T) {
	hub, leaf := linkedPair(t)
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	bob.send("NS REGISTER " + pw)
	bob.expect(":NickServ!services@hub.test.local NOTICE bob :bob is now registered to you.")
	bob.expect(" 900 bob ")
	bob.expect("MODE bob +r")
	waitLink(t, "account on the leaf", func() bool { u := leaf.user("bob"); return u != nil && u.Account == "bob" })
	bob.send("PRIVMSG ChanServ :HELP")
	bob.expect(":ChanServ!services@hub.test.local NOTICE bob :")

	// The hub's services kill someone on the leaf.
	carol := login(t, leaf.s, "carol")
	hub.seen(t, "carol")
	hub.s.onLoop(func() { host{hub.s}.Disconnect(hub.s.net.UserByNick("carol").UID, "Go away") })
	carol.expect("KILL carol")
	waitLink(t, "carol gone on the leaf", func() bool { return leaf.user("carol") == nil })
}

// Two servers that both run services don't link.
func TestLinkTwoServices(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	leaf.services = true
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	hub.start(t)
	leaf.start(t)
	waitLink(t, "refusal", func() bool {
		return hub.logHas("only one server per network") || leaf.logHas("only one server per network")
	})
	if len(hub.servers()) != 0 {
		t.Error("linked anyway")
	}
}

// Network bans reach every server, in the burst and live, and each
// server removes its own users they hit.
func TestLinkBans(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	leaf.blocks[0].Autoconnect = false
	hub.start(t)
	leaf.start(t)
	hub.s.onLoop(func() { hub.s.svc.DnsblBan(store.BanZline, "192.0.2.1", "old zline", time.Hour) })
	leaf.linkNow()
	waitLink(t, "sync", func() bool { return hub.logHas("sync done") && leaf.logHas("sync done") })
	has := func(n *linkNode, kind, mask string) bool {
		found := false
		n.s.onLoop(func() {
			for _, b := range n.s.svc.LinkedBans() {
				if b.Kind == kind && b.Mask == mask {
					found = true
				}
			}
		})
		return found
	}
	waitLink(t, "burst ban", func() bool { return has(leaf, store.BanZline, "192.0.2.1") })

	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	hub.s.onLoop(func() { hub.s.svc.DnsblBan(store.BanAkill, "127.0.0.1", "live akill", time.Hour) })
	bob.expect("465")
	bob.expect("Banned: live akill")
	waitLink(t, "bob gone", func() bool { return hub.user("bob") == nil })
	if !has(leaf, store.BanAkill, "*@127.0.0.1") {
		t.Error("leaf lacks the akill")
	}
	// The leaf's copy survives a restart (it is in its database).
	leaf.s.onLoop(func() { leaf.s.svc.ReloadBans() })
	if !has(leaf, store.BanAkill, "*@127.0.0.1") {
		t.Error("akill lost on reload")
	}
	_ = strings.Contains
}

// An oper's KILL of someone on another server reaches them.
func TestLinkKillRemote(t *testing.T) {
	hub, leaf := linkedPair(t)
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	hub.s.onLoop(func() { hub.s.killUser(hub.s.net.UserByNick("bob"), "alice", "enough") })
	if k := bob.expect("KILL bob"); !strings.Contains(k, "enough") {
		t.Errorf("kill %q", k)
	}
	waitLink(t, "bob gone", func() bool { return leaf.user("bob") == nil && hub.user("bob") == nil })
}
