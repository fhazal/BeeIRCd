package server

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// loginBatch registers nick with the batch capability.
func loginBatch(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("CAP LS 302")
	c.send("CAP REQ :batch")
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick + " Real")
	c.send("CAP END")
	c.expect(" MODE " + nick + " +i")
	return c
}

// chanState writes a channel the same way on any server: TS, modes,
// members with ranks (by UID), lists, topic.
func chanState(s *Server, name string) string {
	var out string
	s.onLoop(func() {
		ch := s.net.Channel(name)
		if ch == nil {
			out = "none"
			return
		}
		var mem []string
		for uid, m := range ch.Members {
			mem = append(mem, rankLetters(m.Ranks)+":"+uid)
		}
		sort.Strings(mem)
		var lists []string
		for _, mode := range []byte{'b', 'e', 'I'} {
			for _, e := range ch.Lists[mode] {
				lists = append(lists, string(mode)+e.Mask)
			}
		}
		out = fmt.Sprintf("ts=%d modes=%s key=%q limit=%d members=%v lists=%v topic=%q",
			ch.TS, sortedModeString(ch.Modes), ch.Key, ch.Limit, mem, lists, ch.Topic)
	})
	return out
}

func sortedModeString(m map[byte]bool) string {
	var b []byte
	for k, on := range m {
		if on {
			b = append(b, k)
		}
	}
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return string(b)
}

func (n *linkNode) user(nick string) *state.User {
	var u *state.User
	n.s.onLoop(func() {
		if v := n.s.net.UserByNick(nick); v != nil {
			u = v.Copy()
		}
	})
	return u
}

// linkLater lets the node dial its first block only when told to.
func (n *linkNode) linkNow() {
	n.s.onLoop(func() {
		n.s.cfg.Link.Servers[0].Autoconnect = true
		n.s.linkTick(time.Now())
	})
}

func TestLinkBurst(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	leaf.blocks[0].Autoconnect = false
	hub.start(t)
	leaf.start(t)

	alice := login(t, hub.s, "alice")
	alice.send("JOIN #bee")
	alice.expect("366")
	alice.send("TOPIC #bee :hub topic")
	alice.send("MODE #bee +b *!*@bad.example")
	alice.expect("MODE #bee +b")
	bob := loginBatch(t, hub.s, "bob")
	bob.send("JOIN #bee")
	bob.expect("366")

	carol := login(t, leaf.s, "carol")
	carol.send("JOIN #bee")
	carol.expect("366")
	carol.send("JOIN #leafonly")
	carol.expect("366")
	carol.send("AWAY :lunch")
	carol.expect("306")
	login(t, leaf.s, "dave")
	// the leaf's #bee is older: it keeps its modes, the hub's are dropped
	leaf.s.onLoop(func() {
		ch := leaf.s.net.Channel("#bee")
		leaf.s.emit(&state.ChannelTS{Channel: "#bee", TS: ch.TS - 100})
	})

	leaf.linkNow()
	waitLink(t, "burst", func() bool { return hub.user("dave") != nil && leaf.user("bob") != nil })
	waitLink(t, "sync", func() bool { return hub.logHas("sync done") && leaf.logHas("sync done") })

	if u := hub.user("carol"); u.Server != "leaf.test.local" || u.Away != "lunch" || u.Ident != "~carol" {
		t.Errorf("hub sees carol %+v", u)
	}
	hb, lb := chanState(hub.s, "#bee"), chanState(leaf.s, "#bee")
	if hb != lb {
		t.Errorf("#bee differs:\nhub  %s\nleaf %s", hb, lb)
	}
	cu := hub.user("carol")
	if !strings.Contains(hb, "o:"+cu.UID) || strings.Contains(hb, "o:"+hub.user("alice").UID) || strings.Contains(hb, "bad.example") {
		t.Errorf("older leaf channel should win: %s", hb)
	}
	if !strings.Contains(hb, `topic="hub topic"`) {
		t.Errorf("topic lost: %s", hb)
	}
	if hl, ll := chanState(hub.s, "#leafonly"), chanState(leaf.s, "#leafonly"); hl != ll || hl == "none" {
		t.Errorf("#leafonly:\nhub  %s\nleaf %s", hl, ll)
	}

	// bob (batch) saw the netjoin wrapped; alice saw her op go
	start := bob.expect("BATCH +")
	if !strings.Contains(start, " netjoin hub.test.local leaf.test.local") {
		t.Errorf("batch start %q", start)
	}
	id := strings.Fields(start)[2][1:]
	if j := bob.expect("JOIN #bee"); !strings.Contains(j, "batch="+id) || !strings.Contains(j, "carol!") {
		t.Errorf("join %q not in batch %s", j, id)
	}
	bob.expect("BATCH -" + id)
	if m := alice.expect("MODE #bee -"); !strings.Contains(m, "b") || !strings.Contains(m, "o") || !strings.Contains(m, "alice") {
		t.Errorf("alice's op and the hub's ban should go: %q", m)
	}

	// WHOIS of a remote user names its server and that server's description
	bob.send("WHOIS carol")
	if w := bob.expect(" 312 "); !strings.HasSuffix(w, "leaf.test.local :test leaf.test.local") {
		t.Errorf("whois 312 %q", w)
	}

	// the leaf goes: its users quit with the split reason
	stopServer(leaf.s)
	bob.expect(":carol!~carol@")
	waitLink(t, "carol gone", func() bool { return hub.user("carol") == nil && hub.user("dave") == nil })
	if hub.logHas("lost") == false {
		t.Error("no lost notice")
	}
}

func TestLinkSplitQuitReason(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	leaf.blocks[0].Autoconnect = false
	hub.start(t)
	leaf.start(t)
	bob := login(t, hub.s, "bob")
	bob.send("JOIN #x")
	bob.expect("366")
	carol := login(t, leaf.s, "carol")
	carol.send("JOIN #x")
	carol.expect("366")
	leaf.linkNow()
	bob.expect("JOIN #x")
	stopServer(leaf.s)
	if q := bob.expect("QUIT"); !strings.HasSuffix(q, ":hub.test.local leaf.test.local") {
		t.Errorf("quit %q", q)
	}
}

func TestLinkNickCollision(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, false, "")
	leaf.expects(hub, true, "")
	leaf.blocks[0].Autoconnect = false
	hub.start(t)
	leaf.start(t)
	login(t, hub.s, "dup")
	time.Sleep(1100 * time.Millisecond) // the leaf's dup is a second newer
	young := login(t, leaf.s, "dup")
	old := hub.user("dup")
	newUID := leaf.user("dup").UID
	leaf.linkNow()
	waitLink(t, "both synced", func() bool { return hub.logHas("sync done") && leaf.logHas("sync done") })
	young.expect("collided")
	young.expect("NICK " + newUID)
	for _, n := range []*linkNode{hub, leaf} {
		if u := n.user("dup"); u == nil || u.UID != old.UID {
			t.Errorf("%s: dup is %+v, want %s", n.name, u, old.UID)
		}
		if u := n.user(newUID); u == nil || u.UID != newUID {
			t.Errorf("%s: the loser isn't renamed to %s", n.name, newUID)
		}
	}
	if !hub.logHas("Nick collision on dup") || !leaf.logHas("Nick collision on dup") {
		t.Error("no collision notices")
	}
}

func TestCollisionWinner(t *testing.T) {
	a := collider{ts: 100, ident: "a", host: "h1"}
	b := collider{ts: 200, ident: "b", host: "h2"}
	same := collider{ts: 200, ident: "a", host: "h1"}
	acc := collider{ts: 300, account: "Bee", ident: "c", host: "h3"}
	for _, c := range []struct {
		name         string
		rename       bool
		ex, in       collider
		exWin, inWin bool
	}{
		{"bee: older wins", true, a, b, true, false},
		{"bee: older wins (incoming)", true, b, a, false, true},
		{"bee: same time, both renamed", true, a, a, false, false},
		{"bee: identified beats older", true, a, acc, false, true},
		{"unreal: older wins", false, a, b, true, false},
		{"unreal: same user@host, newer wins", false, a, same, false, true},
		{"unreal: same time, both killed", false, b, same, false, false},
		{"unreal: accounts don't count", false, a, acc, true, false},
	} {
		ex, in := collisionWinner(c.rename, c.ex, c.in)
		if ex != c.exWin || in != c.inWin {
			t.Errorf("%s: got %v %v, want %v %v", c.name, ex, in, c.exWin, c.inWin)
		}
	}
}
