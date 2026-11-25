package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/extauth"
	"github.com/fhazal/beeircd/internal/spamfilter"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// While READONLY is on the Hive Panel (and RPC) ban and channel forms and
// spam filter changes are refused; a channel notice still goes.
func TestReadOnlyPanel(t *testing.T) {
	svc, h := newServices(t)
	regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	svc.readonly = true

	if _, _, p := svc.BanVia("Boss", "panel", store.BanAkill, "*@10.0.0.1", "r", time.Hour); p != readonlyRefusal {
		t.Errorf("BanVia = %q", p)
	}
	if _, p := svc.BanDelVia("Boss", "panel", store.BanAkill, "*@10.0.0.1"); p != readonlyRefusal {
		t.Errorf("BanDelVia = %q", p)
	}
	if _, p := svc.NameBanVia("Boss", "panel", "Spam*", "r", time.Hour); p != readonlyRefusal {
		t.Errorf("NameBanVia = %q", p)
	}
	if p := svc.NameBanDelVia("Boss", "panel", "Spam*"); p != readonlyRefusal {
		t.Errorf("NameBanDelVia = %q", p)
	}
	if _, p := svc.PanelChanSuspend("Boss", "#hive", "r", 0); p != readonlyRefusal {
		t.Errorf("PanelChanSuspend = %q", p)
	}
	if p := svc.PanelChanUnsuspend("Boss", "#hive"); p != readonlyRefusal {
		t.Errorf("PanelChanUnsuspend = %q", p)
	}
	if p := svc.PanelChanTopic("Boss", "#hive", "new topic"); p != readonlyRefusal {
		t.Errorf("PanelChanTopic on a registered channel = %q", p)
	}
	if live := svc.net.Channel("#hive"); live == nil || live.Topic == "new topic" {
		t.Errorf("topic changed in read-only mode")
	}
	if bans, _ := svc.store.NetBans(store.BanAkill); len(bans) != 0 {
		t.Errorf("AKILL stored in read-only mode: %v", bans)
	}
	f := spamfilter.Filter{Type: spamfilter.Simple, Match: "*free nitro*", Targets: "cp", Action: spamfilter.Block, Reason: "r"}
	if _, err := svc.AddSpamFilter(f, "Boss", false); !errors.Is(err, ErrReadOnly) {
		t.Errorf("AddSpamFilter = %v", err)
	}
	if _, err := svc.DelSpamFilter(1, "Boss"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DelSpamFilter = %v", err)
	}

	svc.readonly = false
	if _, _, p := svc.BanVia("Boss", "panel", store.BanAkill, "*@10.0.0.1", "r", time.Hour); p != "" {
		t.Errorf("BanVia with READONLY off = %q", p)
	}
	if _, err := svc.AddSpamFilter(f, "Boss", false); err != nil {
		t.Errorf("AddSpamFilter with READONLY off = %v", err)
	}
}

// Bans the server adds on its own (spam filter, DNSBL, throttle,
// CLEARCHAN G/Z) aren't stored while READONLY is on.
func TestReadOnlyAutoBans(t *testing.T) {
	svc, _ := newServices(t)
	svc.readonly = true
	svc.SpamBan(store.BanShun, "10.0.0.1", "spam", time.Hour)
	svc.DnsblBan(store.BanAkill, "10.0.0.2", "listed", time.Hour)
	svc.ThrottleBan("10.0.0.3", "too fast", time.Hour)
	svc.OperBan(true, "10.0.0.4", "clearchan", "Boss", time.Hour)
	svc.OperBan(false, "10.0.0.5", "clearchan", "Boss", time.Hour)
	for _, kind := range []string{store.BanShun, store.BanAkill, store.BanZline} {
		if bans, _ := svc.store.NetBans(kind); len(bans) != 0 {
			t.Errorf("%s stored in read-only mode: %v", kind, bans)
		}
	}
	svc.readonly = false
	svc.SpamBan(store.BanShun, "10.0.0.1", "spam", time.Hour)
	svc.ThrottleBan("10.0.0.3", "too fast", time.Hour)
	if bans, _ := svc.store.NetBans(store.BanShun); len(bans) != 1 {
		t.Errorf("SHUN not stored with READONLY off: %v", bans)
	}
	if bans, _ := svc.store.NetBans(store.BanZline); len(bans) != 1 {
		t.Errorf("ZLINE not stored with READONLY off: %v", bans)
	}
}

// MemoServ READ (and the web memo pages) still show a memo in read-only
// mode, but leave it unread.
func TestReadOnlyMemoRead(t *testing.T) {
	svc, h := newServices(t)
	ms := h.net.UserByNick("MemoServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	a, _ := svc.store.AccountByNick("Bee")
	reg, _ := svc.store.ChannelByName("#hive")
	if err := svc.store.AddMemo(a, "Boss", "hello there", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.AddChanMemo(reg, "Boss", "hello hive", time.Now()); err != nil {
		t.Fatal(err)
	}
	svc.readonly = true

	h.notices = nil
	svc.Message(bee.UID, ms, "READ 1")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "hello there") {
		t.Fatalf("MS READ in read-only mode: %q", got)
	}
	svc.Message(bee.UID, ms, "READ #hive 1")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "hello hive") {
		t.Fatalf("MS READ #hive in read-only mode: %q", got)
	}
	memos, _ := svc.store.Memos(a)
	if msg := svc.WebMemoRead(a.ID, memos[0].ID); msg != "" {
		t.Errorf("WebMemoRead = %q", msg)
	}
	cm := svc.store.ChanMemos(reg)
	if msg := svc.WebChanMemo(a.ID, "", "#hive", cm[0].ID, false); msg != "" {
		t.Errorf("WebChanMemo = %q", msg)
	}
	if memos, _ = svc.store.Memos(a); memos[0].Read {
		t.Errorf("memo marked read in read-only mode")
	}
	if cm = svc.store.ChanMemos(reg); cm[0].Read {
		t.Errorf("channel memo marked read in read-only mode")
	}

	svc.readonly = false
	svc.Message(bee.UID, ms, "READ 1")
	if memos, _ = svc.store.Memos(a); !memos[0].Read {
		t.Errorf("memo not marked read with READONLY off")
	}
	if msg := svc.WebChanMemo(a.ID, "", "#hive", cm[0].ID, false); msg != "" {
		t.Errorf("WebChanMemo with READONLY off = %q", msg)
	}
	if cm = svc.store.ChanMemos(reg); !cm[0].Read {
		t.Errorf("channel memo not marked read with READONLY off")
	}
}

// ChanServ KICK still works in read-only mode, but a LOG ... MEMO entry
// writes no memo then.
func TestReadOnlyLogMemo(t *testing.T) {
	svc, h := newServices(t)
	cs := h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	reg, _ := svc.store.ChannelByName("#hive")
	if err := svc.store.SetLog(reg, store.LogSetting{Command: "KICK", Method: "MEMO"}); err != nil {
		t.Fatal(err)
	}
	join := func(uid, nick string) {
		w := helpUser(t, h, uid, nick)
		if err := h.Emit(&state.Join{UID: w.UID, Channel: "#hive", TS: 1}); err != nil {
			t.Fatal(err)
		}
		svc.Flush()
	}
	join("1LFAAAAAB", "Wasp")
	svc.readonly = true
	svc.Message(bee.UID, cs, "KICK #hive Wasp")
	svc.Flush()
	if live := svc.net.Channel("#hive"); live == nil || live.Members[h.net.UserByNick("Wasp").UID] != nil {
		t.Fatalf("CS KICK didn't kick in read-only mode: %q", noticeTo(h, "Bee"))
	}
	if cm := svc.store.ChanMemos(reg); len(cm) != 0 {
		t.Fatalf("LOG MEMO saved a memo in read-only mode: %v", cm)
	}

	svc.readonly = false
	join("1LFAAAAAC", "Moth")
	svc.Message(bee.UID, cs, "KICK #hive Moth")
	svc.Flush()
	if cm := svc.store.ChanMemos(reg); len(cm) != 1 || !strings.Contains(cm[0].Text, "Bee used KICK") {
		t.Errorf("LOG MEMO with READONLY off: %v", cm)
	}
}

// While NOEXPIRE (or READONLY) is on, run-out suspensions stay in force
// and aren't lifted; once both are off they are.
func TestNoExpireSuspensions(t *testing.T) {
	svc, h := newServices(t)
	regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	helpUser(t, h, "1LFAAAAAB", "Wasp")
	svc.Message(h.net.UserByNick("Wasp").UID, h.net.UserByNick("NickServ"), "REGISTER honeypot42")
	h.waitFor("Wasp", "registered")
	past := time.Now().Add(-time.Hour)
	a, _ := svc.store.AccountByNick("Wasp")
	a.SuspendedBy, a.SuspendReason, a.SuspendedAt, a.SuspendUntil = "Boss", "r", past.Add(-time.Hour), past
	if err := svc.store.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
	reg, _ := svc.store.ChannelByName("#hive")
	reg.SuspendedBy, reg.SuspendReason, reg.SuspendedAt, reg.SuspendUntil = "Boss", "r", past.Add(-time.Hour), past
	if err := svc.store.SaveChannel(reg); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"noexpire", "readonly"} {
		svc.noexpire, svc.readonly = mode == "noexpire", mode == "readonly"
		svc.expireAccountSuspends(time.Now())
		svc.expireSuspends(time.Now())
		a, _ = svc.store.AccountByNick("Wasp")
		reg, _ = svc.store.ChannelByName("#hive")
		if a.SuspendedBy == "" || !a.Suspended(svc.KeepNow()) {
			t.Errorf("%s: run-out account suspension lifted or not in force", mode)
		}
		if reg.SuspendedBy == "" || !reg.Suspended(svc.KeepNow()) {
			t.Errorf("%s: run-out channel suspension lifted or not in force", mode)
		}
	}
	svc.noexpire, svc.readonly = true, false
	if _, p := svc.PanelChanSuspend("Boss", "#hive", "again", 0); !strings.Contains(p, "is suspended already") {
		t.Errorf("PanelChanSuspend with NOEXPIRE on = %q", p)
	}

	svc.noexpire = false
	if reg, _ = svc.store.ChannelByName("#hive"); reg.Suspended(svc.KeepNow()) {
		t.Errorf("run-out channel suspension in force with NOEXPIRE off")
	}
	svc.expireAccountSuspends(time.Now())
	svc.expireSuspends(time.Now())
	if a, _ = svc.store.AccountByNick("Wasp"); a.SuspendedBy != "" {
		t.Errorf("account suspension not lifted with NOEXPIRE off")
	}
	if reg, _ = svc.store.ChannelByName("#hive"); reg.SuspendedBy != "" {
		t.Errorf("channel suspension not lifted with NOEXPIRE off")
	}
}

// While NOEXPIRE is on a run-out spam filter keeps matching and stays
// stored; once it is off it goes.
func TestNoExpireSpamFilters(t *testing.T) {
	svc, _ := newServices(t)
	past := time.Now().Add(-time.Hour)
	f := spamfilter.Filter{Type: spamfilter.Simple, Match: "*free nitro*", Targets: "cp", Action: spamfilter.Block,
		Reason: "r", SetBy: "Boss", SetAt: past.Add(-time.Hour), Expires: past}
	if _, err := svc.store.AddSpamFilter(f); err != nil {
		t.Fatal(err)
	}
	svc.noexpire = true
	if err := svc.loadSpamFilters(); err != nil {
		t.Fatal(err)
	}
	if len(svc.SpamFilters()) != 1 || svc.SpamSet().First('p', "free nitro now") == nil {
		t.Fatalf("run-out spam filter not in force with NOEXPIRE on")
	}
	svc.expireSpamFilters(time.Now())
	if list, _ := svc.store.SpamFilters(); len(list) != 1 {
		t.Fatalf("run-out spam filter purged with NOEXPIRE on")
	}

	svc.noexpire = false
	svc.expireSpamFilters(time.Now())
	if list, _ := svc.store.SpamFilters(); len(list) != 0 {
		t.Errorf("run-out spam filter kept with NOEXPIRE off")
	}
	if len(svc.SpamFilters()) != 0 || svc.SpamSet().First('p', "free nitro now") != nil {
		t.Errorf("run-out spam filter still matches with NOEXPIRE off")
	}
}

// While READONLY is on the services' own background writes stop: "last
// seen" and quit message, "last used" of a channel, and the KEEPTOPIC topic.
// Nothing reaches the database; with READONLY off they are saved again.
func TestReadOnlyBackgroundWrites(t *testing.T) {
	svc, h := newServices(t)
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	c := svc.clients[bee.UID]
	if c == nil || c.accountID == 0 {
		t.Fatal("Bee is not identified")
	}
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	a, _ := svc.store.AccountByID(c.accountID)
	a.LastSeen, a.LastHost, a.LastQuit = old, "old@host", "old quit"
	if err := svc.store.SaveAccount(a); err != nil {
		t.Fatal(err)
	}
	reg, _ := svc.store.ChannelByName("#hive")
	reg.LastUsed, reg.KeepTopic, reg.Topic = old, true, "old topic"
	if err := svc.store.SaveChannel(reg); err != nil {
		t.Fatal(err)
	}
	live := svc.net.Channel("#hive")
	live.Topic, live.TopicBy, live.TopicAt = "new topic", "Bee", time.Now()

	svc.readonly = true
	svc.touchAccount(c)
	svc.saveQuit(c, "gone fishing")
	svc.csOnIdentify(c)
	svc.csTopicChanged(live)

	a, _ = svc.store.AccountByID(c.accountID)
	if !a.LastSeen.Equal(old) || a.LastHost != "old@host" || a.LastQuit != "old quit" {
		t.Errorf("account last seen written in read-only mode: %v %q %q", a.LastSeen, a.LastHost, a.LastQuit)
	}
	reg, _ = svc.store.ChannelByName("#hive")
	if !reg.LastUsed.Equal(old) {
		t.Errorf("channel last used written in read-only mode: %v", reg.LastUsed)
	}
	if reg.Topic != "old topic" {
		t.Errorf("KEEPTOPIC stored %q in read-only mode", reg.Topic)
	}

	svc.readonly = false
	svc.saveQuit(c, "gone fishing")
	svc.csOnIdentify(c)
	svc.csTopicChanged(live)
	a, _ = svc.store.AccountByID(c.accountID)
	if !a.LastSeen.After(old) || a.LastQuit != "gone fishing" {
		t.Errorf("account last seen not saved with READONLY off: %v %q", a.LastSeen, a.LastQuit)
	}
	reg, _ = svc.store.ChannelByName("#hive")
	if !reg.LastUsed.After(old) || reg.Topic != "new topic" {
		t.Errorf("channel not saved with READONLY off: %v %q", reg.LastUsed, reg.Topic)
	}
}

// A spam filter learned from a linked server that has already run out is
// refused normally; while NOEXPIRE is on it is kept and stays in force,
// like a local one.
func TestNoExpireRemoteSpamFilter(t *testing.T) {
	svc, _ := newServices(t)
	past := time.Now().Add(-time.Hour)
	f := spamfilter.Filter{Type: spamfilter.Simple, Match: "*free nitro*", Targets: "cp", Action: spamfilter.Block,
		Reason: "r", SetBy: "Boss", SetAt: past.Add(-time.Hour), Expires: past}
	if changed, _ := svc.RemoteSpamFilter(f, true); changed {
		t.Fatalf("run-out remote spam filter taken with NOEXPIRE off")
	}
	svc.noexpire = true
	if changed, _ := svc.RemoteSpamFilter(f, true); !changed {
		t.Fatalf("run-out remote spam filter refused with NOEXPIRE on")
	}
	svc.expireSpamFilters(time.Now())
	if list, _ := svc.store.SpamFilters(); len(list) != 1 {
		t.Fatalf("remote spam filter purged with NOEXPIRE on")
	}
	if svc.SpamSet().First('p', "free nitro now") == nil {
		t.Errorf("remote spam filter not in force with NOEXPIRE on")
	}
	svc.noexpire = false
	svc.expireSpamFilters(time.Now())
	if list, _ := svc.store.SpamFilters(); len(list) != 0 {
		t.Errorf("remote spam filter kept with NOEXPIRE off")
	}
}

// sayOK is an outside login source that takes every password.
type sayOK struct{}

func (sayOK) Check(context.Context, extauth.Query) extauth.Answer {
	return extauth.Answer{Result: extauth.OK}
}
func (sayOK) Close() {}

// A first login through the outside source ([auth] create) made a
// NickServ account while READONLY was on.
func TestReadOnlyOutsideLoginCreatesNothing(t *testing.T) {
	svc, h := newServices(t)
	svc.cfg.Auth.Source = "sql"
	svc.extCfg = svc.cfg.Auth
	svc.ext = sayOK{}
	ns := h.net.UserByNick("NickServ")
	wasp := helpUser(t, h, "1LFAAAAAD", "Wasp")
	if !svc.OutsideCreates("Wasp") {
		t.Fatal("OutsideCreates is off with READONLY off")
	}
	svc.readonly = true
	if svc.OutsideCreates("Wasp") {
		t.Error("OutsideCreates is on with READONLY on")
	}
	svc.Message(wasp.UID, ns, "IDENTIFY honeypot42")
	h.waitFor("Wasp", "")
	if a, _ := svc.store.AccountByNick("Wasp"); a != nil {
		t.Errorf("READONLY on, yet a first outside login registered account %q", a.Name)
	}
}

// The periodic Expire deleted run-out ignores from the database while
// READONLY was on. Now, as for network bans, nothing expires then (nor
// under NOEXPIRE): the ignore stays in force until the switch goes off.
func TestReadOnlyKeepsIgnores(t *testing.T) {
	svc, _ := newServices(t)
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := svc.store.SetSvcIgnore(store.Ignore{Mask: "*!*@spam.example", SetBy: "Boss", SetAt: past, Expires: past.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	svc.reloadOperData()
	for _, sw := range []*bool{&svc.readonly, &svc.noexpire} {
		*sw = true
		svc.Expire(time.Now())
		if list, err := svc.store.SvcIgnores(); err != nil || len(list) != 1 {
			t.Errorf("READONLY %v NOEXPIRE %v: database ignores %v, %v", svc.readonly, svc.noexpire, list, err)
		}
		if len(svc.liveIgnores()) != 1 || len(svc.WebIgnores()) != 1 {
			t.Errorf("READONLY %v NOEXPIRE %v: run-out ignore not kept in force", svc.readonly, svc.noexpire)
		}
		*sw = false
	}
	svc.Expire(time.Now())
	if list, _ := svc.store.SvcIgnores(); len(list) != 0 || len(svc.liveIgnores()) != 0 {
		t.Errorf("switches off: run-out ignore not expired: %v", list)
	}
}
