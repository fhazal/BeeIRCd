package services

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/spamfilter"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// fakeHost is a server with nobody connected to it: every user in these
// tests lives on another server, as they will once servers link.
type fakeHost struct {
	t       *testing.T
	net     *state.Network
	svc     *Services
	notices []string // "to: text"
	posts   chan func()
	opers   map[string]string // UID -> oper flags
	remote  map[string]bool   // UIDs SessionOper treats as on another server
	said    []string          // channel messages: "Bot #chan: text"

	rehashes  []string // who asked for a rehash
	rehashErr error    // what Rehash answers
}

func (h *fakeHost) Emit(ev state.Event) error {
	if err := h.net.Apply(ev); err != nil {
		return err
	}
	h.svc.Event(ev)
	return nil
}
func (h *fakeHost) Notice(from, to, text string) {
	if u := h.net.UserByUID(to); u != nil {
		h.notices = append(h.notices, u.Nick+": "+text)
	}
}
func (h *fakeHost) Numeric(uid, code string, params ...string) {}
func (h *fakeHost) DefaultHost(uid string) string              { return "" }
func (h *fakeHost) ChannelMessage(from, channel string, status byte, privmsg bool, text string) {
	nick := from
	if u := h.net.UserByUID(from); u != nil {
		nick = u.Nick
	}
	h.said = append(h.said, nick+" "+channel+": "+text)
}
func (h *fakeHost) ServerNotice(uid, text string)    {}
func (h *fakeHost) Snotice(letter byte, text string) {}
func (h *fakeHost) Disconnect(uid, reason string) {
	h.Emit(&state.UserQuit{UID: uid, Reason: reason})
}
func (h *fakeHost) Kill(uid, by, reason string)          { h.Disconnect(uid, "Killed") }
func (h *fakeHost) OperFlags(uid string) string          { return h.opers[uid] }
func (h *fakeHost) AccountGone(id int64)                 {}
func (h *fakeHost) IsAdminAccount(a *store.Account) bool { return false }
func (h *fakeHost) CheckModeParam(channel string, m byte, arg string) (string, string) {
	return arg, ""
}

func (h *fakeHost) ApplyChannelModes(uid, srcUID, channel, modes string, args []string) {
}
func (h *fakeHost) ApplySetting(uid, bot, key, value string) {}
func (h *fakeHost) Post(f func())                            { h.posts <- f }
func (h *fakeHost) Stats() HostStats                         { return HostStats{Started: time.Now()} }
func (h *fakeHost) ResetPeak()                               {}
func (h *fakeHost) Rehash(by string) error {
	h.rehashes = append(h.rehashes, by)
	return h.rehashErr
}
func (h *fakeHost) ForceJoin(uid, channel string)                       {}
func (h *fakeHost) JoinBlocked(uid, channel, key string) (string, bool) { return "", false }

func (h *fakeHost) SessionOper(uid, flags string) bool {
	if h.remote[uid] {
		return false
	}
	if h.opers == nil {
		h.opers = map[string]string{}
	}
	if flags == "" {
		delete(h.opers, uid)
	} else {
		h.opers[uid] = flags
	}
	return true
}

// SetUserModes applies a plain "+h" or "-h" style change.
func (h *fakeHost) SetUserModes(uid, src, modes string) bool {
	add := true
	var ch []state.ModeChange
	for i := 0; i < len(modes); i++ {
		switch modes[i] {
		case '+':
			add = true
		case '-':
			add = false
		default:
			ch = append(ch, state.ModeChange{Add: add, Mode: modes[i]})
		}
	}
	h.Emit(&state.UserMode{Source: src, UID: uid, Changes: ch})
	return true
}

// waitFor runs posted work (finished password checks) until a notice to
// nick contains want.
func (h *fakeHost) waitFor(nick, want string) {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, n := range h.notices {
			if strings.HasPrefix(n, nick+": ") && strings.Contains(n, want) {
				h.notices = nil
				return
			}
		}
		select {
		case f := <-h.posts:
			f()
		case <-deadline:
			h.t.Fatalf("no notice to %s with %q; got %q", nick, want, h.notices)
		}
	}
}

func newServices(t *testing.T) (*Services, *fakeHost) {
	t.Helper()
	cfg := &config.Config{Server: config.Server{Name: "hub.test", Network: "BeeNet", SID: "0BE"}}
	cfg.Services.Database = filepath.Join(t.TempDir(), "services.db")
	cfg.SetDefaults()
	no := false
	cfg.Services.ForceEmail = &no
	st, err := store.Open(cfg.Services.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &fakeHost{t: t, net: state.NewNetwork("0BE"), posts: make(chan func(), 16)}
	svc, err := New(h, cfg, log.New(io.Discard, "", 0), h.net, st)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	svc.Start()
	svc.Flush()
	return svc, h
}

func TestServicesForUsersOnAnotherServer(t *testing.T) {
	svc, h := newServices(t)
	nickServ, chanServ := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ")
	if nickServ == nil || chanServ == nil || !svc.IsBot(nickServ) {
		t.Fatal("service bots missing")
	}
	bee := &state.User{UID: "1LFAAAAAA", Nick: "Bee", Ident: "~bee", Host: "leaf.example", RealHost: "leaf.example",
		Server: "leaf.test", Modes: map[byte]bool{}}
	if err := h.Emit(&state.UserAdd{User: bee}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()

	svc.Message(bee.UID, nickServ, "REGISTER honeypot42")
	h.waitFor("Bee", "registered")
	if bee.Account != "Bee" || !bee.Modes['r'] {
		t.Fatalf("after REGISTER: account %q, modes %s", bee.Account, bee.ModeString())
	}
	if svc.AccountID(bee.UID) == 0 {
		t.Fatal("services don't know Bee is identified")
	}

	// A channel on the hub's network, registered by the remote user.
	if err := h.Emit(&state.Join{UID: bee.UID, Channel: "#hive", TS: 1}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	svc.Message(bee.UID, chanServ, "REGISTER #hive the hive")
	h.waitFor("Bee", "You are its Founder")
	if ch := h.net.Channel("#hive"); !ch.HasMode('r') {
		t.Errorf("#hive modes %v, want +r", ch.Modes)
	}

	// Someone else takes the registered nick and doesn't identify.
	h.Emit(&state.UserQuit{UID: bee.UID, Reason: "bye"})
	svc.Flush()
	other := &state.User{UID: "1LFAAAAAB", Nick: "Bee", Ident: "~x", Host: "x", Server: "leaf.test", Modes: map[byte]bool{}}
	h.Emit(&state.UserAdd{User: other})
	svc.Flush()
	h.waitFor("Bee", "registered and protected")
	svc.Tick(time.Now().Add(2 * time.Minute)) // past the identify deadline
	if other.Nick == "Bee" {
		t.Errorf("unidentified user kept the registered nick")
	}
}

// After a split both sides send their copy of a ban: the newest wins.
func TestRemoteBanNewestWins(t *testing.T) {
	s, _ := newServices(t)
	now := time.Now()
	newer := store.NetBan{Kind: store.BanAkill, Mask: "*@bad.example", Reason: "newer", SetBy: "a", SetAt: now}
	older := store.NetBan{Kind: store.BanAkill, Mask: "*@bad.example", Reason: "older", SetBy: "b", SetAt: now.Add(-time.Hour)}
	s.RemoteBan(newer, true)
	if changed, stale := s.RemoteBan(older, true); changed || stale {
		t.Error("older ban replaced the newer one")
	}
	if b := s.firstBan(store.BanAkill, "x", "bad.example", "", ""); b == nil || b.Reason != "newer" {
		t.Errorf("kept %+v", b)
	}
}

// A ban removed here while another server was split off stays removed
// when that server sends it back (it is stale), whoever removed it; one
// set again later, or one set during the split, comes through.
func TestRemoteBanRemovedDuringSplit(t *testing.T) {
	s, _ := newServices(t)
	set := time.Now().Add(-time.Hour).Truncate(time.Second)
	z := store.NetBan{Kind: store.BanZline, Mask: "192.0.2.32", Reason: "x", SetBy: "op", SetAt: set, Expires: time.Now().Add(time.Hour)}
	g := store.NetBan{Kind: store.BanAkill, Mask: "*@192.0.2.33", Reason: "y", SetBy: "op", SetAt: set}
	for _, b := range []store.NetBan{z, g} {
		if changed, _ := s.RemoteBan(b, true); !changed {
			t.Fatalf("%s not added", b.Mask)
		}
	}
	// One removed by an oper here, one by another server's DEL.
	if ok, err := s.store.DelNetBan(z.Kind, z.Mask); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s.reloadOperData()
	if changed, _ := s.RemoteBan(g, false); !changed {
		t.Fatal("remote DEL not applied")
	}
	older := g
	older.SetAt = set.Add(-time.Minute)
	for _, b := range []store.NetBan{z, g, older} {
		if changed, stale := s.RemoteBan(b, true); changed || !stale {
			t.Errorf("%s (set %v) back: changed %v, stale %v", b.Mask, b.SetAt, changed, stale)
		}
	}
	if len(s.LinkedBans()) != 0 {
		t.Errorf("bans back: %+v", s.LinkedBans())
	}
	again := z
	again.SetAt, again.Reason = set.Add(time.Minute), "set again"
	if changed, stale := s.RemoteBan(again, true); !changed || stale {
		t.Errorf("ban set again: changed %v, stale %v", changed, stale)
	}
	other := store.NetBan{Kind: store.BanShun, Mask: "*@192.0.2.34", Reason: "new", SetBy: "op", SetAt: set}
	if changed, stale := s.RemoteBan(other, true); !changed || stale {
		t.Errorf("new ban: changed %v, stale %v", changed, stale)
	}
}

// A spam filter removed here while another server was split off stays
// removed when that server sends it back (it is stale), whoever removed
// it; one set again later, or one set during the split, comes through.
func TestRemoteSpamFilterRemovedDuringSplit(t *testing.T) {
	s, _ := newServices(t)
	set := time.Now().Add(-time.Hour).Truncate(time.Second)
	mk := func(match string) spamfilter.Filter {
		return spamfilter.Filter{Type: spamfilter.Simple, Match: match, Targets: "cp", Action: spamfilter.Block, Reason: "x", SetBy: "op", SetAt: set}
	}
	a, b := mk("*by oper*"), mk("*by remote*")
	for _, f := range []spamfilter.Filter{a, b} {
		if changed, _ := s.RemoteSpamFilter(f, true); !changed {
			t.Fatalf("%s not added", f.Match)
		}
	}
	// One removed by an oper here, one by another server's removal.
	if _, err := s.DelSpamFilter(s.SpamFilters()[0].ID, "oper"); err != nil {
		t.Fatal(err)
	}
	if changed, _ := s.RemoteSpamFilter(b, false); !changed {
		t.Fatal("remote removal not applied")
	}
	older := b
	older.SetAt = set.Add(-time.Minute)
	for _, f := range []spamfilter.Filter{a, b, older} {
		if changed, stale := s.RemoteSpamFilter(f, true); changed || !stale {
			t.Errorf("%s (set %v) back: changed %v, stale %v", f.Match, f.SetAt, changed, stale)
		}
	}
	if len(s.SpamFilters()) != 0 {
		t.Errorf("rules back: %+v", s.SpamFilters())
	}
	again := a
	again.SetAt, again.Reason = set.Add(time.Minute), "set again"
	if changed, stale := s.RemoteSpamFilter(again, true); !changed || stale {
		t.Errorf("rule set again: changed %v, stale %v", changed, stale)
	}
	if changed, stale := s.RemoteSpamFilter(mk("*new*"), true); !changed || stale {
		t.Errorf("new rule: changed %v, stale %v", changed, stale)
	}
}
