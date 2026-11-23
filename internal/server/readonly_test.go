package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/control"

	"github.com/fhazal/beeircd/internal/services"
)

// readOnlyOn turns OperServ SET READONLY on as b (the config admin).
func readOnlyOn(t *testing.T, b *tc) {
	t.Helper()
	b.send("PRIVMSG OperServ :SET READONLY ON")
	b.expect("Services are now in read-only mode")
}

// While READONLY is on AdminServ refuses changes to oper entries (services
// data), UNDO of one included; what lives in beeircd.conf still changes.
func TestAdminServReadOnly(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS OPER ember ADD FLAGS g HOST *@*")
	b.expect(asLine("Boss", "ember is an oper now"))
	n := lastChange(t, b)
	readOnlyOn(t, b)

	refused := asLine("Boss", services.ReadonlyRefusal)
	for _, cmd := range []string{"AS OPER ember SET FLAGS gk", "AS OPER ember DISABLE", "AS OPER ember DEL",
		"AS OPER Boss SET TITLE Net Boss", "AS UNDO " + n} {
		b.send(cmd)
		b.expect(refused)
	}
	b.send("AS OPER LIST")
	b.expect(asLine("Boss", "2 opers."))
	b.send("AS CLASS staff ADD FLAGS hk")
	b.expect(asLine("Boss", "Class staff"))

	b.send("PRIVMSG OperServ :SET READONLY OFF")
	b.expect("no longer in read-only mode")
	b.send("AS OPER ember DEL")
	b.expect(asLine("Boss", "ember is no longer an oper."))
}

// While READONLY is on a spam filter's SHUN isn't stored: it holds for
// that connection only. Rules can't be added.
func TestSpamfilterReadOnlyShun(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "c shun 1h Quiet *join my server*")
	readOnlyOn(t, b)
	b.send("SPAMFILTER add c block - No *free nitro*")
	b.expect("Not added: services are in read-only mode")

	a, v := login(t, s, "Ann"), login(t, s, "Vic")
	for _, c := range []*tc{a, v} {
		c.send("JOIN #lobby")
		c.expect(" 366 " + c.nick + " #lobby ")
	}
	v.send("PRIVMSG #lobby :join my server now")
	v.send("PRIVMSG #lobby :and hello")
	v.sync()
	a.quiet("PRIVMSG #lobby")
	var shuns int
	s.onLoop(func() { shuns = len(s.svc.PanelBans("SHUN")) })
	if shuns != 0 {
		t.Fatalf("%d SHUNs stored in read-only mode", shuns)
	}
	// Nothing was stored, so a new connection from that IP may talk.
	w := login(t, s, "Vic2")
	w.send("JOIN #lobby")
	w.expect(" 366 Vic2 #lobby ")
	w.send("PRIVMSG #lobby :hi again")
	a.expect("PRIVMSG #lobby :hi again")
}

// While READONLY is on the Hive Panel opers form refuses to add, change or
// remove an oper entry (services data).
func TestPanelOpersReadOnly(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS OPER ember ADD FLAGS g HOST *@*")
	b.expect("ember is an oper now")
	readOnlyOn(t, b)
	ask := func(r control.Request) *control.Response {
		r.As, r.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}
	for _, r := range []control.Request{
		{Cmd: control.CmdOperSave, Name: "ember", Flags: "gk", Hosts: "*@*"},
		{Cmd: control.CmdOperDel, Name: "ember"},
	} {
		if got := ask(r); got.Error != services.ReadonlyRefusal {
			t.Errorf("%s: %+v", r.Cmd, got)
		}
	}
	b.send("AS OPER LIST")
	b.expect(asLine("Boss", "2 opers."))
}
