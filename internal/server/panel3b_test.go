package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// Step 3b: the People page.

func TestPanelPeople(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")
	b.send("AS CLASS watch ADD FLAGS g") // no h (hosts), no k (kill, bans)
	b.expect("watch")
	b.send("AS OPER ember ADD CLASS watch HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("ember is an oper now")
	g1 := login(t, s, "guesty")
	g2 := login(t, s, "rover")
	g1.send("JOIN #lobby")
	g1.expect(" 366 guesty #lobby ")
	_ = e
	_ = g2

	asBoss := func(r control.Request) *control.Response {
		r.As, r.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}
	asEmber := func(r control.Request) *control.Response {
		r.As, r.IP = "ember", "127.0.0.1"
		return panelAsk(t, s, r)
	}

	p := asBoss(control.Request{Cmd: control.CmdPeople}).People
	if p == nil || p.Online != 4 || p.LoggedIn != 2 || p.Guests != 2 || p.Opers != 1 || p.Matched != 4 {
		t.Fatalf("people: %+v", p)
	}
	if p.Rows[0].Nick != "Boss" || !p.Rows[0].Oper || p.Rows[3].Nick != "rover" {
		t.Errorf("rows (by nick, no services): %+v", p.Rows)
	}
	if p := asBoss(control.Request{Cmd: control.CmdPeople, Filter: "guests"}).People; p.Matched != 2 {
		t.Errorf("guests: %+v", p)
	}
	if p := asBoss(control.Request{Cmd: control.CmdPeople, Query: "ROV"}).People; p.Matched != 1 || p.Rows[0].Nick != "rover" {
		t.Errorf("search: %+v", p)
	}
	if p := asBoss(control.Request{Cmd: control.CmdPeople, Limit: 2, Offset: 2}).People; len(p.Rows) != 2 || p.Offset != 2 || p.Rows[0].Nick != "guesty" {
		t.Errorf("paging: %+v", p)
	}
	// Searching by address: only with flag h (Boss has it, ember the helper doesn't).
	if p := asBoss(control.Request{Cmd: control.CmdPeople, Query: "127.0.0"}).People; p.Matched != 4 {
		t.Errorf("Boss by address: %d", p.Matched)
	}

	uid := ""
	for _, r := range p.Rows {
		if r.Nick == "guesty" {
			uid = r.UID
		}
	}
	pr := asBoss(control.Request{Cmd: control.CmdPerson, UID: uid}).Person
	if pr == nil || pr.IP != "127.0.0.1" || pr.RealHost == "" || len(pr.Chans) != 1 || pr.Chans[0] != "@#lobby" || !pr.Local {
		t.Errorf("person for Boss: %+v", pr)
	}

	// ember (flag g only): no real hosts, no address search, no kill or ban.
	if who := asEmber(control.Request{Cmd: control.CmdPanelWho}).Oper; who == nil || who.Flags != "g" {
		t.Fatalf("ember: %+v", who)
	}
	if pr := asEmber(control.Request{Cmd: control.CmdPerson, UID: uid}).Person; pr == nil || pr.IP != "" || pr.RealHost != "" {
		t.Errorf("real host shown without flag h: %+v", pr)
	}
	// With cloaks on, a cloaked person can't be found by address without h.
	// (Cloaks are off in this config, so the others show their address to
	// everyone on IRC anyway.)
	b.send("AS SET cloak on")
	b.expect("cloak is now on")
	login(t, s, "cloaky")
	if p := asEmber(control.Request{Cmd: control.CmdPeople, Query: "127.0.0"}).People; p.Matched != 4 {
		t.Errorf("address search without flag h found %d (want the 4 uncloaked)", p.Matched)
	}
	if p := asBoss(control.Request{Cmd: control.CmdPeople, Query: "127.0.0"}).People; p.Matched != 5 {
		t.Errorf("address search with flag h found %d", p.Matched)
	}
	if r := asEmber(control.Request{Cmd: control.CmdKill, UID: uid}); r.Denied != "flag" {
		t.Errorf("kill without k: %+v", r)
	}
	if r := asEmber(control.Request{Cmd: control.CmdBan, UID: uid}); r.Denied != "flag" {
		t.Errorf("ban without k: %+v", r)
	}

	// Notice.
	if r := asBoss(control.Request{Cmd: control.CmdNotice, UID: uid, Value: "  "}); r.Error == "" {
		t.Errorf("empty notice: %+v", r)
	}
	if r := asBoss(control.Request{Cmd: control.CmdNotice, UID: uid, Value: "hello  there"}); r.Done == nil {
		t.Fatalf("notice: %+v", r)
	}
	g1.expect("*** Notice from Boss: hello there")

	// Disconnect.
	rUID := ""
	for _, r := range asBoss(control.Request{Cmd: control.CmdPeople, Query: "rover"}).People.Rows {
		rUID = r.UID
	}
	if r := asBoss(control.Request{Cmd: control.CmdKill, UID: rUID, Value: "bye"}); r.Done == nil || !strings.Contains(r.Done.Text, "rover was disconnected") {
		t.Fatalf("kill: %+v", r)
	}
	g2.expect("KILL rover :bye")
	if r := asBoss(control.Request{Cmd: control.CmdKill, UID: rUID}); r.Error != "They have left." {
		t.Errorf("kill again: %+v", r)
	}

	// Ban: AKILL on their address. Everyone here is on 127.0.0.1, so it
	// would hit everyone: refused.
	if r := asBoss(control.Request{Cmd: control.CmdBan, UID: uid, Value: "spam", Life: "7d"}); r.Done != nil || !strings.Contains(r.Error, "matches everyone") {
		t.Errorf("ban everyone: %+v", r)
	}
	if r := asBoss(control.Request{Cmd: control.CmdBan, UID: uid, Life: "forever"}); !strings.Contains(r.Error, "ban length") {
		t.Errorf("bad length: %+v", r)
	}
	// A ban that matches nobody here goes in like OperServ's.
	var mask, problem string
	s.onLoop(func() { mask, _, problem = s.svc.PanelAkill("Boss", "*@198.51.100.7", "test", -1) })
	if mask != "*@198.51.100.7" || problem != "" {
		t.Errorf("PanelAkill: %q %q", mask, problem)
	}
	s.onLoop(func() { _, _, problem = s.svc.PanelAkill("Boss", "*@198.51.100.7", "again", -1) })
	if !strings.Contains(problem, "already") {
		t.Errorf("same ban twice: %q", problem)
	}
	b.send("OS AKILL LIST")
	b.expect("*@198.51.100.7")

	ev := asBoss(control.Request{Cmd: control.CmdPanelLog}).Events
	if ev[0].What != "disconnected rover (bye)" || ev[1].What != "sent a notice to guesty" {
		t.Errorf("events: %+v", ev[:2])
	}
}

func TestPanelLife(t *testing.T) {
	for in, want := range map[string]bool{"": true, "0": true, "1h": true, "7d": true, "30d": true, "forever": false, "10s": false} {
		if _, ok := panelLife(in); ok != want {
			t.Errorf("%q: %v", in, ok)
		}
	}
}
