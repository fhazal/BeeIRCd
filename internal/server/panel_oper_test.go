package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// The oper pages that change services: People's Accounts, the Services
// tabs, Overview's notice to everyone and the Bans page's forbids.

func TestPanelOperPages(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS helper HOST *@127.0.0.0/8 PASSWORD " + emberPW) // h only
	b.expect("ember is an oper now")
	v := registered(t, s, "Vic")
	registered(t, s, "Wren")

	ask := func(as string, r control.Request) *control.Response {
		t.Helper()
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}
	do := func(r control.Request) string {
		t.Helper()
		r.Cmd = control.CmdServDo
		a := ask("Boss", r)
		if a.Error != "" || a.Done == nil {
			t.Fatalf("%s: %+v", r.Kind, a)
		}
		return a.Done.Text
	}
	account := func(name string) *control.Account {
		t.Helper()
		a := ask("Boss", control.Request{Cmd: control.CmdAccount, Name: name}).Account
		if a == nil {
			t.Fatalf("no account %s", name)
		}
		return a
	}
	svc := func(kind string) *control.Svc {
		t.Helper()
		r := ask("Boss", control.Request{Cmd: control.CmdSvcData, Kind: kind})
		if r.Svc == nil {
			t.Fatalf("svc %s: %+v", kind, r)
		}
		return r.Svc
	}

	// Flags: ember (h) gets the Vhosts tab only.
	for _, c := range []control.Request{
		{Cmd: control.CmdAccounts},
		{Cmd: control.CmdAccount, Name: "Vic"},
		{Cmd: control.CmdSvcData, Kind: "bots"},
		{Cmd: control.CmdSvcData, Kind: "news"},
		{Cmd: control.CmdSvcData, Kind: "sessions"},
		{Cmd: control.CmdSvcData, Kind: "defcon"},
		{Cmd: control.CmdServDo, Kind: "suspend", Name: "Vic"},
		{Cmd: control.CmdServDo, Kind: "bot-del", Name: "Nobody"},
		{Cmd: control.CmdServDo, Kind: "defcon", Value: "3"},
		{Cmd: control.CmdGlobal, Value: "hello"},
		{Cmd: control.CmdBans, Kind: "forbid"},
		{Cmd: control.CmdBanAdd, Kind: "forbid", Mask: "BadNick"},
	} {
		if r := ask("ember", c); r.Denied != "flag" {
			t.Errorf("%s %s as ember: %+v", c.Cmd, c.Kind, r.Error)
		}
	}
	if r := ask("ember", control.Request{Cmd: control.CmdSvcData, Kind: "vhosts"}); r.Svc == nil {
		t.Errorf("vhosts as ember: %+v", r.Error)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdSvcData, Kind: "nope"}); r.Error == "" {
		t.Errorf("unknown tab: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "nope"}); r.Error == "" {
		t.Errorf("unknown action: %+v", r)
	}

	// Accounts list and one account.
	l := ask("Boss", control.Request{Cmd: control.CmdAccounts}).Accounts
	if l == nil || l.Total != 4 || l.Online != 4 || len(l.Rows) != 4 {
		t.Fatalf("accounts: %+v", l)
	}
	if l := ask("Boss", control.Request{Cmd: control.CmdAccounts, Query: "vi"}).Accounts; l == nil || l.Matched != 1 || l.Rows[0].Name != "Vic" {
		t.Errorf("search vi: %+v", l)
	}
	if a := account("Boss"); a.Locked == "" {
		t.Errorf("owner's account not locked: %+v", a)
	}
	if a := account("ember"); a.Locked == "" || !a.Oper {
		t.Errorf("oper's account not locked: %+v", a)
	}
	if a := account("Vic"); a.Locked != "" || a.Suspended || len(a.NickList) != 1 {
		t.Errorf("Vic: %+v", a)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdAccount, Name: "nobody-here"}); r.Error == "" {
		t.Errorf("no such account: %+v", r)
	}

	// Suspend, unsuspend, email, password, drop.
	do(control.Request{Kind: "suspend", Name: "Vic", Life: "7d", Reason: "testing"})
	v.expect("suspended")
	if a := account("Vic"); !a.Suspended || a.SuspendReason != "testing" || a.SuspendUntil.IsZero() || a.SuspendedBy != "Boss" {
		t.Errorf("suspended: %+v", a)
	}
	if l := ask("Boss", control.Request{Cmd: control.CmdAccounts, Filter: "suspended"}).Accounts; l == nil || l.Suspended != 1 || len(l.Rows) != 1 {
		t.Errorf("suspended filter: %+v", l)
	}
	do(control.Request{Kind: "unsuspend", Name: "Vic"})
	if a := account("Vic"); a.Suspended {
		t.Errorf("still suspended: %+v", a)
	}
	do(control.Request{Kind: "email", Name: "Vic", Value: "vic@example.org"})
	if a := account("Vic"); a.Email != "vic@example.org" {
		t.Errorf("email: %+v", a)
	}
	do(control.Request{Kind: "email", Name: "Vic"})
	if a := account("Vic"); a.Email != "" {
		t.Errorf("email off: %+v", a)
	}
	do(control.Request{Kind: "password", Name: "Vic", Password: "Brand-new-pass-77"})
	v2 := login(t, s, "Vic2")
	v2.send("NS IDENTIFY Vic Brand-new-pass-77")
	v2.expect(nsLine("Vic2", "Password accepted - you are now recognized."))
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "password", Name: "Vic", Password: "x"}); r.Error == "" {
		t.Errorf("short password taken: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "suspend", Name: "ember"}); r.Error == "" {
		t.Errorf("an oper suspended: %+v", r)
	}
	do(control.Request{Kind: "drop", Name: "Wren"})
	if r := ask("Boss", control.Request{Cmd: control.CmdAccount, Name: "Wren"}); r.Account != nil {
		t.Errorf("Wren not dropped: %+v", r.Account)
	}

	// Vhosts: a request, approve; another, reject; set and delete. (The
	// suspend moved Vic off the nick; back on it.)
	v.send("NICK Vic")
	v.send("NS IDENTIFY Brand-new-pass-77")
	v.expect("Password accepted - you are now recognized.")
	v.send("HS REQUEST vic.example.org")
	v.expect("Your vHost has been requested.")
	if w := svc("vhosts").Waiting; len(w) != 1 || w[0].Nick != "Vic" || w[0].VHost != "vic.example.org" {
		t.Fatalf("waiting: %+v", w)
	}
	if r := ask("ember", control.Request{Cmd: control.CmdServDo, Kind: "vhost-ok", Name: "Vic"}); r.Error != "" || r.Done == nil {
		t.Fatalf("approve as ember: %+v", r)
	}
	if a := svc("vhosts").Active; len(a) != 1 || a[0].VHost != "vic.example.org" {
		t.Errorf("active: %+v", a)
	}
	v.send("HS REQUEST other.example.org")
	v.expect("Your vHost has been requested.")
	do(control.Request{Kind: "vhost-no", Name: "Vic", Reason: "not that one"})
	if w := svc("vhosts").Waiting; len(w) != 0 {
		t.Errorf("still waiting: %+v", w)
	}
	if got := do(control.Request{Kind: "vhost-set", Name: "Vic", Value: "set.example.org"}); got != "VHost for Vic set to set.example.org." {
		t.Errorf("vhost-set answer not plain text: %q", got)
	}
	if a := account("Vic"); a.VHost != "set.example.org" {
		t.Errorf("vhost set: %+v", a.VHost)
	}
	do(control.Request{Kind: "vhost-del", Name: "Vic"})
	if a := svc("vhosts").Active; len(a) != 0 {
		t.Errorf("vhost not deleted: %+v", a)
	}

	// Bots.
	do(control.Request{Kind: "bot-add", Name: "Buzz", Mask: "buzz@bots.example.org", Reason: "The Buzz bot"})
	if bs := svc("bots").Bots; len(bs) != 1 || bs[0].Nick != "Buzz" || bs[0].Host != "bots.example.org" || bs[0].Real != "The Buzz bot" {
		t.Fatalf("bot added: %+v", bs)
	}
	do(control.Request{Kind: "bot-change", Name: "Buzz", Value: "Hum", Mask: "hum@bots.example.org"})
	if bs := svc("bots").Bots; len(bs) != 1 || bs[0].Nick != "Hum" || bs[0].User != "hum" {
		t.Errorf("bot changed: %+v", bs)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "bot-add", Name: "Bad", Mask: "no host"}); r.Error == "" {
		t.Errorf("bad user@host taken: %+v", r)
	}
	do(control.Request{Kind: "bot-del", Name: "Hum"})
	if bs := svc("bots").Bots; len(bs) != 0 {
		t.Errorf("bot not deleted: %+v", bs)
	}

	// News.
	do(control.Request{Kind: "news-add", Name: "logon", Value: "Welcome to the hive"})
	do(control.Request{Kind: "news-add", Name: "logon", Value: "Second line"})
	var logon control.NewsList
	for _, n := range svc("news").News {
		if n.Kind == "logon" {
			logon = n
		}
	}
	if len(logon.Items) != 2 || logon.Items[0].Text != "Welcome to the hive" || logon.About == "" || logon.Max == 0 {
		t.Fatalf("logon news: %+v", logon)
	}
	login(t, s, "fresh").expect("Welcome to the hive")
	do(control.Request{Kind: "news-del", Name: "logon", ID: logon.Items[1].ID})
	for _, n := range svc("news").News {
		if n.Kind == "logon" && (len(n.Items) != 1 || n.Items[0].Text != "Welcome to the hive") {
			t.Errorf("news after delete: %+v", n)
		}
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "news-del", Name: "logon", ID: logon.Items[1].ID}); r.Error == "" {
		t.Errorf("deleted twice: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "news-add", Name: "weather", Value: "x"}); r.Error == "" {
		t.Errorf("unknown news kind: %+v", r)
	}

	// Sessions.
	ss := svc("sessions")
	if ss.SessionLimit != 100 || len(ss.Busiest) != 1 || ss.Busiest[0].IP != "127.0.0.1" || ss.Busiest[0].Count < 5 {
		t.Errorf("sessions: %+v", ss)
	}
	do(control.Request{Kind: "except-add", Mask: "192.0.2.*", Value: "20", Reason: "office", Life: "1d"})
	if ex := svc("sessions").Excepts; len(ex) != 1 || ex[0].Mask != "192.0.2.*" || ex[0].Limit != 20 || ex[0].Expires.IsZero() {
		t.Errorf("exception: %+v", ex)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "except-add", Mask: "192.0.2.*", Value: "lots"}); r.Error == "" {
		t.Errorf("bad limit taken: %+v", r)
	}
	do(control.Request{Kind: "except-del", Mask: "192.0.2.*"})
	if ex := svc("sessions").Excepts; len(ex) != 0 {
		t.Errorf("exception not removed: %+v", ex)
	}

	// DEFCON and IGNORE.
	d := svc("defcon")
	if d.Defcon != 5 || len(d.Levels) != 5 || !d.CanDefcon || !d.CanIgnore {
		t.Errorf("defcon: %+v", d)
	}
	do(control.Request{Kind: "defcon", Value: "4"})
	if d := svc("defcon"); d.Defcon != 4 {
		t.Errorf("defcon 4: %+v", d.Defcon)
	}
	do(control.Request{Kind: "defcon", Value: "5"})
	if r := ask("Boss", control.Request{Cmd: control.CmdServDo, Kind: "defcon", Value: "9"}); r.Error == "" {
		t.Errorf("defcon 9: %+v", r)
	}
	do(control.Request{Kind: "ignore-add", Mask: "*!*@198.51.100.7", Life: "1h", Reason: "noisy"})
	if g := svc("defcon").Ignores; len(g) != 1 || g[0].Reason != "noisy" || g[0].Expires.IsZero() {
		t.Errorf("ignore: %+v", g)
	}
	do(control.Request{Kind: "ignore-del", Mask: svc("defcon").Ignores[0].Mask})
	if g := svc("defcon").Ignores; len(g) != 0 {
		t.Errorf("ignore not removed: %+v", g)
	}

	// Notice to everyone.
	if r := ask("Boss", control.Request{Cmd: control.CmdGlobal}); r.Error == "" {
		t.Errorf("empty notice sent: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdGlobal, Value: "Hello hive"}); r.Error != "" {
		t.Fatalf("global: %+v", r)
	}
	v.expect("Hello hive")

	// Forbids on the Bans page.
	if m := ask("Boss", control.Request{Cmd: control.CmdBanMatch, Kind: "forbid", Mask: "Vic*"}).Match; m == nil || m.People != 2 {
		t.Errorf("forbid match Vic*: %+v", m)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdBanMatch, Kind: "forbid", Mask: "*"}); r.Error == "" {
		t.Errorf("forbid * allowed: %+v", r)
	}
	r := ask("Boss", control.Request{Cmd: control.CmdBanAdd, Kind: "forbid", Mask: "Vic2", Reason: "taken", Life: "7d"})
	if r.Error != "" || r.Done == nil || !strings.Contains(r.Done.Text, " on Vic2 ") {
		t.Fatalf("forbid add: %+v", r)
	}
	if bl := ask("Boss", control.Request{Cmd: control.CmdBans, Kind: "forbid"}).Bans; bl == nil || len(bl.Rows) != 1 || bl.Counts["forbid"] != 1 {
		t.Errorf("forbid list: %+v", bl)
	}
	r = ask("Boss", control.Request{Cmd: control.CmdBanDel, Kind: "forbid", Mask: "Vic2"})
	if r.Error != "" || r.Done == nil {
		t.Errorf("forbid del: %+v", r)
	}
	if bl := ask("Boss", control.Request{Cmd: control.CmdBans, Kind: "forbid"}).Bans; bl == nil || len(bl.Rows) != 0 {
		t.Errorf("forbid still listed: %+v", bl)
	}

	// Every change is on the panel's log.
	evs := ask("Boss", control.Request{Cmd: control.CmdPanelLog}).Events
	text := ""
	for _, e := range evs {
		text += e.What + "\n"
	}
	for _, want := range []string{"sent a notice to everyone", "added a forbid", "removed a forbid"} {
		if !strings.Contains(text, want) {
			t.Errorf("panel log lacks %q:\n%s", want, text)
		}
	}
}
