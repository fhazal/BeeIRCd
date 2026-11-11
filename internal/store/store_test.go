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

func TestLevelsMigration(t *testing.T) {
	s, path := open(t)
	bee, _ := s.CreateAccount("Bee", "x", "", time.Now())
	ember, _ := s.CreateAccount("ember", "x", "", time.Now())
	c, err := s.CreateChannel("#bchat", bee, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Pretend this database is from step 5: AOP stored as 3, schema 1.
	if _, err := s.db.Exec("INSERT INTO chan_access (channel_id, account_id, level, added_by, added_at) VALUES (?, ?, 3, 'Bee', 0)", c.ID, ember.ID); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("UPDATE meta SET value = '1' WHERE key = 'schema'")
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if l, _ := s2.Level(c, ember.ID); l != LevelAOP {
		t.Fatalf("level after migration = %d, want %d", l, LevelAOP)
	}
	// Schema 2 (10 to 50) moves to Anope's numbers too.
	for old, want := range map[int]int{10: LevelUOP, 20: LevelVOP, 25: LevelHOP, 30: LevelAOP, 40: LevelSOP, 45: LevelQOP} {
		s2.db.Exec("UPDATE chan_access SET level = ? WHERE account_id = ?", old, ember.ID)
		s2.db.Exec("UPDATE meta SET value = '2' WHERE key = 'schema'")
		s2.Close()
		if s2, err = Open(path); err != nil {
			t.Fatal(err)
		}
		if l, _ := s2.Level(c, ember.ID); l != want {
			t.Fatalf("schema 2 level %d became %d, want %d", old, l, want)
		}
	}
	// Schema 3: a start must not move levels again (10 is SOP now).
	s2.db.Exec("UPDATE chan_access SET level = 10 WHERE account_id = ?", ember.ID)
	s2.Close()
	s3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if l, _ := s3.Level(c, ember.ID); l != LevelSOP {
		t.Fatalf("level after another start = %d", l)
	}
}

func TestAccountsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// The accounts table as step 4 shipped it, with one account.
	if _, err := old.Exec(`CREATE TABLE accounts (id INTEGER PRIMARY KEY, name TEXT NOT NULL, pass_hash TEXT NOT NULL,
		email TEXT NOT NULL DEFAULT '', registered INTEGER NOT NULL, last_seen INTEGER NOT NULL, last_host TEXT NOT NULL DEFAULT '',
		enforce INTEGER NOT NULL DEFAULT 1, hide_email INTEGER NOT NULL DEFAULT 1, hide_host INTEGER NOT NULL DEFAULT 1,
		memo_notify INTEGER NOT NULL DEFAULT 1, memo_limit INTEGER NOT NULL DEFAULT 50);
		INSERT INTO accounts (id, name, pass_hash, registered, last_seen) VALUES (7, 'Bee', 'h', 1, 1);
		CREATE TABLE nicks (nick_key TEXT PRIMARY KEY, nick TEXT NOT NULL, account_id INTEGER NOT NULL, registered INTEGER NOT NULL);
		INSERT INTO nicks VALUES ('bee', 'Bee', 7, 1);`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.AccountByNick("bee")
	if a == nil || a.HideStatus || a.HideQuit || a.Protect != 0 || !a.Enforce {
		t.Fatalf("migrated account = %+v", a)
	}
	a.HideStatus, a.LastQuit, a.Protect = true, "Quit: bye", 30
	if err := s.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyCache(); err != nil {
		t.Fatal(err)
	}
}

func TestNetBansAndMigration(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 0)
	// An AKILL in the old table moves over at the next start.
	if _, err := s.db.Exec("INSERT INTO akills (mask, reason, set_by, set_at, expires) VALUES ('*@old.example', 'old', 'Boss', 5, 0)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.NetBans(BanAkill)
	if len(got) != 1 || got[0].Mask != "*@old.example" || got[0].Kind != BanAkill {
		t.Fatalf("moved AKILLs = %+v", got)
	}
	for _, b := range []NetBan{
		{Kind: BanZline, Mask: "10.0.0.0/8", Reason: "r", SetBy: "B", SetAt: now},
		{Kind: BanShun, Mask: "*@10.0.0.1", Reason: "r", SetBy: "B", SetAt: now, Expires: now.Add(time.Hour)},
		{Kind: BanZline, Mask: "10.0.0.0/8", Reason: "changed", SetBy: "B", SetAt: now}, // replaces
		{Kind: BanAkill, Mask: "10.0.0.0/8", Reason: "same mask, other kind", SetBy: "B", SetAt: now},
	} {
		if err := s.AddNetBan(b); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.NetBans("")
	if len(all) != 4 {
		t.Fatalf("all bans = %+v", all)
	}
	z, _ := s.NetBans(BanZline)
	if len(z) != 1 || z[0].Reason != "changed" || !z[0].Live(now.Add(100*24*time.Hour)) {
		t.Errorf("zline = %+v", z)
	}
	s.AddForbid(ForbidNick, Forbid{Name: "Spam*", Reason: "r", SetBy: "B", SetAt: now, Expires: now.Add(time.Minute)})
	s.SaveSessionExcept(SessionExcept{Mask: "10.0.0.0/8", Max: 5, SetBy: "B", SetAt: now, Expires: now.Add(time.Minute)})
	if f, _ := s.Forbids(ForbidNick); len(f) != 1 || f[0].Expires.Unix() != now.Add(time.Minute).Unix() {
		t.Errorf("forbid expiry lost: %+v", f)
	}
	gone, err := s.PurgeExpired(now.Add(2 * time.Hour))
	if err != nil || len(gone) != 3 {
		t.Fatalf("purged %v, %v", gone, err)
	}
	if ok, _ := s.DelNetBan(BanZline, "10.0.0.0/8"); !ok {
		t.Error("DelNetBan")
	}
	if left, _ := s.NetBans(""); len(left) != 2 {
		t.Errorf("left = %+v", left)
	}
}
