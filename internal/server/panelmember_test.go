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
	if r.Oper != nil || r.Error != "Wrong nick or password." {
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
