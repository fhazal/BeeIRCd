package state

import (
	"testing"
	"time"
)

func newUser(n *Network, nick string) *User {
	u := &User{UID: n.NextUID(), Nick: nick, Ident: "~" + nick, Host: nick + ".example", IP: "192.0.2.1"}
	if err := n.addUser(u); err != nil {
		panic(err)
	}
	return u
}

func TestUIDs(t *testing.T) {
	n := NewNetwork("0BE")
	if a, b := n.NextUID(), n.NextUID(); a != "0BEAAAAAA" || b != "0BEAAAAAB" {
		t.Errorf("UIDs %s %s", a, b)
	}
}

func TestNicks(t *testing.T) {
	n := NewNetwork("0BE")
	bee := newUser(n, "Bee")
	if err := n.addUser(&User{UID: "x", Nick: "bEE"}); err != ErrNickInUse {
		t.Error("same nick in another case was allowed")
	}
	if err := n.changeNick(bee, "BEE", 1); err != nil {
		t.Error("case change of own nick refused")
	}
	newUser(n, "ember")
	if err := n.changeNick(bee, "Ember", 2); err != ErrNickInUse {
		t.Error("taking another's nick was allowed")
	}
	if n.UserByNick("bee") != bee || n.UserByNick("Ember") == nil {
		t.Error("lookup by nick failed")
	}
	n.removeUser(bee)
	if n.NickInUse("Bee") || n.UserCount() != 1 {
		t.Error("RemoveUser left the nick behind")
	}
}

func TestJoinPart(t *testing.T) {
	n := NewNetwork("0BE")
	bee, ember := newUser(n, "Bee"), newUser(n, "ember")
	ch, m, created := n.join(bee, "#BChat", 100, 0, false)
	if !created || !m.Has(RankOp) || !ch.HasMode('n') || !ch.HasMode('t') || ch.TS != 100 {
		t.Fatalf("first join: created=%v op=%v modes=%v", created, m.Has(RankOp), ch.Modes)
	}
	ch.Invited[ember.UID] = true
	ch2, m2, created2 := n.join(ember, "#bchat", 200, 0, false)
	if ch2 != ch || created2 || m2.Has(RankOp) || ch.Invited[ember.UID] {
		t.Fatal("second join should reuse the channel, not op, and clear the invite")
	}
	if len(n.Neighbours(bee)) != 1 || !SharesChannel(bee, ember) {
		t.Error("neighbours wrong")
	}
	n.part(bee, ch)
	n.part(ember, ch)
	if n.Channel("#bchat") != nil || n.ChannelCount() != 0 {
		t.Error("empty channel not removed")
	}
}

func TestBans(t *testing.T) {
	n := NewNetwork("0BE")
	bee := newUser(n, "Bee")
	bee.RealHost = "real.example"
	ch, _, _ := n.join(bee, "#a", 1, 0, false)
	now := time.Now()
	if !ch.addListEntry('b', "*!*@real.example", "x", now) || ch.addListEntry('b', "*!*@REAL.example", "x", now) {
		t.Fatal("AddListEntry duplicate handling wrong")
	}
	if !ch.IsBanned(bee) {
		t.Error("ban on real host missed")
	}
	ch.addListEntry('e', "bee!*@*", "x", now)
	if ch.IsBanned(bee) {
		t.Error("exception ignored")
	}
	if mask, ok := ch.removeListEntry('b', "*!*@real.EXAMPLE"); !ok || mask != "*!*@real.example" {
		t.Error("RemoveListEntry failed")
	}
}

func TestModeStrings(t *testing.T) {
	n := NewNetwork("0BE")
	bee := newUser(n, "Bee")
	ch, _, _ := n.join(bee, "#a", 1, 0, false)
	ch.Modes['k'], ch.Key = true, "secret"
	ch.Modes['l'], ch.Limit = true, 25
	modes, params := ch.ModeString(false)
	if modes != "+klnt" || len(params) != 2 || params[0] != "*" || params[1] != "25" {
		t.Errorf("ModeString = %s %v", modes, params)
	}
	bee.Modes['w'], bee.Modes['i'] = true, true
	if bee.ModeString() != "+iw" {
		t.Errorf("user ModeString = %s", bee.ModeString())
	}
}

func TestRanks(t *testing.T) {
	m := &Member{}
	if m.Prefix() != "" || m.Top() != 0 {
		t.Fatal("empty member has a rank")
	}
	m.set(RankVoice, true)
	m.set(RankOp, true)
	if !m.set(RankOwner, true) || m.set(RankOwner, true) {
		t.Fatal("Set should report a change once")
	}
	if m.Prefix() != "~" || m.AllPrefixes() != "~@+" || m.Top() != RankOwner {
		t.Fatalf("prefix %q all %q", m.Prefix(), m.AllPrefixes())
	}
	if !m.AtLeast(RankHalfop) || !m.AtLeast(RankOwner) {
		t.Fatal("AtLeast")
	}
	m.set(RankOwner, false)
	m.set(RankOp, false)
	if m.AtLeast(RankHalfop) || m.Prefix() != "+" {
		t.Fatal("voice only")
	}
	if RankForMode('h') != RankHalfop || RankForPrefix('&') != RankAdmin || RankForMode('x') != 0 {
		t.Fatal("lookups")
	}
}
