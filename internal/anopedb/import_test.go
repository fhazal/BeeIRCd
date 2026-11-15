package anopedb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
)

// testdata/anope-sample.db was written by a real Anope 2.0.20
// (deploy/testnet/anope-sample.sh): enc_sha256 passwords, every kind of
// record anope-fill.py could make.

func sample(t *testing.T) []Object {
	t.Helper()
	f, err := os.Open("testdata/anope-sample.db")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	objs, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "services.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestParse(t *testing.T) {
	objs := sample(t)
	n := map[string]int{}
	for _, o := range objs {
		n[o.Type]++
	}
	if n["NickCore"] != 5 || n["ChannelInfo"] != 3 || n["XLine"] != 4 {
		t.Errorf("counts %v", n)
	}
	for _, bad := range []string{"OBJECT A\nDATA x 1\n", "DATA x 1\n", "OBJECT A\nOBJECT B\nEND\n", "hello\n"} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("Parse(%q) took it", bad)
		}
	}
	o, err := Parse(strings.NewReader("OBJECT A\r\nID 3\r\nDATA k a b \r\nDATA empty \r\nEND\r\n"))
	if err != nil || len(o) != 1 || o[0].Get("k") != "a b " || o[0].Get("empty") != "" {
		t.Errorf("Parse spaces/CRLF: %+v %v", o, err)
	}
}

func TestImportSample(t *testing.T) {
	st := newStore(t)
	r, err := Import(st, sample(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("in %v\nchanged %v\nclashes %v\nskipped %v\nunknown %v", r.In, r.Changed, r.Clashes, r.Skipped, r.Unknown)

	// Accounts, with the passwords Anope hashed (enc_sha256) still right.
	for nick, pw := range map[string]string{"zfill": "fill-pass-zfill", "alice": "fill-pass-alice", "bob": "fill-pass-bob", "carol": "fill-pass-carol", "dave": "fill-pass-dave"} {
		a, _ := st.AccountByNick(nick)
		if a == nil {
			t.Fatalf("no account %s", nick)
		}
		if !passwd.IsAnope(a.PassHash) || !passwd.Check(pw, a.PassHash) || passwd.Check(pw+"x", a.PassHash) {
			t.Errorf("%s: password %q doesn't check (%s)", nick, pw, a.PassHash)
		}
	}
	alice, _ := st.AccountByNick("alice")
	if alice.Email != "alice@example.org" || !alice.Secure || !alice.Private || !alice.HideEmail || !alice.KeepModes ||
		alice.Protect != store.KillQuick || !alice.Enforce || alice.AutoOp || alice.URL != "https://alice.example.org" {
		t.Errorf("alice settings %+v", alice)
	}
	if acc, _ := st.Access(alice); len(acc) != 1 || acc[0] != "alice@10.0.0.*" {
		t.Errorf("alice access %v", acc)
	}
	if st.Greet(alice.ID) != "hello from alice" {
		t.Errorf("greet %q", st.Greet(alice.ID))
	}
	if aj := st.Ajoins(alice); len(aj) != 2 {
		t.Errorf("ajoins %v", aj)
	}
	if v, ok := st.VHostOf("alice"); !ok || v.Host != "cool.alice.vhost" {
		t.Errorf("alice vhost %+v %v", v, ok)
	}
	if v, ok := st.VHostOf("dave"); !ok || v.Ident != "dave" || v.Host != "ident.vhost" {
		t.Errorf("dave vhost %+v %v", v, ok)
	}
	if _, ok := st.VHostRequest("bob"); !ok {
		t.Error("bob's vhost request is missing")
	}
	carol, _ := st.AccountByNick("carol")
	if carol.SuspendedBy == "" || !strings.Contains(carol.SuspendReason, "testing the import") || !carol.SuspendUntil.After(time.Now()) {
		t.Errorf("carol suspension %+v", carol)
	}
	dave, _ := st.AccountByNick("dave")
	if !dave.NoExpire {
		t.Error("dave NOEXPIRE")
	}
	bob, _ := st.AccountByNick("bob")
	if bob.MemoLimit != 7 {
		t.Errorf("bob memo limit %d", bob.MemoLimit)
	}
	if ms, _ := st.Memos(bob); len(ms) != 1 || !strings.Contains(ms[0].Text, "survive the move") || ms[0].From != "alice" {
		t.Errorf("bob memos %+v", ms)
	}
	if ig, _ := st.MemoIgnores(bob); len(ig) != 1 {
		t.Errorf("bob memo ignores %v", ig)
	}

	// Channels.
	bee, _ := st.ChannelByName("#bee")
	if bee == nil {
		t.Fatal("no #bee")
	}
	if bee.Founder != "alice" || bee.Successor != "bob" || bee.Description != "the bee channel" || bee.Topic != "Welcome to the hive" ||
		!bee.KeepTopic || !bee.TopicLock || !bee.Peace || !bee.SecureOps || !bee.SignKick || !bee.Persist || !bee.Private ||
		!bee.KeepModes || !bee.Secure || !bee.SecureFounder || bee.BanType != 3 || bee.URL != "https://bee.example.org" {
		t.Errorf("#bee settings %+v", bee)
	}
	if !strings.Contains(bee.MlockOn, "n") || !strings.Contains(bee.MlockOn, "t") || bee.MlockKey != "honey" || bee.MlockLimit != 50 || !strings.Contains(bee.MlockOff, "s") {
		t.Errorf("#bee mode lock on=%q off=%q key=%q limit=%d", bee.MlockOn, bee.MlockOff, bee.MlockKey, bee.MlockLimit)
	}
	if !strings.HasPrefix(bee.LastModes, "+") || !strings.Contains(bee.LastModes, "honey") {
		t.Errorf("#bee last modes %q", bee.LastModes)
	}
	levels := map[string]int{}
	acc, _ := st.AccessList(bee, 0)
	for _, e := range acc {
		levels[e.Account] = e.Level
	}
	if levels["bob"] != 5 || levels["carol"] != 10 || levels["dave"] != 5 || levels["zfill"] != 10 {
		t.Errorf("#bee access %v", levels)
	}
	if lv := st.ChanLevels(bee); lv["TOPIC"] != 4 || lv["GETKEY"] != store.LevelDisabled {
		t.Errorf("#bee levels TOPIC=%d GETKEY=%d", lv["TOPIC"], lv["GETKEY"])
	}
	if ak, _ := st.Akicks(bee); len(ak) != 2 {
		t.Errorf("#bee akicks %+v", ak)
	}
	if em := st.EntryMsgs(bee); len(em) != 2 {
		t.Errorf("#bee entry messages %+v", em)
	}
	cb := st.ChanBot(bee)
	if b := st.BotByID(cb.BotID); b == nil || b.Nick != "Honey" || !cb.Greet || !cb.Fantasy || !cb.DontKickOps || cb.BanExpire != time.Minute {
		t.Errorf("#bee bot %+v", cb)
	}
	k := st.ChanKickers(bee)
	if !k.On[store.KickCaps] || !k.On[store.KickBadwords] || !k.On[store.KickFlood] || k.TTB[store.KickCaps] != 3 || k.CapsMin != 10 || k.FloodLines != 6 {
		t.Errorf("#bee kickers %+v", k)
	}
	if bw := st.BadWords(bee); len(bw) != 2 {
		t.Errorf("#bee bad words %+v", bw)
	}
	if ms := st.ChanMemos(bee); len(ms) != 1 {
		t.Errorf("#bee memos %+v", ms)
	}
	if ls := st.LogSettings(bee); len(ls) != 1 {
		t.Errorf("#bee log %+v", ls)
	}
	if hive, _ := st.ChannelByName("#hive"); hive == nil || hive.SuspendedBy == "" {
		t.Errorf("#hive suspension %+v", hive)
	}
	if b := st.BotByNick("Drone"); b == nil {
		t.Error("bot Drone")
	}
	if st.BotByNick("NickServ") != nil {
		t.Error("Anope's NickServ came in as a bot")
	}

	// Network lists.
	if bans, _ := st.NetBans(store.BanAkill); len(bans) != 2 {
		t.Errorf("akills %+v", bans)
	}
	if bans, _ := st.NetBans(store.BanSNLine); len(bans) != 1 {
		t.Errorf("snlines %+v", bans)
	}
	if f, _ := st.Forbids(store.ForbidNick); len(f) < 2 { // evil* + the SQLINE BadNick*
		t.Errorf("forbid nick %+v", f)
	}
	if ex, _ := st.SessionExcepts(); len(ex) != 2 {
		t.Errorf("session exceptions %+v", ex)
	}
	if n, _ := st.AllNews(); len(n) != 2 {
		t.Errorf("news %+v", n)
	}
	if s, _ := st.AllSeen(); len(s) == 0 {
		t.Error("no seen entries")
	}
	if len(r.Unknown) != 0 {
		t.Errorf("unknown types %v", r.Unknown)
	}
	found := false
	for _, s := range r.Skipped {
		if strings.Contains(s, "*!*@trusted.host") {
			found = true
		}
	}
	if !found {
		t.Errorf("the mask access entry isn't reported: %v", r.Skipped)
	}
}

// Importing the same file again adds nothing twice.
func TestImportTwice(t *testing.T) {
	st := newStore(t)
	for i := 0; i < 2; i++ {
		if _, err := Import(st, sample(t), Options{}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.AllNews(); len(n) != 2 {
		t.Errorf("news after two imports: %+v", n)
	}
	if ex, _ := st.SessionExcepts(); len(ex) != 2 {
		t.Errorf("session exceptions after two imports: %+v", ex)
	}
}

// A nick BeeIRCd already has keeps BeeIRCd's account, unless Replace.
func TestImportClash(t *testing.T) {
	st := newStore(t)
	h, _ := passwd.Hash("bee-own")
	if _, err := st.CreateAccount("alice", h, "own@example.org", time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := Import(st, sample(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := st.AccountByNick("alice")
	if a.Email != "own@example.org" || len(r.Clashes) == 0 {
		t.Errorf("BeeIRCd's alice was not kept: %+v %v", a, r.Clashes)
	}
	if c, _ := st.ChannelByName("#bee"); c != nil {
		t.Errorf("#bee came in without its founder: %+v", c)
	}

	st2 := newStore(t)
	st2.CreateAccount("alice", h, "own@example.org", time.Now())
	if _, err := Import(st2, sample(t), Options{Replace: true}); err != nil {
		t.Fatal(err)
	}
	if a, _ := st2.AccountByNick("alice"); a.Email != "alice@example.org" {
		t.Errorf("Replace kept BeeIRCd's alice: %+v", a)
	}
}

func TestFlagsLevel(t *testing.T) {
	for flags, want := range map[string]int{"AFOfiortv": 10, "Oo": 5, "h": 4, "Vv": 3, "i": 1, "": 0} {
		if got := flagsLevel(flags); got != want {
			t.Errorf("flagsLevel(%q) = %d, want %d", flags, got, want)
		}
	}
	if got := lastModes(strings.Fields("KEY,honey LIMIT,50 NOEXTERNAL BOGUS TOPIC")); got != "+klnt honey 50" {
		t.Errorf("lastModes = %q", got)
	}
}

// Grouped nicks, FLAGS access and an akick on an account, hand-made.
func TestImportGroupsAndFlags(t *testing.T) {
	db := `OBJECT NickCore
DATA display Ann
DATA pass md5:2ab96390c7dbe3439de74d0c9b0b1767
DATA email ann@example.org
END
OBJECT NickAlias
DATA nick Ann
DATA nc Ann
DATA time_registered 1700000000
END
OBJECT NickAlias
DATA nick Ann_away
DATA nc Ann
DATA time_registered 1700000100
END
OBJECT NickCore
DATA display Ben
DATA pass old:abcdef
END
OBJECT NickAlias
DATA nick Ben
DATA nc Ben
END
OBJECT ChannelInfo
DATA name #club
DATA founder Ann
END
OBJECT ChanAccess
DATA provider access/flags
DATA ci #club
DATA mask Ben
DATA data Vv
END
OBJECT AutoKick
DATA ci #club
DATA nc Ben
DATA reason no
END
OBJECT Mystery
DATA x 1
END
`
	objs, err := Parse(strings.NewReader(db))
	if err != nil {
		t.Fatal(err)
	}
	st := newStore(t)
	r, err := Import(st, objs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := st.AccountByNick("Ann_away")
	if a == nil || a.Name != "Ann" || !passwd.Check("hunter2", a.PassHash) {
		t.Errorf("grouped nick / md5 password: %+v", a)
	}
	ben, _ := st.AccountByNick("Ben")
	if ben == nil || passwd.Check("", ben.PassHash) || passwd.Check("abcdef", ben.PassHash) {
		t.Errorf("Ben (enc_old) should come in without a usable password: %+v", ben)
	}
	c, _ := st.ChannelByName("#club")
	if lvl, flags := st.ChanAccess(c, ben.ID); lvl != 3 || flags != "Vv" {
		t.Errorf("FLAGS Vv -> level %d flags %q, want rank 3 and Vv", lvl, flags)
	}
	for in, want := range map[string]string{"Vv": "Vv", "vfO": "Oflv", "fl": "fl", "xyz": "", "": ""} {
		if got := importFlags(in); got != want {
			t.Errorf("importFlags(%q) = %q, want %q", in, got, want)
		}
	}
	if ak, _ := st.Akicks(c); len(ak) != 1 || ak[0].Mask != "~a:Ben" {
		t.Errorf("account akick %+v", ak)
	}
	if len(r.Changed) != 1 || len(r.Unknown) != 1 || r.Unknown[0].What != "Mystery" {
		t.Errorf("report changed=%v unknown=%v", r.Changed, r.Unknown)
	}
}
