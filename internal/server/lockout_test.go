package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// QA 14 Nov 2026: wrong passwords locked the account for everyone, so a
// stranger could keep any nick locked out with 5 lines per 10 minutes.
// Now they lock only the source they came from.
func TestWrongPasswordsLockOnlyTheirSource(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	registered(t, s, "victim")
	stranger := func(ip string) *control.Response {
		return panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "victim", Password: "wrong-guess", IP: ip})
	}
	for i := 0; i < 5; i++ {
		stranger("203.0.113.9")
	}
	if r := stranger("203.0.113.9"); !strings.Contains(r.Error, "Too many wrong passwords") {
		t.Fatalf("the stranger's own source should be locked: %+v", r)
	}
	// The owner, from elsewhere, still gets in: panel, IDENTIFY and SASL.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdPanelLogin, Nick: "victim", Password: pw, IP: "198.51.100.7"}); r.Oper == nil {
		t.Errorf("owner's panel sign-in: %+v", r)
	}
	o := login(t, s, "owner1")
	o.send("NS IDENTIFY victim " + pw)
	o.expect("Password accepted")
	if got := saslWith(t, s, "owner2", "victim", pw); got != "903" {
		t.Errorf("owner's SASL: %s", got)
	}
}
