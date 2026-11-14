package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// QA 14 Nov 2026: wrong passwords locked the account (or oper entry) for
// everyone, so a stranger could keep any nick or oper locked out with 5
// tries per 10 minutes. Now they lock only the source they came from.
func TestWrongPasswordsLockOnlyTheirSource(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s) // Boss, oper password operPW
	// Both addresses may oper, so only the lock tells them apart.
	s.onLoop(func() {
		s.cfg.Services.AdminHosts = append(s.cfg.Services.AdminHosts, "*@203.0.113.9", "*@198.51.100.7")
	})
	stranger := func(ip string) *control.Response {
		return panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "Boss", Password: "wrong-guess-1", IP: ip})
	}
	for i := 0; i < 5; i++ {
		stranger("203.0.113.9")
	}
	if r := stranger("203.0.113.9"); !strings.Contains(r.Error, "Too many wrong passwords") {
		t.Fatalf("the stranger's own source should be locked: %+v", r)
	}
	// The real oper, from elsewhere, still gets in: the panel and /oper.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "Boss", Password: operPW, IP: "198.51.100.7"}); r.Oper == nil {
		t.Errorf("owner's panel sign-in: %+v", r)
	}
	b.send("MODE Boss -o")
	b.send("OPER Boss " + operPW)
	b.expect(" 381 Boss ")
}
