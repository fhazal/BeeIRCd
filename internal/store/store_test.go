package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "services.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func TestAccountsAndNicks(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 0)
	a, err := s.CreateAccount("Bee", "hash", "bee@example.org", now)
	if err != nil || a.Name != "Bee" || !a.Enforce || !a.HideEmail || a.MemoLimit != 50 {
		t.Fatalf("CreateAccount = %+v, %v", a, err)
	}
	if _, err := s.CreateAccount("BEE", "h", "", now); err != ErrExists {
		t.Errorf("same nick in other case: %v", err)
	}
	if err := s.AddNick(a, "Bee_", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNick(a, "bee_", now); err != ErrExists {
		t.Errorf("AddNick duplicate: %v", err)
	}
	got, _ := s.AccountByNick("BEE_")
	if got == nil || got.ID != a.ID {
		t.Fatalf("AccountByNick(BEE_) = %+v", got)
	}
	nicks, _ := s.Nicks(a)
	if len(nicks) != 2 || nicks[0] != "Bee" || nicks[1] != "Bee_" {
		t.Errorf("Nicks = %v", nicks)
	}
	a.Enforce, a.LastHost = false, "host.example"
	if err := s.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path) // survives a reopen
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	again, _ := s2.AccountByNick("bee")
	if again == nil || again.Enforce || again.LastHost != "host.example" || !again.Registered.Equal(now) {
		t.Errorf("after reopen: %+v", again)
	}
	if err := s2.Check(); err != nil {
		t.Error(err)
	}
	if none, err := s2.AccountByNick("nobody"); none != nil || err != nil {
		t.Errorf("AccountByNick(nobody) = %v, %v", none, err)
	}
}

func TestMemosAccessAndDelete(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	a, _ := s.CreateAccount("Bee", "h", "", now)
	for _, text := range []string{"one", "two"} {
		if err := s.AddMemo(a, "ember", text, now); err != nil {
			t.Fatal(err)
		}
	}
	memos, _ := s.Memos(a)
	if len(memos) != 2 || memos[0].Text != "one" || memos[0].Read {
		t.Fatalf("Memos = %+v", memos)
	}
	s.MarkRead(memos[0].ID)
	s.DeleteMemo(memos[1].ID)
	memos, _ = s.Memos(a)
	if len(memos) != 1 || !memos[0].Read {
		t.Errorf("after read+delete: %+v", memos)
	}

	if ok, _ := s.AddAccess(a, "~bee@*.example"); !ok {
		t.Error("AddAccess failed")
	}
	if ok, _ := s.AddAccess(a, "~bee@*.example"); ok {
		t.Error("AddAccess allowed a duplicate")
	}
	if ok, _ := s.AddMemoIgnore(a, "Spammer"); !ok {
		t.Error("AddMemoIgnore failed")
	}
	if ig, _ := s.Ignores(a, "SPAMMER"); !ig {
		t.Error("Ignores is case sensitive")
	}

	old := time.Now().Add(-100 * 24 * time.Hour)
	b, _ := s.CreateAccount("Old", "h", "", old)
	exp, _ := s.Expired(time.Now().Add(-90 * 24 * time.Hour))
	if len(exp) != 1 || exp[0].ID != b.ID {
		t.Errorf("Expired = %+v", exp)
	}

	if _, err := s.DeleteAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	accounts, nicks, memoCount, _ := s.Counts()
	if accounts != 1 || nicks != 1 || memoCount != 0 {
		t.Errorf("after delete: %d accounts, %d nicks, %d memos (cascade failed?)", accounts, nicks, memoCount)
	}
}

func TestOpersMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// The opers table as step 6 first shipped it, without pass_hash.
	if _, err := old.Exec(`CREATE TABLE opers (account_id INTEGER PRIMARY KEY, flags TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
		hosts TEXT NOT NULL DEFAULT '', added_by TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.CreateAccount("ember", "x", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOper(&Oper{AccountID: a.ID, Flags: "g", Hosts: []string{"*@*"}, PassHash: "h", AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	o, err := s.OperByAccount(a.ID)
	if err != nil || o == nil || o.PassHash != "h" {
		t.Fatalf("oper = %+v, %v", o, err)
	}
}
