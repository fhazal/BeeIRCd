package server

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// The Hive Panel's side of the control socket.

func panelAsk(t *testing.T, s *Server, req control.Request) *control.Response {
	t.Helper()
	if req.As != "" && req.Seal == "" {
		// As the panel does: the seal it got at sign-in.
		s.onLoop(func() {
			if po, _, _, _ := s.panelOper(req.As, req.IP); po != nil {
				req.Seal = po.Seal
			}
		})
	}
	resp, err := control.Ask(s.cfg.Control.Socket, req, 25*time.Second)
	if resp == nil {
		t.Fatalf("%s: %v", req.Cmd, err)
	}
	return resp
}

func TestPanelLoginAndFlags(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s) // the config admin, oper password set by the first /oper
	e := registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect(asLine("Boss", "ember is an oper now: class globop, hk (hosts, kill), from *@127.0.0.0/8."))
	_ = e

	// Wrong password, unknown nick, no oper entry: all the same answer.
	for _, req := range []control.Request{
		{Cmd: control.CmdPanelLogin, Nick: "Boss", Password: "nope", IP: "127.0.0.1"},
		{Cmd: control.CmdPanelLogin, Nick: "nobody", Password: "nope", IP: "127.0.0.1"},
		{Cmd: control.CmdPanelLogin, Nick: "ember", Password: emberPW, IP: "203.0.113.9"}, // host not covered
	} {
		r := panelAsk(t, s, req)
		if r.Oper != nil || r.Error != "Wrong nick or password." || r.Denied != "signin" {
			t.Errorf("%+v: %+v", req, r)
		}
	}

	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "boss", Password: operPW, IP: "127.0.0.1"})
	if r.Oper == nil || r.Oper.Account != "Boss" || r.Oper.Flags != "abcghks" || !r.Oper.Owner {
		t.Fatalf("owner login: %+v", r)
	}
	r = panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: emberPW, IP: "127.0.0.1"})
	if r.Oper == nil || r.Oper.Flags != "hk" || r.Oper.Owner || r.Oper.Class != "globop" {
		t.Fatalf("ember login: %+v", r)
	}

	// Status and What happened for any oper; settings need a.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdStatus, As: "ember", IP: "127.0.0.1"}); r.Status == nil || r.Status.MaxUsers == 0 || !r.Status.ServicesOn {
		t.Errorf("status for ember: %+v", r)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdSettings, As: "ember", IP: "127.0.0.1"}); r.Denied != "flag" || !strings.Contains(r.Error, "flag a") {
		t.Errorf("settings for ember: %+v", r)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdBackup, As: "ember", IP: "127.0.0.1"}); r.Denied != "flag" || r.Backup != nil {
		t.Errorf("backup for ember: %+v", r)
	}
	ev := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLog, As: "ember", IP: "127.0.0.1"}).Events
	if len(ev) < 4 || ev[0].Who != "ember" || ev[0].What != "signed in" || ev[0].IP != "" {
		t.Errorf("events for ember (no addresses without flag a): %+v", ev)
	}
	ev = panelAsk(t, s, control.Request{Cmd: control.CmdPanelLog, As: "Boss", IP: "127.0.0.1"}).Events
	if ev[0].IP != "127.0.0.1" || !strings.HasPrefix(ev[len(ev)-1].What, "could not sign in: wrong password") {
		t.Errorf("events for Boss: %+v", ev)
	}

	// Settings: change, check, undo.
	st := panelAsk(t, s, control.Request{Cmd: control.CmdSettings, As: "Boss", IP: "127.0.0.1"}).Settings
	var mu *control.Setting
	for i := range st {
		if st[i].Name == "max-users" {
			mu = &st[i]
		}
	}
	if mu == nil || mu.Kind != "number" || mu.Min != 1 || mu.Max != 100000 {
		t.Fatalf("settings: %+v", st)
	}
	before := mu.Value
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdSet, As: "Boss", IP: "127.0.0.1", Name: "max-users", Value: "0"}); r.Changed != nil || !strings.Contains(r.Error, "from 1 to 100000") {
		t.Errorf("bad value: %+v", r)
	}
	r = panelAsk(t, s, control.Request{Cmd: control.CmdSet, As: "Boss", IP: "127.0.0.1", Name: "max-users", Value: "4321"})
	if r.Changed == nil || r.Changed.New != "4321" || r.Changed.Old != before || r.Changed.ID == 0 {
		t.Fatalf("set: %+v", r)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "max_users = 4321") {
		t.Errorf("not saved to beeircd.conf:\n%s", raw)
	}
	b.send("AS HISTORY")
	b.expect("max-users")
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdUndo, As: "Boss", IP: "127.0.0.1", ID: r.Changed.ID}); r.Changed == nil || r.Changed.New != before {
		t.Errorf("undo: %+v", r)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdStatus, As: "Boss", IP: "127.0.0.1"}); r.Status.MaxUsers == 4321 {
		t.Errorf("undo not applied: %d", r.Status.MaxUsers)
	}

	// Taking ember's oper entry away stops them at the next click.
	b.send("AS OPER ember DEL")
	b.expect("ember")
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdStatus, As: "ember", IP: "127.0.0.1"}); r.Denied != "signin" || r.Status != nil {
		t.Errorf("removed oper still in: %+v", r)
	}
	// A request without As is the command line, as before.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdStatus}); r.Status == nil {
		t.Errorf("CLI status: %+v", r)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdSettings}); r.Denied != "signin" {
		t.Errorf("settings without an oper: %+v", r)
	}
}

func TestPanelLockout(t *testing.T) {
	s, _, _ := fileServer(t, "")
	boss(t, s)
	for i := 0; i < 5; i++ {
		panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "Boss", Password: "wrong", IP: "127.0.0.1"})
	}
	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "Boss", Password: operPW, IP: "127.0.0.1"})
	if r.Oper != nil || !strings.Contains(r.Error, "Too many wrong passwords") {
		t.Errorf("not locked: %+v", r)
	}
}

// A new oper password ends the sessions made with the old one.
func TestPanelSealChangesWithPassword(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")
	_ = e
	b.send("AS OPER ember ADD CLASS helper HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("ember is an oper now")
	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: emberPW, IP: "127.0.0.1"})
	if r.Oper == nil || r.Oper.Seal == "" {
		t.Fatalf("login: %+v", r)
	}
	seal := r.Oper.Seal
	ask := func() *control.Response {
		return panelAsk(t, s, control.Request{Cmd: control.CmdStatus, As: "ember", IP: "127.0.0.1", Seal: seal})
	}
	if r := ask(); r.Status == nil {
		t.Fatalf("status with seal: %+v", r)
	}
	b.send("AS OPER ember SET PASSWORD New-Ember-2026")
	b.expect("ember has a new oper password.")
	if r := ask(); r.Denied != "signin" || !strings.Contains(r.Error, "password changed") {
		t.Errorf("old session still works after a new password: %+v", r)
	}
}

// A refused sign-in for a nick that isn't an oper costs a password check
// too, so timing doesn't tell oper nicks apart.
func TestPanelLoginSameCost(t *testing.T) {
	s, _, _ := fileServer(t, "")
	boss(t, s)
	var calls atomic.Int32
	old := panelDummyCheck
	panelDummyCheck = func(pw string) { calls.Add(1); old(pw) }
	t.Cleanup(func() { panelDummyCheck = old })
	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "nobody", Password: "x", IP: "127.0.0.1"})
	if r.Oper != nil || r.Error != "Wrong nick or password." || calls.Load() != 1 {
		t.Errorf("unknown nick: %+v, dummy checks %d", r, calls.Load())
	}
}
