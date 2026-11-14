package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// Hive Chat h1: members sign in to the Hive Panel with their NickServ
// password and get only their own pages.

const membersOn = "\n[web]\nmembers = true\n"

// memberLogin signs nick in with password and returns the session the
// panel would keep (As + Seal).
func memberSession(t *testing.T, s *Server, nick, password string) control.Request {
	t.Helper()
	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: nick, Password: password, IP: "127.0.0.1"})
	if r.Oper == nil {
		t.Fatalf("login %s: %+v", nick, r)
	}
	return control.Request{As: r.Oper.Account, Seal: r.Oper.Seal, IP: "127.0.0.1"}
}

func as(sess control.Request, cmd string) control.Request {
	sess.Cmd = cmd
	return sess
}

func TestPanelMembersOffByDefault(t *testing.T) {
	s, _, _ := fileServer(t, "")
	registered(t, s, "ember")
	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: pw, IP: "127.0.0.1"})
	if r.Oper != nil || r.Error != memberOffMsg {
		t.Fatalf("NickServ password with members off: %+v", r)
	}
	if h := panelAsk(t, s, control.Request{Cmd: control.CmdPanelHello}); h.Hello == nil || h.Hello.Members {
		t.Errorf("hello with members off: %+v", h)
	}
}

func TestPanelMemberPages(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	e := registered(t, s, "ember")
	if h := panelAsk(t, s, control.Request{Cmd: control.CmdPanelHello}); h.Hello == nil || !h.Hello.Members {
		t.Fatalf("hello with members on: %+v", h)
	}
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: "wrong-one", IP: "127.0.0.1"}); r.Oper != nil || r.Error != "Wrong nick or password." {
		t.Errorf("wrong NickServ password: %+v", r)
	}
	m := memberSession(t, s, "ember", pw)
	if who := panelAsk(t, s, as(m, control.CmdPanelWho)); who.Oper == nil || !who.Oper.Member || who.Oper.Flags != "" || who.Oper.Account != "ember" {
		t.Fatalf("panel-who for a member: %+v", who)
	}

	// Admin pages: refused as a missing right, not a sign-out.
	for _, cmd := range []string{control.CmdStatus, control.CmdPanelLog, control.CmdPeople, control.CmdSettings, control.CmdBans, control.CmdOpers} {
		if r := panelAsk(t, s, as(m, cmd)); r.Denied != "flag" || r.Status != nil || r.People != nil || len(r.Events) > 0 {
			t.Errorf("%s for a member: %+v", cmd, r)
		}
	}

	// My nick, and a channel where they are founder.
	e.send("JOIN #kampung")
	e.expect(" 366 ember #kampung ")
	e.send("PRIVMSG ChanServ :REGISTER #kampung")
	e.expect("#kampung is registered")
	my := panelAsk(t, s, as(m, control.CmdMyNick)).MyNick
	if my == nil || my.Name != "ember" || my.Kill != "on" || len(my.Channels) != 1 || my.Channels[0].Name != "#kampung" ||
		my.Channels[0].Level != "Founder" || my.Channels[0].Users != 1 {
		t.Fatalf("my-nick: %+v", my)
	}
	if !strings.Contains(my.Email, "•••@") && my.Email != "" {
		t.Errorf("email not masked: %q", my.Email)
	}

	// SET through the page is NickServ's own SET: its answer, its change.
	if r := panelAsk(t, s, func() control.Request { q := as(m, control.CmdMySet); q.Name, q.Value = "KILL", "OFF"; return q }()); r.Done == nil || !strings.Contains(r.Done.Text, "Kill protection is off for ember") {
		t.Fatalf("my-set KILL OFF: %+v", r)
	}
	if r := panelAsk(t, s, func() control.Request { q := as(m, control.CmdMySet); q.Name, q.Value = "HIDE", "EMAIL ON"; return q }()); r.Done == nil {
		t.Fatalf("my-set HIDE EMAIL ON: %+v", r)
	}
	if r := panelAsk(t, s, func() control.Request {
		q := as(m, control.CmdMySet)
		q.Name, q.Value = "GREET", "hi all, back again"
		return q
	}()); r.Done == nil {
		t.Fatalf("my-set GREET: %+v", r)
	}
	my = panelAsk(t, s, as(m, control.CmdMyNick)).MyNick
	if my.Kill != "off" || !my.HideEmail || my.Greet != "hi all, back again" {
		t.Errorf("after SET: %+v", my)
	}
	// Only the options the page offers; nothing that mails or renames.
	for _, opt := range []string{"PASSWORD", "EMAIL", "DISPLAY"} {
		q := as(m, control.CmdMySet)
		q.Name, q.Value = opt, "x@example.org"
		if r := panelAsk(t, s, q); r.Done != nil || r.Error == "" {
			t.Errorf("my-set %s: %+v", opt, r)
		}
	}
	// It shows on IRC as the same account's setting.
	e.send("PRIVMSG NickServ :INFO ember")
	e.expect("ember")

	// A new NickServ password ends the session.
	e.send("PRIVMSG NickServ :SET PASSWORD a-new-password-2026")
	e.expect("password")
	if r := panelAsk(t, s, as(m, control.CmdMyNick)); r.Denied != "signin" {
		t.Errorf("old member session after a new password: %+v", r)
	}
}

func TestPanelMemberSessionEndsWhenTurnedOff(t *testing.T) {
	s, path, _ := fileServer(t, membersOn)
	registered(t, s, "ember")
	m := memberSession(t, s, "ember", pw)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "members = true", "members = false", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Ask(s.cfg.Control.Socket, control.Request{Cmd: control.CmdRehash}, 25*time.Second); err != nil {
		t.Fatal(err)
	}
	if r := panelAsk(t, s, as(m, control.CmdMyNick)); r.Denied != "signin" || !strings.Contains(r.Error, "off") {
		t.Errorf("member session after members = false: %+v", r)
	}
}

// An oper signs in either way: the oper password opens the admin pages,
// the NickServ password only the member pages.
func TestPanelOperOrMember(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("ember is an oper now")

	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: emberPW, IP: "127.0.0.1"})
	if r.Oper == nil || r.Oper.Member || r.Oper.Flags != "hk" {
		t.Fatalf("oper password: %+v", r)
	}
	op := control.Request{As: r.Oper.Account, Seal: r.Oper.Seal, IP: "127.0.0.1"}
	if st := panelAsk(t, s, as(op, control.CmdStatus)); st.Status == nil {
		t.Errorf("status for the oper session: %+v", st)
	}
	if my := panelAsk(t, s, as(op, control.CmdMyNick)); my.MyNick == nil {
		t.Errorf("my-nick for the oper session: %+v", my)
	}

	m := memberSession(t, s, "ember", pw)
	if who := panelAsk(t, s, as(m, control.CmdPanelWho)); who.Oper == nil || !who.Oper.Member {
		t.Fatalf("NickServ password of an oper: %+v", who)
	}
	if st := panelAsk(t, s, as(m, control.CmdStatus)); st.Denied != "flag" {
		t.Errorf("status for the member session of an oper: %+v", st)
	}
}

// An oper who signs in with their NickServ password (the member pages) is
// not trying a wrong oper password: doing it again and again must never
// lock their /oper (release test 14 Nov 2026).
func TestPanelMemberSignInDoesNotLockOper(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	b := boss(t, s)
	e := registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("ember is an oper now")

	for i := 0; i < 7; i++ {
		memberSession(t, s, "ember", pw)
	}
	e.send("OPER ember " + emberPW)
	e.expect(" 381 ember ")
	r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: emberPW, IP: "127.0.0.1"})
	if r.Oper == nil || r.Oper.Member {
		t.Fatalf("oper password after member sign-ins: %+v", r)
	}
}

// A suspension that ran out lets the member in at once, as on IRC, before
// the hourly sweep clears it (release test 14 Nov 2026).
func TestPanelMemberSuspensionRanOut(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	registered(t, s, "ember")
	setSuspend := func(until time.Time) {
		s.onLoop(func() {
			a, _ := s.store.AccountByNick("ember")
			a.SuspendedBy, a.SuspendReason, a.SuspendedAt, a.SuspendUntil = "Boss", "test", time.Now().Add(-2*time.Hour), until
			if err := s.store.SaveAccount(a); err != nil {
				t.Error(err)
			}
		})
	}
	setSuspend(time.Now().Add(time.Hour))
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: pw, IP: "127.0.0.1"}); r.Oper != nil {
		t.Fatalf("suspended member signed in: %+v", r)
	}
	setSuspend(time.Now().Add(-time.Minute))
	m := memberSession(t, s, "ember", pw)
	if my := panelAsk(t, s, as(m, control.CmdMyNick)); my.MyNick == nil {
		t.Errorf("my-nick after the suspension ran out: %+v", my)
	}
	if r := panelAsk(t, s, as(m, control.CmdMyHome)); r.Home == nil {
		t.Errorf("home after the suspension ran out: %+v", r)
	}
}

// The Settings page's Members switch: on and off at once, saved to the file.
func TestPanelMembersSwitch(t *testing.T) {
	s, path, _ := fileServer(t, "")
	boss(t, s)
	registered(t, s, "ember")
	set := func(v string) *control.Response {
		return panelAsk(t, s, control.Request{Cmd: control.CmdSet, As: "Boss", IP: "127.0.0.1", Name: "members", Value: v})
	}
	if r := set("on"); r.Changed == nil || r.Changed.New != "on" {
		t.Fatalf("members on: %+v", r)
	}
	if h := panelAsk(t, s, control.Request{Cmd: control.CmdPanelHello}); h.Hello == nil || !h.Hello.Members {
		t.Errorf("hello after members on: %+v", h)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "members = true") {
		t.Errorf("not saved:\n%s", raw)
	}
	m := memberSession(t, s, "ember", pw)
	if r := set("off"); r.Changed == nil || r.Changed.New != "off" {
		t.Fatalf("members off: %+v", r)
	}
	if r := panelAsk(t, s, as(m, control.CmdMyNick)); r.Denied != "signin" {
		t.Errorf("member session after the switch went off: %+v", r)
	}
}

// With Members off, a right NickServ password is not a wrong password: it
// says sign-in is off and uses up no lock (QA 1.0.0 round, bug 2).
func TestPanelMembersOffRightPasswordNotCounted(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("ember is an oper now")
	for i := 0; i < 7; i++ {
		r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: pw, IP: "127.0.0.1"})
		if r.Oper != nil || !strings.HasPrefix(r.Error, "Member sign-in is off on this server.") {
			t.Fatalf("try %d: %+v", i, r)
		}
	}
	e.send("OPER ember " + emberPW)
	e.expect(" 381 ember ")
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "ember", Password: "not-it", IP: "127.0.0.1"}); r.Error != "Wrong nick or password." {
		t.Errorf("wrong password: %+v", r)
	}
	registered(t, s, "plain")
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "plain", Password: pw, IP: "127.0.0.1"}); !strings.HasPrefix(r.Error, "Member sign-in is off") {
		t.Errorf("member, members off: %+v", r)
	}
}
