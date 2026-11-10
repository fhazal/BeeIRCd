package state

import (
	"testing"
	"time"
)

func newUser(n *Network, nick string) *User {
	u := &User{UID: n.NextUID(), Nick: nick, Ident: "~" + nick, Host: nick + ".example", IP: "192.0.2.1"}
	if err := n.AddUser(u); err != nil {
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
	if err := n.AddUser(&User{UID: "x", Nick: "bEE"}); err != ErrNickInUse {
		t.Error("same nick in another case was allowed")
	}
	if err := n.ChangeNick(bee, "BEE", 1); err != nil {
		t.Error("case change of own nick refused")
	}
	newUser(n, "ember")
	if err := n.ChangeNick(bee, "Ember", 2); err != ErrNickInUse {
		t.Error("taking another's nick was allowed")
	}
	if n.UserByNick("bee") != bee || n.UserByNick("Ember") == nil {
		t.Error("lookup by nick failed")
	}
	n.RemoveUser(bee)
	if n.NickInUse("Bee") || n.UserCount() != 1 {
		t.Error("RemoveUser left the nick behind")
	}
}

func TestJoinPart(t *testing.T) {
	n := NewNetwork("0BE")
	bee, ember := newUser(n, "Bee"), newUser(n, "ember")
	ch, m, created := n.Join(bee, "#BChat", 100)
	if !created || !m.Op || !ch.HasMode('n') || !ch.HasMode('t') || ch.TS != 100 {
		t.Fatalf("first join: created=%v op=%v modes=%v", created, m.Op, ch.Modes)
	}
	ch.Invited[ember.UID] = true
	ch2, m2, created2 := n.Join(ember, "#bchat", 200)
	if ch2 != ch || created2 || m2.Op || ch.Invited[ember.UID] {
		t.Fatal("second join should reuse the channel, not op, and clear the invite")
	}
	if len(n.Neighbours(bee)) != 1 || !SharesChannel(bee, ember) {
		t.Error("neighbours wrong")
	}
	n.Part(bee, ch)
	n.Part(ember, ch)
	if n.Channel("#bchat") != nil || n.ChannelCount() != 0 {
		t.Error("empty channel not removed")
	}
}

func TestBans(t *testing.T) {
	n := NewNetwork("0BE")
	bee := newUser(n, "Bee")
	bee.RealHost = "real.example"
	ch, _, _ := n.Join(bee, "#a", 1)
	now := time.Now()
	if !ch.AddListEntry('b', "*!*@real.example", "x", now) || ch.AddListEntry('b', "*!*@REAL.example", "x", now) {
		t.Fatal("AddListEntry duplicate handling wrong")
	}
	if !ch.IsBanned(bee) {
		t.Error("ban on real host missed")
	}
	ch.AddListEntry('e', "bee!*@*", "x", now)
	if ch.IsBanned(bee) {
		t.Error("exception ignored")
	}
	if mask, ok := ch.RemoveListEntry('b', "*!*@real.EXAMPLE"); !ok || mask != "*!*@real.example" {
		t.Error("RemoveListEntry failed")
	}
}

func TestModeStrings(t *testing.T) {
	n := NewNetwork("0BE")
	bee := newUser(n, "Bee")
	ch, _, _ := n.Join(bee, "#a", 1)
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
