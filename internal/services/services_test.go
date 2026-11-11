package services

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
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
func (h *fakeHost) Numeric(uid, code string, params ...string)                                  {}
func (h *fakeHost) DefaultHost(uid string) string                                               { return "" }
func (h *fakeHost) ChannelMessage(from, channel string, status byte, privmsg bool, text string) {}
func (h *fakeHost) ServerNotice(uid, text string)                                               {}
func (h *fakeHost) Snotice(text string)                                                         {}
func (h *fakeHost) Disconnect(uid, reason string) {
	h.Emit(&state.UserQuit{UID: uid, Reason: reason})
}
func (h *fakeHost) Kill(uid, by, reason string)          { h.Disconnect(uid, "Killed") }
func (h *fakeHost) OperFlags(uid string) string          { return "" }
func (h *fakeHost) AccountGone(id int64)                 {}
func (h *fakeHost) IsAdminAccount(a *store.Account) bool { return false }
func (h *fakeHost) CheckModeParam(channel string, m byte, arg string) (string, string) {
	return arg, ""
}

func (h *fakeHost) ApplyChannelModes(uid, srcUID, channel, modes string, args []string) {
}
func (h *fakeHost) ApplySetting(uid, bot, key, value string)            {}
func (h *fakeHost) Post(f func())                                       { h.posts <- f }
func (h *fakeHost) Stats() HostStats                                    { return HostStats{Started: time.Now()} }
func (h *fakeHost) ResetPeak()                                          {}
func (h *fakeHost) ForceJoin(uid, channel string)                       {}
func (h *fakeHost) JoinBlocked(uid, channel, key string) (string, bool) { return "", false }
func (h *fakeHost) SetUserModes(uid, src, modes string) bool            { return false }

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
	h.waitFor("Bee", "is a registered nick")
	svc.Tick(time.Now().Add(2 * time.Minute)) // past the identify deadline
	if other.Nick == "Bee" {
		t.Errorf("unidentified user kept the registered nick")
	}
}
