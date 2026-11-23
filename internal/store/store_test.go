package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/spamfilter"
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
	if err != nil || a.Name != "Bee" || !a.Enforce || !a.HideEmail || a.MemoLimit != 20 || !a.MemoSignon || a.MemoMail {
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
	// The opers table as an early version shipped it, without pass_hash.
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
	// Pretend this database is from an early version: AOP stored as 3, schema 1.
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
	// The accounts table as an early version shipped it, with one account.
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

// A removed network ban is remembered (also over a restart) until it
// would have run out, a week at most; adding it again forgets it.
func TestRemovedNetBans(t *testing.T) {
	s, path := open(t)
	now := time.Now()
	set := now.Add(-time.Hour).Truncate(time.Second)
	for _, b := range []NetBan{
		{Kind: BanZline, Mask: "192.0.2.1", Reason: "r", SetBy: "B", SetAt: set, Expires: now.Add(time.Hour)},
		{Kind: BanAkill, Mask: "*@192.0.2.2", Reason: "r", SetBy: "B", SetAt: set},
		{Kind: BanKline, Mask: "*@192.0.2.3", Reason: "r", SetBy: "B", SetAt: set},
	} {
		if err := s.AddNetBan(b); err != nil {
			t.Fatal(err)
		}
		if ok, err := s.DelNetBan(b.Kind, b.Mask); !ok || err != nil {
			t.Fatalf("DelNetBan %s: %v %v", b.Mask, ok, err)
		}
	}
	if ok, _ := s.DelNetBan(BanShun, "*@nothing"); ok {
		t.Error("removed a ban that wasn't there")
	}
	s.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	removed := func(kind, mask string, at time.Time) bool {
		t.Helper()
		got, ok, err := s.RemovedNetBan(kind, mask, at)
		if err != nil {
			t.Fatal(err)
		}
		if ok && !got.Equal(set) {
			t.Errorf("%s %s: set at %v, want %v", kind, mask, got, set)
		}
		return ok
	}
	if !removed(BanZline, "192.0.2.1", now) || !removed(BanAkill, "*@192.0.2.2", now) {
		t.Fatal("removal not remembered over a restart")
	}
	if removed(BanKline, "*@192.0.2.3", now) || removed(BanShun, "*@nothing", now) {
		t.Error("remembered a K-line or a ban that wasn't there")
	}
	if removed(BanZline, "192.0.2.1", now.Add(2*time.Hour)) {
		t.Error("remembered past the ban's own expiry")
	}
	if !removed(BanAkill, "*@192.0.2.2", now.Add(6*24*time.Hour)) || removed(BanAkill, "*@192.0.2.2", now.Add(RemovedBanKeep+time.Minute)) {
		t.Error("a permanent ban's removal isn't kept for a week")
	}
	if err := s.AddNetBan(NetBan{Kind: BanAkill, Mask: "*@192.0.2.2", Reason: "again", SetBy: "B", SetAt: now}); err != nil {
		t.Fatal(err)
	}
	if removed(BanAkill, "*@192.0.2.2", now) {
		t.Error("adding the ban again didn't forget the removal")
	}

	// PurgeExpired forgets old ones and keeps only the newest RemovedBanMax.
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO netban_removed SELECT 'zline', 'm' || i, 1, i, ? FROM n`, RemovedBanMax+5, now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if gone, err := s.PurgeExpired(now.Add(2 * time.Hour)); err != nil || len(gone) != 0 {
		t.Fatalf("purged %v, %v", gone, err)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM netban_removed").Scan(&n)
	if n != 0 {
		t.Errorf("%d left after their time", n)
	}
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO netban_removed SELECT 'zline', 'm' || i, 1, i, ? FROM n`, RemovedBanMax+5, now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	s.PurgeExpired(now)
	s.db.QueryRow("SELECT COUNT(*) FROM netban_removed").Scan(&n)
	_, oldest, _ := s.RemovedNetBan(BanZline, "m5", now)
	_, next, _ := s.RemovedNetBan(BanZline, "m6", now)
	if n != RemovedBanMax || oldest || !next {
		t.Errorf("cap: %d kept, m5 %v, m6 %v", n, oldest, next)
	}
}

// Opening an older database moves SET PROTECT delays to SET KILL modes and
// drops the UNBANME privilege (Anope 2.0.20), saying which channels lost it.
func TestMigrateAnope(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 0)
	var ids []int64
	for _, n := range []string{"on", "short", "long", "quick"} {
		a, err := s.CreateAccount(n, "h", "", now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	ch, err := s.CreateChannel("#lvl", s.c.account(ids[0]), "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetChanLevel(ch, "UNBANME", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChanLevel(ch, "UNBAN", 3); err != nil {
		t.Fatal(err)
	}
	for i, d := range []int{0, 15, 45, 20} {
		if _, err := s.db.Exec("UPDATE accounts SET protect_delay = ? WHERE id = ?", d, ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for i, want := range []int{KillOn, KillQuick, KillOn, KillQuick} {
		if a, _ := s2.AccountByID(ids[i]); a.Protect != want || !a.Secure {
			t.Errorf("account %d: Protect %d Secure %v, want %d true", i, a.Protect, a.Secure, want)
		}
	}
	reg, _ := s2.ChannelByName("#lvl")
	lv := s2.ChanLevels(reg)
	if _, ok := lv["UNBANME"]; ok || lv["UNBAN"] != 3 || !reg.Secure {
		t.Errorf("levels %v, secure %v", lv, reg.Secure)
	}
	if n := s2.Notes(); len(n) != 1 || n[0] != "#lvl: LEVELS UNBANME 1 removed (Anope 2.0.20 has only UNBAN)" {
		t.Errorf("notes %q", n)
	}
	if err := s2.VerifyCache(); err != nil {
		t.Error(err)
	}
}

func TestSplitAndRename(t *testing.T) {
	s, _ := open(t)
	now := time.Unix(1_800_000_000, 0)
	a, _ := s.CreateAccount("Bee", "hash", "bee@example.org", now)
	s.AddNick(a, "Bee_", now)
	b, err := s.SplitNick("bee_", now)
	if err != nil || b == nil || b.Name != "Bee_" || b.PassHash != "hash" || b.Email != "bee@example.org" || b.ID == a.ID {
		t.Fatalf("SplitNick = %+v, %v", b, err)
	}
	if got, _ := s.AccountByNick("Bee_"); got.ID != b.ID {
		t.Errorf("Bee_ belongs to %d", got.ID)
	}
	s.AddNick(a, "Bee2", now)
	if err := s.RenameAccount(a.ID, "Bee2"); err != nil {
		t.Fatal(err)
	}
	s.RemoveNick("Bee")
	if got, _ := s.AccountByNick("Bee2"); got.Name != "Bee2" {
		t.Errorf("name %q", got.Name)
	}
	if err := s.VerifyCache(); err != nil {
		t.Error(err)
	}
}

func TestOperMoreTables(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 0)
	a, _ := s.CreateAccount("Bee", "h", "", now)
	ch, _ := s.CreateChannel("#hive", a, "", now)
	if err := s.AddOperInfo(false, a.ID, "watch", "Boss", now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOperInfo(true, ch.ID, "chan note", "Boss", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSvcIgnore(Ignore{Mask: "*!*@Bad.Example", SetBy: "Boss", SetAt: now, Expires: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSvcIgnore(Ignore{Mask: "*!*@bad.example", Reason: "again", SetBy: "Boss", SetAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNews(NewsOper, "opers", "Boss", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSeen([]Seen{{Nick: "Bee", Mask: "~b@h", Kind: SeenQuit, Message: "bye", Last: now}, {Nick: "x", Kind: SeenJoin, Last: now}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSeen(nil, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n, _ := s2.OperInfos(false, a.ID); len(n) != 1 || n[0].Text != "watch" {
		t.Errorf("account info %+v", n)
	}
	if g, _ := s2.SvcIgnores(); len(g) != 1 || g[0].Reason != "again" || g[0].Expires.Unix() != 0 {
		t.Errorf("ignores %+v (same mask in other case replaces it)", g)
	}
	if news, _ := s2.AllNews(); len(news) != 1 || news[0].Kind != NewsOper {
		t.Errorf("news %+v", news)
	}
	if seen, _ := s2.AllSeen(); len(seen) != 1 || seen[0].Message != "bye" || !seen[0].Last.Equal(now) {
		t.Errorf("seen %+v", seen)
	}
	// Notes go with their account or channel.
	if err := s2.DeleteChannel(ch.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.OperInfos(true, ch.ID); len(n) != 0 {
		t.Errorf("channel notes outlived the channel: %+v", n)
	}
}

// HostServ vhosts belong to a nick and go with it (and with the account).
func TestVHostsFollowNicks(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	a, err := s.CreateAccount("bee", "x", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddNick(a, "bee2", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVHost("nobody", "", "x.org", "Boss", now); err != ErrNotFound {
		t.Fatalf("vhost for an unregistered nick: %v", err)
	}
	for _, n := range []string{"bee", "bee2"} {
		if err := s.SetVHost(n, "", n+".example.org", "Boss", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RequestVHost("bee2", "id", "req.example.org", now); err != nil {
		t.Fatal(err)
	}
	if v, ok := s.VHostOf("BEE2"); !ok || v.Mask() != "bee2.example.org" || v.Nick != "bee2" {
		t.Fatalf("VHostOf = %+v %v", v, ok)
	}
	if r, ok := s.VHostRequest("bee2"); !ok || r.Mask() != "id@req.example.org" {
		t.Fatalf("request = %+v %v", r, ok)
	}
	if err := s.RemoveNick("bee2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VHostOf("bee2"); ok {
		t.Error("vhost stayed after its nick went")
	}
	if len(s.VHostRequests()) != 0 {
		t.Error("request stayed after its nick went")
	}
	if err := s.VerifyCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAccount(a.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.VHosts()) != 0 {
		t.Error("vhosts stayed after the account went")
	}
	if err := s.VerifyCache(); err != nil {
		t.Fatal(err)
	}
}

// AdminServ history keeps the last HistoryKeep changes.
func TestAdminHistory(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	var last int64
	for i := 0; i < HistoryKeep+5; i++ {
		id, err := s.AddChange(Change{At: now, By: "Boss", Kind: "set", Key: "max-users", Old: "1", New: "2"})
		if err != nil {
			t.Fatal(err)
		}
		last = id
	}
	all, err := s.Changes(1000)
	if err != nil || len(all) != HistoryKeep || all[0].ID != last {
		t.Fatalf("kept %d, newest %d (want %d), %v", len(all), all[0].ID, last, err)
	}
	if _, ok, _ := s.ChangeByID(1); ok {
		t.Error("oldest change still kept")
	}
	if c, ok, err := s.ChangeByID(last); !ok || err != nil || c.Key != "max-users" || c.Old != "1" {
		t.Fatalf("ChangeByID = %+v %v %v", c, ok, err)
	}
}

// TestOperReach: an entry's reach is saved and read back (also after a
// reopen); old rows and odd values read as home.
func TestOperReach(t *testing.T) {
	s, path := open(t)
	a, _ := s.CreateAccount("ember", "x", "", time.Now())
	b, _ := s.CreateAccount("wasp", "x", "", time.Now())
	if err := s.SaveOper(&Oper{AccountID: a.ID, Flags: "a", Hosts: []string{"*@*"}, Reach: ReachNetwork, AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOper(&Oper{AccountID: b.ID, Flags: "a", Hosts: []string{"*@*"}, Reach: "everywhere", AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.OperByAccount(a.ID); !o.Network() {
		t.Fatalf("reach = %q, want network", o.Reach)
	}
	if o, _ := s.OperByAccount(b.ID); o.Network() || o.Reach != "" {
		t.Fatalf("odd reach kept: %q", o.Reach)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if o, _ := s2.OperByAccount(a.ID); !o.Network() {
		t.Fatalf("after reopen reach = %q, want network", o.Reach)
	}
	o, _ := s2.OperByAccount(a.ID)
	o.Reach = ""
	if err := s2.SaveOper(o); err != nil {
		t.Fatal(err)
	}
	if o, _ := s2.OperByAccount(a.ID); o.Network() {
		t.Fatal("reach not set back to home")
	}
}

// A removed spam filter that goes over links is remembered (also over a
// restart) until it would have run out, a week at most; adding it again
// forgets it.
func TestRemovedSpamFilters(t *testing.T) {
	s, path := open(t)
	now := time.Now()
	set := now.Add(-time.Hour).Truncate(time.Second)
	mk := func(match, action string, exp time.Time) spamfilter.Filter {
		return spamfilter.Filter{Type: spamfilter.Simple, Match: match, Targets: "cp", Action: action, Reason: "r", SetBy: "B", SetAt: set, Expires: exp}
	}
	timed := mk("timed", spamfilter.Block, now.Add(time.Hour))
	perm := mk("perm", spamfilter.Kill, time.Time{})
	local := mk("local", "nothing", time.Time{})
	for _, f := range []spamfilter.Filter{timed, perm, local} {
		id, err := s.AddSpamFilter(f)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.DelSpamFilter(id); !ok || err != nil {
			t.Fatalf("DelSpamFilter %s: %v %v", f.Match, ok, err)
		}
	}
	if ok, err := s.DelSpamFilter(9999); ok || err != nil {
		t.Errorf("removed a rule that wasn't there: %v %v", ok, err)
	}
	s.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	removed := func(f spamfilter.Filter, at time.Time) bool {
		t.Helper()
		got, ok, err := s.RemovedSpamFilter(f, at)
		if err != nil {
			t.Fatal(err)
		}
		if ok && !got.Equal(set) {
			t.Errorf("%s: set at %v, want %v", f.Match, got, set)
		}
		return ok
	}
	if !removed(timed, now) || !removed(perm, now) {
		t.Fatal("removal not remembered over a restart")
	}
	if removed(local, now) {
		t.Error("remembered a rule that doesn't go over links")
	}
	other := perm
	other.Targets = "u"
	if removed(other, now) {
		t.Error("a rule with other targets counted as removed")
	}
	if removed(timed, now.Add(2*time.Hour)) {
		t.Error("remembered past the rule's own expiry")
	}
	if !removed(perm, now.Add(6*24*time.Hour)) || removed(perm, now.Add(RemovedBanKeep+time.Minute)) {
		t.Error("a permanent rule's removal isn't kept for a week")
	}
	again := perm
	again.SetAt = now
	if _, err := s.AddSpamFilter(again); err != nil {
		t.Fatal(err)
	}
	if removed(perm, now) {
		t.Error("adding the rule again didn't forget the removal")
	}

	// PurgeSpamFilters forgets old ones and keeps only the newest RemovedBanMax.
	ins := `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO spamfilter_removed SELECT 'simple', 'cp', 'block', 'm' || i, 1, i, ? FROM n`
	if _, err := s.db.Exec(ins, RemovedBanMax+5, now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeSpamFilters(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM spamfilter_removed").Scan(&n)
	if n != 0 {
		t.Errorf("%d left after their time", n)
	}
	if _, err := s.db.Exec(ins, RemovedBanMax+5, now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	s.PurgeSpamFilters(now)
	s.db.QueryRow("SELECT COUNT(*) FROM spamfilter_removed").Scan(&n)
	_, oldest, _ := s.RemovedSpamFilter(mk("m5", spamfilter.Block, time.Time{}), now)
	_, next, _ := s.RemovedSpamFilter(mk("m6", spamfilter.Block, time.Time{}), now)
	if n != RemovedBanMax || oldest || !next {
		t.Errorf("cap: %d kept, m5 %v, m6 %v", n, oldest, next)
	}
}
