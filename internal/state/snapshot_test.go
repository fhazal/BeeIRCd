package state

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"testing"
	"time"
)

// snapNet is a small network with everything a snapshot must keep.
func snapNet(t *testing.T) *Network {
	t.Helper()
	n := NewNetwork("0BE")
	n.NewChannelParams = map[byte]string{'H': "50:1d"}
	tag := "(oper)"
	bot := &User{UID: n.NextUID(), Nick: "ChanServ", Ident: "services", Host: "irc.test", Server: "irc.test", Service: true}
	alice := &User{UID: n.NextUID(), Nick: "Alice", Ident: "a", Host: "cloak.a", RealHost: "host.a", IP: "10.0.0.1", Realname: "Alice A",
		Server: "irc.test", Account: "alice", Away: "lunch", TLS: true, CertFP: "ab12", OperClass: "admin", OperTag: &tag,
		OperSwhois: []string{"is cool"}, Groups: []string{"known-users"}, Country: "NL", ASN: 64500,
		SignOn: time.Unix(1700000000, 0), NickTS: 1700000000, Modes: map[byte]bool{'i': true, 'o': true, 'x': true}}
	bob := &User{UID: n.NextUID(), Nick: "Bob", Ident: "b", Host: "host.b", RealHost: "host.b", IP: "10.0.0.2", Server: "irc.test",
		SignOn: time.Unix(1700000100, 0), NickTS: 1700000100, Modes: map[byte]bool{'i': true}}
	for _, ev := range []Event{
		&UserAdd{User: bot}, &UserAdd{User: alice}, &UserAdd{User: bob},
		&Join{UID: alice.UID, Channel: "#Hive", TS: 1700000200},
		&Join{UID: bob.UID, Channel: "#hive", TS: 1700000200},
		&Join{UID: bot.UID, Channel: "#hive", TS: 1700000200},
		&Join{UID: bob.UID, Channel: "#quiet", TS: 1700000300},
		&Topic{Source: alice.UID, Channel: "#hive", Text: "bees only", By: "Alice", At: time.Unix(1700000400, 0)},
		&Topic{Source: bob.UID, Channel: "#quiet", Text: latin1, By: "Bob", At: time.Unix(1700000401, 0)},
		&Away{UID: bob.UID, Message: latin1},
		&ChannelMode{Source: alice.UID, Channel: "#hive", By: "Alice", At: time.Unix(1700000500, 0), Changes: []ModeChange{
			{Add: true, Mode: 'k', Arg: "honey"}, {Add: true, Mode: 'l', Arg: "25"}, {Add: true, Mode: 'm'},
			{Add: true, Mode: 'b', Arg: "*!*@wasp.example"}, {Add: true, Mode: 'e', Arg: "*!*@bee.example"},
			{Add: true, Mode: 'v', Arg: "Bob", UID: bob.UID}}},
		&Invite{Source: alice.UID, UID: bob.UID, Channel: "#hive"},
	} {
		if err := n.Apply(ev); err != nil {
			t.Fatalf("%T: %v", ev, err)
		}
	}
	return n
}

func roundTrip(t *testing.T, s *Snapshot) *Snapshot {
	t.Helper()
	for _, u := range s.Users {
		if u.Channels != nil {
			t.Fatalf("snapshot user %s still points at its channels", u.Nick)
		}
	}
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(s); err != nil {
		t.Fatal(err)
	}
	var out Snapshot
	if err := gob.NewDecoder(&b).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return &out
}

// latin1 is text that isn't UTF-8: it must come through byte for byte.
const latin1 = "caf\xe9 \xfcber"

// A snapshot that was written and read again loads into an equal network.
func TestSnapshotRoundTrip(t *testing.T) {
	n := snapNet(t)
	got := NewNetwork("0BE")
	if skipped := got.Load(roundTrip(t, n.Snapshot())); len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	if ok, where := Equal(n, got); !ok {
		t.Fatalf("loaded network differs: %s", where)
	}
	ch, want := got.Channel("#hive"), n.Channel("#hive")
	if ch.Key != "honey" || ch.Limit != 25 || !ch.HasMode('m') || ch.Name != "#Hive" || ch.Param('H') != "50:1d" {
		t.Fatalf("channel settings: %+v", ch)
	}
	bob := got.UserByNick("bob")
	if m := ch.Member(bob); m == nil || !m.Has(RankVoice) || !m.Joined.Equal(want.Members[bob.UID].Joined) {
		t.Fatalf("bob's membership: %+v", m)
	}
	if bob.Channels["#hive"] != ch || bob.Channels["#quiet"] == nil {
		t.Fatal("bob's channels aren't linked back")
	}
	if !ch.Invited[bob.UID] {
		t.Fatal("bob's invite is gone")
	}
	if got.Channel("#quiet").Topic != latin1 || bob.Away != latin1 {
		t.Fatalf("text that isn't UTF-8 changed: %q, %q", got.Channel("#quiet").Topic, bob.Away)
	}
	if _, inv, opers := got.PeopleCounts(); inv != 2 || opers != 1 {
		t.Fatalf("counts: invisible %d opers %d", inv, opers)
	}
	// ~i: bans look channels up through the network.
	if ch.net != got {
		t.Fatal("loaded channel doesn't know its network")
	}
	// Every field of a user comes along.
	a, b := *n.UserByNick("alice"), *got.UserByNick("alice")
	a.Channels, b.Channels = nil, nil
	if !a.SignOn.Equal(b.SignOn) {
		t.Fatalf("sign-on time: %v, want %v", b.SignOn, a.SignOn)
	}
	a.SignOn, b.SignOn = time.Time{}, time.Time{}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("user differs:\n%+v\n%+v", a, b)
	}
	if got.NextUID() != n.NextUID() {
		t.Fatal("the UID counter didn't carry over")
	}
}

// A new process has its own service bots already (maybe under other
// UIDs) and channels they keep open: the snapshot merges into that.
func TestSnapshotLoadIntoStartedNetwork(t *testing.T) {
	old := snapNet(t)
	snap := roundTrip(t, old.Snapshot())
	got := NewNetwork("0BE")
	extra := &User{UID: got.NextUID(), Nick: "NewServ", Service: true}
	cs := &User{UID: got.NextUID(), Nick: "ChanServ", Service: true}
	for _, ev := range []Event{&UserAdd{User: extra}, &UserAdd{User: cs},
		&Join{UID: cs.UID, Channel: "#hive", TS: 1800000000}, &Join{UID: extra.UID, Channel: "#hive", TS: 1800000000}} {
		if err := got.Apply(ev); err != nil {
			t.Fatal(err)
		}
	}
	// What the server does: bots map by nick, people who collide move.
	oldCS, alice := old.UserByNick("ChanServ"), old.UserByNick("alice")
	got.Reserve(snap.Next)
	to := map[string]string{oldCS.UID: cs.UID}
	if got.UserByUID(alice.UID) == nil {
		t.Fatal("test setup: alice's UID should collide with the new ChanServ")
	}
	to[alice.UID] = got.NextUID()
	snap.Remap(to)
	if skipped := got.Load(snap); len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	ch := got.Channel("#hive")
	if ch.TS != 1700000200 || ch.Topic != "bees only" {
		t.Fatalf("the snapshot's channel settings should win: %+v", ch)
	}
	for _, nick := range []string{"ChanServ", "NewServ", "Alice", "Bob"} {
		if u := got.UserByNick(nick); u == nil || ch.Member(u) == nil {
			t.Errorf("%s isn't in #hive", nick)
		}
	}
	if u := got.UserByNick("alice"); u.UID != to[alice.UID] || got.UserByUID(u.UID) != u {
		t.Fatalf("alice has UID %s, want %s", u.UID, to[alice.UID])
	}
	if got.UserByNick("chanserv") != cs {
		t.Fatal("the new process's own ChanServ was replaced")
	}
	if !ch.Invited[got.UserByNick("bob").UID] {
		t.Fatal("bob's invite is gone")
	}
}

// Users that can't come along are skipped with their memberships; the
// rest still loads.
func TestSnapshotLoadSkipsConflicts(t *testing.T) {
	snap := roundTrip(t, snapNet(t).Snapshot())
	got := NewNetwork("0BE")
	squatter := &User{UID: "0BEZZZZZZ", Nick: "bob"}
	if err := got.Apply(&UserAdd{User: squatter}); err != nil {
		t.Fatal(err)
	}
	var bobUID string
	for _, u := range snap.Users {
		if u.Nick == "Bob" {
			bobUID = u.UID
		}
	}
	skipped := got.Load(snap)
	if len(skipped) != 1 || skipped[0] != bobUID {
		t.Fatalf("skipped %v, want bob (%s)", skipped, bobUID)
	}
	if got.UserByUID(bobUID) != nil || got.Channel("#quiet") != nil {
		t.Fatal("the skipped user left something behind")
	}
	if ch := got.Channel("#hive"); len(ch.Members) != 2 || len(ch.Invited) != 0 {
		t.Fatalf("#hive: %d members, %d invites", len(ch.Members), len(ch.Invited))
	}
	// Dropping by Remap does the same.
	snap2 := roundTrip(t, snapNet(t).Snapshot())
	snap2.Remap(map[string]string{bobUID: ""})
	got2 := NewNetwork("0BE")
	if skipped := got2.Load(snap2); len(skipped) != 0 || got2.UserByNick("bob") != nil || got2.Channel("#quiet") != nil {
		t.Fatalf("remap to \"\" should drop bob: skipped %v", skipped)
	}
}

// ChannelSnap and Load copy Channel field by field. A new Channel field
// must be added to both; this count is the reminder.
func TestSnapshotCoversChannel(t *testing.T) {
	if n := reflect.TypeOf(Channel{}).NumField(); n != 14 {
		t.Fatalf("Channel has %d fields: add the new one to ChannelSnap, Snapshot and Load, then update this count", n)
	}
	if n := reflect.TypeOf(Member{}).NumField(); n != 4 {
		t.Fatalf("Member has %d fields: add the new one to MemberSnap, Snapshot and Load, then update this count", n)
	}
}
