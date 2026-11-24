package store

import (
	"testing"
	"time"
)

func TestTopChanStatsInAndTotals(t *testing.T) {
	s, _ := open(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local) // a Wednesday
	bee, _ := s.CreateAccount("Bee", "hash", "", now)
	ann, _ := s.CreateAccount("Ann", "hash", "", now)
	cat, _ := s.CreateAccount("Cat", "hash", "", now)
	hive, err := s.CreateChannel("#hive", bee, "the hive", now)
	if err != nil {
		t.Fatal(err)
	}
	// Last month: Cat wrote a lot, then this month Bee and Ann tie on letters.
	last := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	s.AddChanStats(hive.ID, cat.ID, ChanStats{Lines: 9, Letters: 900}, last)
	s.AddChanStats(hive.ID, bee.ID, ChanStats{Lines: 1, Letters: 10, Happy: 2, Kicks: 1}, now)
	s.AddChanStats(hive.ID, ann.ID, ChanStats{Lines: 3, Letters: 10, Actions: 1, Modes: 2}, now)

	top := s.TopChanStatsIn(hive.ID, StatsMonth, 10, now)
	if len(top) != 2 || top[0].Name != "Ann" || top[1].Name != "Bee" { // same letters: more lines first
		t.Fatalf("month top: %+v", top)
	}
	if all := s.TopChanStatsIn(hive.ID, StatsTotal, 10, now); len(all) != 3 || all[0].Name != "Cat" {
		t.Fatalf("all-time top: %+v", all)
	}
	if got := s.TopChanStatsIn(hive.ID, StatsTotal, 2, now); len(got) != 2 {
		t.Fatalf("n=2: %+v", got)
	}
	// A day later nobody has counted today; the week and month still hold.
	next := now.AddDate(0, 0, 1)
	if got := s.TopChanStatsIn(hive.ID, StatsDay, 10, next); len(got) != 0 {
		t.Fatalf("stale day: %+v", got)
	}
	if got := s.TopChanStatsIn(hive.ID, StatsWeek, 10, next); len(got) != 2 {
		t.Fatalf("week: %+v", got)
	}
	if s.TopChanStatsIn(hive.ID, 9, 10, now) != nil {
		t.Fatal("bad period")
	}

	tot := s.ChanStatsTotals(hive.ID, StatsMonth, now)
	if tot.Lines != 4 || tot.Letters != 20 || tot.Smileys() != 2 || tot.Actions != 1 || tot.Kicks != 1 || tot.Modes != 2 {
		t.Fatalf("month totals: %+v", tot)
	}
	if tot := s.ChanStatsTotals(hive.ID, StatsTotal, now); tot.Letters != 920 {
		t.Fatalf("all-time totals: %+v", tot)
	}
	if tot := s.ChanStatsTotals(hive.ID, StatsDay, next); tot != (ChanStats{}) {
		t.Fatalf("stale day totals: %+v", tot)
	}
	if tot := s.ChanStatsTotals(12345, StatsTotal, now); tot != (ChanStats{}) {
		t.Fatalf("unknown channel: %+v", tot)
	}
}
