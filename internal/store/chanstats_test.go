package store

import (
	"testing"
	"time"
)

func TestChanStatsSaveAndLoad(t *testing.T) {
	s, path := open(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local) // a Wednesday
	bee, _ := s.CreateAccount("Bee", "hash", "", now)
	ann, _ := s.CreateAccount("Ann", "hash", "", now)
	hive, err := s.CreateChannel("#hive", bee, "the hive", now)
	if err != nil {
		t.Fatal(err)
	}
	if s.ChanStatsChannel("#HIVE") != 0 {
		t.Fatal("CHANSTATS is on before SET")
	}
	hive.ChanStats = true
	bee.ChanStats = true
	if err := s.SaveChannel(hive); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAccount(bee); err != nil {
		t.Fatal(err)
	}
	if s.ChanStatsChannel("#HIVE") != hive.ID || !s.AccountChanStats(bee.ID) || s.AccountChanStats(ann.ID) {
		t.Fatal("CHANSTATS flags not kept")
	}

	s.AddChanStats(hive.ID, bee.ID, ChanStats{Lines: 1, Letters: 10, Words: 2, Happy: 1}, now)
	s.AddChanStats(hive.ID, bee.ID, ChanStats{Lines: 1, Letters: 5, Words: 1, Actions: 1}, now)
	s.AddChanStats(hive.ID, ann.ID, ChanStats{Lines: 1, Letters: 50, Words: 9}, now)
	s.AddChanStats(hive.ID, 999, ChanStats{Lines: 1}, now) // no such account: ignored
	if err := s.SaveChanStats(); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyCache(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok := s2.ChanStatsOf(hive.ID, bee.ID, now)
	want := ChanStats{Lines: 2, Letters: 15, Words: 3, Actions: 1, Happy: 1}
	if !ok || got[StatsTotal] != want || got[StatsDay] != want || got[StatsWeek] != want || got[StatsMonth] != want {
		t.Fatalf("after reopen: %+v %v", got, ok)
	}
	if g, _ := s2.ChanStatsOf(0, bee.ID, now); g[StatsTotal] != want {
		t.Fatalf("network-wide: %+v", g)
	}
	if _, ok := s2.ChanStatsOf(hive.ID, 999, now); ok {
		t.Fatal("stats for an account that doesn't exist")
	}
	top := s2.TopChanStats(hive.ID, 3)
	if len(top) != 2 || top[0].Name != "Ann" || top[1].Name != "Bee" {
		t.Fatalf("top: %+v", top)
	}

	// The next day (Thursday), the next week (Monday) and the next month.
	thu := now.AddDate(0, 0, 1)
	got, _ = s2.ChanStatsOf(hive.ID, bee.ID, thu)
	if got[StatsDay] != (ChanStats{}) || got[StatsWeek] != want || got[StatsTotal] != want {
		t.Fatalf("next day: %+v", got)
	}
	mon := time.Date(2026, 11, 30, 0, 0, 1, 0, time.Local)
	s2.AddChanStats(hive.ID, bee.ID, ChanStats{Lines: 1, Letters: 1}, mon)
	got, _ = s2.ChanStatsOf(hive.ID, bee.ID, mon)
	one := ChanStats{Lines: 1, Letters: 1}
	if got[StatsDay] != one || got[StatsWeek] != one || got[StatsMonth].Lines != 3 || got[StatsTotal].Lines != 3 {
		t.Fatalf("Monday: %+v", got)
	}
	dec := time.Date(2026, 12, 1, 9, 0, 0, 0, time.Local)
	got, _ = s2.ChanStatsOf(hive.ID, bee.ID, dec)
	if got[StatsMonth] != (ChanStats{}) || got[StatsWeek] != one || got[StatsTotal].Lines != 3 {
		t.Fatalf("new month: %+v", got)
	}

	// Renaming the channel keeps its counts (they go by ID).
	if err := s2.RenameChannel("#hive", "#comb"); err != nil {
		t.Fatal(err)
	}
	if s2.ChanStatsChannel("#comb") != hive.ID || s2.ChanStatsChannel("#hive") != 0 {
		t.Fatal("CHANSTATS didn't follow the rename")
	}
	if got, ok := s2.ChanStatsOf(hive.ID, bee.ID, dec); !ok || got[StatsTotal].Lines != 3 {
		t.Fatalf("after rename: %+v", got)
	}

	// Dropping the account, then the channel, takes their counts along.
	if _, err := s2.DeleteAccount(ann.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.ChanStatsOf(hive.ID, ann.ID, now); ok {
		t.Fatal("dropped account still has stats")
	}
	if err := s2.SaveChanStats(); err != nil {
		t.Fatal(err)
	}
	if err := s2.DeleteChannel(hive.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.ChanStatsOf(hive.ID, bee.ID, now); ok {
		t.Fatal("dropped channel still has stats")
	}
	var n int
	s2.db.QueryRow("SELECT COUNT(*) FROM chanstats WHERE channel_id != 0").Scan(&n)
	if n != 0 {
		t.Errorf("%d channel rows left in the database", n)
	}
	s2.db.QueryRow("SELECT COUNT(*) FROM chanstats WHERE account_id = ?", ann.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows of the dropped account left", n)
	}
	if g, ok := s2.ChanStatsOf(0, bee.ID, now); !ok || g[StatsTotal].Lines != 3 {
		t.Errorf("network-wide counts went with the channel: %+v", g)
	}
}

func TestStatsKeys(t *testing.T) {
	sun := time.Date(2026, 11, 29, 23, 59, 0, 0, time.Local)
	k := statsKeys(sun)
	if k[StatsMonth] != 202611 || k[StatsWeek] != 20261123 || k[StatsDay] != 20261129 {
		t.Fatalf("Sunday: %v", k)
	}
	if k := statsKeys(time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local)); k[StatsWeek] != 20261228 || k[StatsMonth] != 202701 {
		t.Fatalf("New year: %v", k)
	}
}
