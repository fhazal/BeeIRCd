package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// Step 3h: the Channels page.

func TestPanelChannels(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	a := login(t, s, "alice")
	c := login(t, s, "carol")
	b.send("JOIN #lobby")
	b.expect(" 366 Boss #lobby ")
	b.send("CS REGISTER #lobby")
	b.expect("registered")
	a.send("JOIN #lobby")
	a.expect(" 366 alice #lobby ")
	c.send("JOIN #lobby")
	c.expect(" 366 carol #lobby ")
	a.send("JOIN #small")
	a.expect(" 366 alice #small ")
	a.send("MODE #small +k sekrit")
	a.sync()
	ask := func(r control.Request) *control.Response {
		r.As, r.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}

	l := ask(control.Request{Cmd: control.CmdChannels}).Channels
	if l == nil || l.Open != 2 || l.Registered != 1 || l.Biggest != "#lobby" || l.BiggestN != 3 || len(l.Rows) != 2 || l.Rows[0].Name != "#lobby" || !l.Rows[0].Registered {
		t.Fatalf("list: %+v", l)
	}
	if l.Rows[1].Modes == "" || strings.Contains(l.Rows[1].Modes, "sekrit") {
		t.Errorf("modes (no key): %q", l.Rows[1].Modes)
	}
	if l := ask(control.Request{Cmd: control.CmdChannels, Filter: "unregistered"}).Channels; l.Matched != 1 || l.Rows[0].Name != "#small" {
		t.Errorf("unregistered: %+v", l)
	}
	if l := ask(control.Request{Cmd: control.CmdChannels, Query: "SMA"}).Channels; l.Matched != 1 {
		t.Errorf("search: %+v", l)
	}
	info := ask(control.Request{Cmd: control.CmdChannel, Name: "#small"}).Channel
	if info == nil || !info.HasKey || info.Members[0] != "@alice" {
		t.Errorf("#small: %+v", info)
	}
	info = ask(control.Request{Cmd: control.CmdChannel, Name: "#lobby"}).Channel
	if info == nil || info.Founder != "Boss" || info.Members[0][0] != '@' || info.People != 3 {
		t.Errorf("#lobby: %+v", info)
	}

	// Notice from Global, topic as ChanServ.
	if r := ask(control.Request{Cmd: control.CmdChanNotice, Name: "#lobby", Value: "hello all"}); r.Done == nil {
		t.Fatalf("notice: %+v", r)
	}
	a.expect("NOTICE #lobby :[Boss] hello all")
	if r := ask(control.Request{Cmd: control.CmdChanTopic, Name: "#lobby", Value: "Be kind"}); r.Done == nil {
		t.Fatalf("topic: %+v", r)
	}
	a.expect("TOPIC #lobby :Be kind")
	if r := ask(control.Request{Cmd: control.CmdChanTopic, Name: "#nowhere", Value: "x"}); !strings.Contains(r.Error, "empty") {
		t.Errorf("topic of an empty channel: %+v", r)
	}

	// Suspend: only registered channels; non-opers are removed.
	if r := ask(control.Request{Cmd: control.CmdChanSuspend, Name: "#small"}); !strings.Contains(r.Error, "isn't registered") {
		t.Errorf("suspend unregistered: %+v", r)
	}
	r := ask(control.Request{Cmd: control.CmdChanSuspend, Name: "#lobby", Reason: "spam", Life: "0"})
	if r.Done == nil || !strings.Contains(r.Done.Text, "2 removed") {
		t.Fatalf("suspend: %+v", r)
	}
	c.expect("KICK #lobby carol :This channel is suspended: spam")
	if l := ask(control.Request{Cmd: control.CmdChannels, Filter: "suspended"}).Channels; l.Matched != 1 || !l.Rows[0].Suspended || l.Suspended != 1 {
		t.Errorf("suspended filter: %+v", l)
	}
	if info := ask(control.Request{Cmd: control.CmdChannel, Name: "#lobby"}).Channel; info == nil || info.SuspendedBy != "Boss" || info.SuspendWhy != "spam" {
		t.Errorf("suspended info: %+v", info)
	}
	if r := ask(control.Request{Cmd: control.CmdChanUnsuspend, Name: "#lobby"}); r.Done == nil {
		t.Errorf("unsuspend: %+v", r)
	}
	if r := ask(control.Request{Cmd: control.CmdChanUnsuspend, Name: "#lobby"}); !strings.Contains(r.Error, "isn't suspended") {
		t.Errorf("unsuspend twice: %+v", r)
	}
}
