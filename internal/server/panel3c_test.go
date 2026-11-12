package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// Step 3c: the Bans page.

func TestPanelBans(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS helper HOST *@127.0.0.0/8 PASSWORD " + emberPW) // h only
	b.expect("ember is an oper now")
	_ = e
	login(t, s, "guesty")

	ask := func(as string, r control.Request) *control.Response {
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}

	// Flag k for all of it.
	for _, cmd := range []string{control.CmdBans, control.CmdBanAdd, control.CmdBanDel, control.CmdBanMatch} {
		if r := ask("ember", control.Request{Cmd: cmd, Kind: "akill", Mask: "*@198.51.100.1"}); r.Denied != "flag" {
			t.Errorf("%s without k: %+v", cmd, r)
		}
	}

	r := ask("Boss", control.Request{Cmd: control.CmdBans})
	if r.Bans == nil || r.Bans.Kind != "akill" || len(r.Bans.Rows) != 0 || r.Bans.Default != "30d" || len(r.Bans.Counts) != 4 {
		t.Fatalf("empty list: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdBans, Kind: "kline"}); r.Error == "" {
		t.Errorf("kline is not a panel kind: %+v", r)
	}

	// Preview while typing.
	if m := ask("Boss", control.Request{Cmd: control.CmdBanMatch, Kind: "akill", Mask: "*@127.0.0.1"}).Match; m == nil || m.People != 3 {
		t.Errorf("match everyone here: %+v", m)
	}
	if m := ask("Boss", control.Request{Cmd: control.CmdBanMatch, Kind: "akill", Mask: "198.51.100.1"}).Match; m == nil || m.People != 0 || m.Mask != "*@198.51.100.1" {
		t.Errorf("match nobody, mask completed: %+v", m)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdBanMatch, Kind: "akill", Mask: "*@*"}); r.Error == "" {
		t.Errorf("*@* allowed: %+v", r)
	}

	// Add each kind, list, remove.
	adds := []control.Request{
		{Kind: "akill", Mask: "*@198.51.100.1", Reason: "spam", Life: "7d"},
		{Kind: "zline", Mask: "198.51.100.0/24", Reason: "botnet", Life: "1h"},
		{Kind: "shun", Mask: "*@203.0.113.5", Reason: "quiet please", Life: "0"},
		{Kind: "eline", Mask: "*@staff.example.org", Reason: "staff"},
	}
	for _, a := range adds {
		a.Cmd = control.CmdBanAdd
		if r := ask("Boss", a); r.Done == nil {
			t.Errorf("add %s: %+v", a.Kind, r)
		}
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdBanAdd, Kind: "akill", Mask: "*@198.51.100.1", Reason: "again"}); !strings.Contains(r.Error, "already an AKILL") {
		t.Errorf("twice: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdBanAdd, Kind: "akill", Mask: "*@127.0.0.1"}); !strings.Contains(r.Error, "matches everyone") {
		t.Errorf("everyone: %+v", r)
	}
	bans := ask("Boss", control.Request{Cmd: control.CmdBans, Kind: "eline"}).Bans
	if bans.Counts["akill"] != 1 || bans.Counts["zline"] != 1 || bans.Counts["shun"] != 1 || bans.Counts["eline"] != 1 {
		t.Errorf("counts: %+v", bans.Counts)
	}
	if len(bans.Rows) != 1 || bans.Rows[0].Mask != "*@staff.example.org" || !bans.Rows[0].Expires.IsZero() || bans.Rows[0].SetBy != "Boss" {
		t.Errorf("exception row (no expiry by default): %+v", bans.Rows)
	}
	b.send("OS AKILL LIST")
	b.expect("*@198.51.100.1")
	b.send("OS SHUN LIST")
	b.expect("*@203.0.113.5")

	if r := ask("Boss", control.Request{Cmd: control.CmdBanDel, Kind: "zline", Mask: "198.51.100.0/24"}); r.Done == nil {
		t.Errorf("del: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdBanDel, Kind: "zline", Mask: "198.51.100.0/24"}); !strings.Contains(r.Error, "no ZLINE") {
		t.Errorf("del twice: %+v", r)
	}
	// "What happened" names the kind, not the mask.
	ev := ask("Boss", control.Request{Cmd: control.CmdPanelLog}).Events
	if ev[0].What != "removed a ZLINE" || ev[1].What != "added an Exception until removed" || strings.Contains(ev[4].What, "198.51") {
		t.Errorf("events: %+v", ev[:5])
	}
}

func TestLengthWords(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Hour: "1 hour", 7 * 24 * time.Hour: "7 days", 90 * time.Minute: "90 minutes", 24 * time.Hour: "1 day", 2 * time.Hour: "2 hours"} {
		if got := lengthWords(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}
