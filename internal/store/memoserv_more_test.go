package store

import (
	"testing"
	"time"
)

func TestBuddiesAndMemoColumns(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 0)
	a, _ := s.CreateAccount("Bee", "h", "", now)
	b, _ := s.CreateAccount("Kes", "h", "", now)
	for _, n := range []string{"Kes", "wren", "KES"} {
		if _, err := s.AddBuddy(a, n); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Buddies(a); len(got) != 2 || got[0] != "Kes" {
		t.Errorf("buddies = %v", got)
	}
	if got := s.BuddyOf("kes"); len(got) != 1 || got[0] != a.ID {
		t.Errorf("BuddyOf = %v", got)
	}
	if gone, _ := s.DelBuddy(a, "WREN"); !gone {
		t.Error("DelBuddy")
	}
	a.MemoSignon, a.MemoMail, a.MemoNotify = false, true, false
	if err := s.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
	// An account on the old default limit moves to 20 at the next start;
	// one that chose its own keeps it.
	s.db.Exec("UPDATE accounts SET memo_limit = 50 WHERE id = ?", a.ID)
	s.db.Exec("UPDATE accounts SET memo_limit = 35 WHERE id = ?", b.ID)
	s.db.Exec("DELETE FROM meta WHERE key = 'memo_limit_20'")
	if err := s.VerifyCache(); err == nil {
		t.Log("(cache differs on purpose until the reopen)")
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
	got, _ := s2.AccountByNick("bee")
	kes, _ := s2.AccountByNick("kes")
	if got.MemoLimit != 20 || kes.MemoLimit != 35 || got.MemoSignon || !got.MemoMail || got.MemoNotify || len(s2.Buddies(got)) != 1 {
		t.Errorf("after reopen: %+v / %+v", got, kes)
	}
	if n := len(s2.AllAccounts()); n != 2 {
		t.Errorf("AllAccounts = %d", n)
	}
}
