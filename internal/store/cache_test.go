package store

import (
	"strings"
	"testing"
	"time"
)

// TestCacheMatchesDatabase runs every kind of write and checks after each
// one that memory and SQLite agree, then that a reopen sees the same.
func TestCacheMatchesDatabase(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 500_000_000) // the half second must not leak into memory
	step := 0
	ok := func(err error) {
		t.Helper()
		step++
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if err := s.VerifyCache(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	a, err := s.CreateAccount("Bee", "h1", "bee@example.org", now)
	ok(err)
	b, err := s.CreateAccount("Wasp", "h2", "", now.Add(time.Second))
	ok(err)
	c, err := s.CreateAccount("Ant", "h3", "", now)
	ok(err)
	ok(s.AddNick(a, "Bee_", now.Add(2*time.Second)))
	ok(s.AddNick(b, "Wasp|away", now))
	ok(s.RemoveNick("wasp|AWAY"))
	a.Enforce, a.LastSeen, a.LastHost, a.MemoLimit = false, now.Add(time.Hour), "~bee@home", 10
	ok(s.SaveAccount(a))
	_, err = s.AddAccess(a, "*@home")
	ok(err)
	_, err = s.AddAccess(a, "*@office")
	ok(err)
	_, err = s.DelAccess(a, "*@home")
	ok(err)
	ok(s.AddMemo(a, "Wasp", "hi", now))
	ok(s.AddMemo(a, "Ant", "yo", now))
	memos, _ := s.Memos(a)
	ok(s.MarkRead(memos[0].ID))
	ok(s.DeleteMemo(memos[1].ID))
	_, err = s.AddMemoIgnore(a, "Spam")
	ok(err)
	_, err = s.AddMemoIgnore(a, "Junk")
	ok(err)
	_, err = s.DelMemoIgnore(a, "SPAM")
	ok(err)

	ch, err := s.CreateChannel("#Hive", a, "the hive", now)
	ok(err)
	ch2, err := s.CreateChannel("#nest", b, "", now)
	ok(err)
	_, err = s.SetLevel(ch, b, LevelAOP, "Bee", now)
	ok(err)
	_, err = s.SetLevel(ch, c, LevelVOP, "Bee", now)
	ok(err)
	_, err = s.SetLevel(ch, c, LevelSOP, "Bee", now.Add(time.Minute)) // change
	ok(err)
	_, err = s.SetLevel(ch2, a, LevelAOP, "Wasp", now)
	ok(err)
	_, err = s.AddAkick(ch, "*!*@bad", "go away", "Bee", now.Add(time.Second))
	ok(err)
	_, err = s.AddAkick(ch, "*!*@worse", "", "Bee", now)
	ok(err)
	_, err = s.DelAkick(ch, "*!*@bad")
	ok(err)
	ch.SuccessorID, ch.MlockOn, ch.Topic, ch.TopicAt, ch.Secure = c.ID, "ntR", "buzz", now, true
	ok(s.SaveChannel(ch))
	ok(s.SaveOper(&Oper{AccountID: b.ID, Flags: "ck", Hosts: []string{"*@a", "*@b"}, AddedBy: "Bee", AddedAt: now}))
	ok(s.SaveOper(&Oper{AccountID: c.ID, Flags: "c", AddedBy: "Bee", AddedAt: now}))
	ok(s.SaveOper(&Oper{AccountID: b.ID, Flags: "ckg", Title: "Guard", AddedBy: "x", AddedAt: now.Add(time.Hour)}))
	if o, _ := s.OperByAccount(b.ID); o.AddedBy != "Bee" || o.Account != "Wasp" || len(o.Hosts) != 0 {
		t.Errorf("replaced oper entry = %+v", o)
	}
	_, err = s.DeleteOper(c.ID)
	ok(err)
	ok(s.SetFounder(ch, c)) // c was successor and SOP: both cleared
	got, _ := s.ChannelByName("#hive")
	if got.Founder != "Ant" || got.SuccessorID != 0 {
		t.Errorf("after SetFounder: %+v", got)
	}
	if l, _ := s.Level(got, c.ID); l != LevelFounder {
		t.Errorf("new founder level = %d", l)
	}
	_, err = s.ClearLevel(got, LevelAOP)
	ok(err)

	got.SuccessorID = a.ID
	ok(s.SaveChannel(got))
	changes, err := s.DeleteAccount(c.ID) // #Hive goes to Bee
	ok(err)
	if len(changes) != 1 || changes[0].NewOwner != "Bee" {
		t.Errorf("changes = %+v", changes)
	}
	_, err = s.DeleteAccount(b.ID) // #nest has no successor: dropped
	ok(err)
	if n, _ := s.ChannelCount(); n != 1 {
		t.Errorf("channels left = %d", n)
	}
	ch3, err := s.CreateChannel("#tmp", a, "", now)
	ok(err)
	ok(s.DeleteChannel(ch3.ID))

	accounts, nicks, nMemos, _ := s.Counts()
	if accounts != 1 || nicks != 2 || nMemos != 1 {
		t.Errorf("counts = %d %d %d", accounts, nicks, nMemos)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.VerifyCache(); err != nil {
		t.Fatal(err)
	}
	again, _ := s2.AccountByNick("bee_")
	if again == nil || again.MemoLimit != 10 || again.LastSeen.Unix() != now.Add(time.Hour).Unix() {
		t.Errorf("after reopen: %+v", again)
	}
}

// TestReadsAreCopies makes sure changing what a read returned doesn't
// change the cache until it is saved.
func TestReadsAreCopies(t *testing.T) {
	s, _ := open(t)
	now := time.Unix(1_800_000_000, 0)
	a, _ := s.CreateAccount("Bee", "h", "", now)
	a.Email = "changed"
	ch, _ := s.CreateChannel("#hive", a, "", now)
	ch.Topic = "changed"
	if again, _ := s.AccountByID(a.ID); again.Email != "" {
		t.Error("account read is not a copy")
	}
	if again, _ := s.ChannelByName("#hive"); again.Topic != "" {
		t.Error("channel read is not a copy")
	}
	if err := s.VerifyCache(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkAccountByNick(b *testing.B) {
	p := b.TempDir() + "/s.db"
	s, err := Open(p)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	s.CreateAccount("Bee", "h", "", time.Now())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.AccountByNick("bee")
	}
}

func TestOneServerPerDatabase(t *testing.T) {
	s, path := open(t)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), ErrInUse.Error()) {
		t.Fatalf("second Open = %v, want %v", err, ErrInUse)
	}
	s.Close()
	s2, err := Open(path) // free again once closed
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}
