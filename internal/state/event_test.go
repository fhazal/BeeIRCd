package state

import (
	"testing"
	"time"
)

func TestApplyEvents(t *testing.T) {
	n := NewNetwork("0BE")
	now := time.Unix(1_800_000_000, 0)
	must := func(ev Event) {
		t.Helper()
		if err := n.Apply(ev); err != nil {
			t.Fatalf("%T: %v", ev, err)
		}
	}
	must(&UserAdd{User: &User{UID: "0BEA", Nick: "Bee", Ident: "b", Host: "h"}})
	must(&UserAdd{User: &User{UID: "0BEB", Nick: "Wasp", Ident: "w", Host: "h"}})
	if err := n.Apply(&UserAdd{User: &User{UID: "0BEC", Nick: "BEE"}}); err != ErrNickInUse {
		t.Errorf("same nick: %v", err)
	}

	j := &Join{UID: "0BEA", Channel: "#Hive", TS: 100}
	must(j)
	if !j.Created || j.Ranks != RankOp {
		t.Errorf("first join = %+v, want created as op", j)
	}
	j2 := &Join{UID: "0BEB", Channel: "#hive", TS: 200}
	must(j2)
	if j2.Created || j2.Ranks != 0 || n.Channel("#hive").TS != 100 {
		t.Errorf("second join = %+v", j2)
	}

	// Only real changes stay; arguments come back as stored.
	m := &ChannelMode{Source: "0BEA", Channel: "#hive", By: "Bee", At: now, Changes: []ModeChange{
		{Add: true, Mode: 'v', UID: "0BEB"},
		{Add: true, Mode: 'v', UID: "0BEB"}, // already has it
		{Add: true, Mode: 'b', Arg: "*!*@Spam"},
		{Add: true, Mode: 'b', Arg: "*!*@spam"}, // same mask, other case
		{Add: true, Mode: 'n'},                  // +n is on already
		{Add: true, Mode: 'k', Arg: "secret"},
		{Add: true, Mode: 'l', Arg: "0"}, // not a limit
		{Add: true, Mode: 'l', Arg: "25"},
		{Mode: 'o', UID: "0BEB"}, // isn't op
	}}
	must(m)
	if got := len(m.Changes); got != 4 {
		t.Fatalf("effective changes = %+v", m.Changes)
	}
	if m.Changes[0].Arg != "Wasp" {
		t.Errorf("rank change arg = %q, want the nick", m.Changes[0].Arg)
	}
	m2 := &ChannelMode{Channel: "#hive", Changes: []ModeChange{{Mode: 'b', Arg: "*!*@SPAM"}, {Mode: 'k'}}}
	must(m2)
	if m2.Changes[0].Arg != "*!*@Spam" || m2.Changes[1].Arg != "secret" {
		t.Errorf("removals = %+v, want stored mask and key", m2.Changes)
	}

	um := &UserMode{UID: "0BEA", Changes: []ModeChange{{Add: true, Mode: 'i'}, {Add: true, Mode: 'i'}, {Mode: 'w'}}}
	must(um)
	if len(um.Changes) != 1 {
		t.Errorf("user mode changes = %+v", um.Changes)
	}

	nc := &NickChange{UID: "0BEA", Nick: "Queen", TS: 5}
	must(nc)
	if nc.Old != "Bee!b@h" || n.UserByNick("queen") == nil || n.UserByNick("bee") != nil {
		t.Errorf("nick change: old %q", nc.Old)
	}
	if err := n.Apply(&NickChange{UID: "0BEA", Nick: "wasp"}); err != ErrNickInUse {
		t.Errorf("nick collision: %v", err)
	}
	must(&Topic{Channel: "#hive", Text: "buzz", By: "Queen", At: now})
	must(&Away{UID: "0BEB", Message: "lunch"})
	must(&Login{UID: "0BEB", Account: "Wasp"})
	must(&HostChange{UID: "0BEB", Host: "cloak.ip"})
	must(&Invite{Source: "0BEA", UID: "0BEB", Channel: "#nowhere"}) // no such channel: fine
	if err := n.Apply(&Part{UID: "0BEB", Channel: "#nowhere"}); err != ErrNoSuchChannel {
		t.Errorf("part unknown channel: %v", err)
	}

	// A clone fed the same events stays equal.
	c := n.Clone()
	if ok, where := Equal(n, c); !ok {
		t.Fatalf("clone differs: %s", where)
	}
	for _, ev := range []Event{
		&Kick{Source: "0BEA", UID: "0BEB", Channel: "#hive", Reason: "out"},
		&Invite{Source: "0BEA", UID: "0BEB", Channel: "#hive"},
		&Join{UID: "0BEB", Channel: "#other", TS: 300},
		&UserQuit{UID: "0BEA", Reason: "bye"},
	} {
		if err := c.Apply(CopyEvent(ev)); err != nil {
			t.Fatalf("clone %T: %v", ev, err)
		}
		must(ev)
		if ok, where := Equal(n, c); !ok {
			t.Fatalf("after %T: %s", ev, where)
		}
	}
	if n.Channel("#hive") != nil {
		t.Error("empty channel left behind")
	}
	// The clone shares nothing with the original.
	n.UserByNick("wasp").Away = "changed"
	if c.UserByNick("wasp").Away == "changed" {
		t.Error("clone shares users with the original")
	}
}
