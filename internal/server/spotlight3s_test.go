package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// Hive Panel Spotlight: the busiest room counts who talked in the last 5
// minutes, never shows +s/+p rooms, keeps no text; today's peak; the
// newest account.
func TestSpotlight(t *testing.T) {
	s := testServer(t, nil)
	a, b, c := login(t, s, "Ann"), login(t, s, "Bee"), login(t, s, "Cat")
	for _, x := range []*tc{a, b, c} {
		x.send("JOIN #hive")
		x.expect(" 366 ")
	}
	a.send("JOIN #quiet")
	a.expect(" 366 Ann #quiet ")
	// #early ties #hive (2 talking) but talked first: #hive wins.
	for _, x := range []*tc{a, c} {
		x.send("JOIN #early")
		x.expect(" 366 ")
		x.send("PRIVMSG #early :first")
		x.send("PING e")
		x.expect("PONG")
	}
	a.send("TOPIC #hive :\x02Welcome\x02 to the hive")
	a.expect("TOPIC #hive")
	a.send("PRIVMSG #hive :hi")
	b.send("PRIVMSG #hive :hello")
	a.send("PRIVMSG #quiet :just me")
	c.send("NOTICE #hive :a notice is not talk")
	b.send("PING sync")
	b.expect("PONG")
	c.send("PING sync")
	c.expect("PONG")

	var sp control.Spotlight
	s.onLoop(func() { sp = s.statusInfo().Spotlight })
	if sp.Room == nil || sp.Room.Name != "#hive" || sp.Room.Talkers != 2 || sp.Room.Members != 3 {
		t.Fatalf("room = %+v", sp.Room)
	}
	if sp.Room.Topic != "\x02Welcome\x02 to the hive" {
		t.Errorf("topic = %q", sp.Room.Topic)
	}
	if sp.DayPeak == nil || sp.DayPeak.People != 3 {
		t.Errorf("day peak = %+v", sp.DayPeak)
	}

	// Still 2 talking in each, but #early now has more lines: it wins,
	// even though #hive talks last.
	for i := 0; i < 3; i++ {
		c.send("PRIVMSG #early :more")
	}
	b.send("PRIVMSG #hive :last word")
	b.send("PING sync")
	b.expect("PONG")
	c.send("PING sync")
	c.expect("PONG")
	s.onLoop(func() { sp = s.statusInfo().Spotlight })
	if sp.Room == nil || sp.Room.Name != "#early" {
		t.Errorf("more lines: room = %+v", sp.Room)
	}

	// A secret room is never shown, however busy.
	a.send("MODE #early +s")
	a.expect("MODE #early +s")
	s.onLoop(func() { sp = s.statusInfo().Spotlight })
	if sp.Room == nil || sp.Room.Name != "#hive" {
		t.Errorf("with #early +s, room = %+v", sp.Room)
	}

	// Talk older than 5 minutes is forgotten.
	s.onLoop(func() {
		old := time.Now().Add(-talkWindow - time.Second).UnixNano()
		for _, m := range s.talk {
			for uid := range m {
				m[uid] = old
			}
		}
		sp = s.statusInfo().Spotlight
	})
	if sp.Room != nil {
		t.Errorf("old talk still counted: %+v", sp.Room)
	}
	s.onLoop(func() {
		if len(s.talk) != 0 {
			t.Errorf("old talk kept: %d rooms", len(s.talk))
		}
	})
}

// Newest account and anniversaries come from the store.
func TestSpotlightAccounts(t *testing.T) {
	s, _, _ := fileServer(t, "")
	registered(t, s, "Bee")
	var sp control.Spotlight
	s.onLoop(func() { sp = s.statusInfo().Spotlight })
	if sp.Newest == nil || sp.Newest.Name != "Bee" {
		t.Fatalf("newest = %+v", sp.Newest)
	}
	if len(sp.Anniversaries) != 0 {
		t.Errorf("anniversaries = %+v", sp.Anniversaries)
	}
	// Seen one year later: no longer new, but an anniversary.
	newest, years := s.store.SpotlightAccounts(time.Now().AddDate(1, 0, 0), time.Now().AddDate(1, 0, -7), 3)
	if newest != nil || len(years) != 1 || years[0].Name != "Bee" || years[0].Years != 1 {
		t.Errorf("a year on: newest %+v, years %+v", newest, years)
	}
}
