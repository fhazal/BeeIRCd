package services

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// OperServ SET SUPERADMIN makes the oper who turns it on founder on every
// registered channel, needs oper flag a, and ends on de-oper and on quit.
func TestSuperAdmin(t *testing.T) {
	svc, h := newServices(t)
	ns, cs, os := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ"), h.net.UserByNick("OperServ")
	regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	small := helpUser(t, h, "1LFAAAAAC", "Small")
	svc.Message(boss.UID, ns, "REGISTER honeypot42")
	h.waitFor("Boss", "registered")
	h.opers = map[string]string{boss.UID: "oa", small.UID: "os"}

	svc.Message(small.UID, os, "SET SUPERADMIN ON")
	if got := noticeTo(h, "Small"); !strings.Contains(got, "You need oper flag a") {
		t.Fatalf("non-admin oper SET SUPERADMIN ON: %q", got)
	}
	if lvl := svc.JoinLevel(small.UID, "#hive"); lvl != store.LevelNone {
		t.Fatalf("refused oper got level %d", lvl)
	}
	svc.Message(boss.UID, cs, "SET SECUREFOUNDER #hive OFF")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Only the founder") {
		t.Fatalf("Boss changed a founder setting before SUPERADMIN: %q", got)
	}

	svc.Message(boss.UID, os, "SET SUPERADMIN ON")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "now a super admin") {
		t.Fatalf("SET SUPERADMIN ON: %q", got)
	}
	if lvl := svc.JoinLevel(boss.UID, "#hive"); lvl != store.LevelFounder {
		t.Errorf("super admin has level %d on #hive, want founder", lvl)
	}
	svc.Message(boss.UID, cs, "SET SECUREFOUNDER #hive OFF")
	if got := noticeTo(h, "Boss"); strings.Contains(got, "Only the founder") {
		t.Errorf("super admin refused a founder setting: %q", got)
	}
	svc.Message(boss.UID, os, "SET LIST")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "(super admin)") {
		t.Errorf("SET LIST for the super admin: %q", got)
	}
	svc.Message(small.UID, os, "SET LIST")
	if got := noticeTo(h, "Small"); strings.Contains(got, "(super admin)") {
		t.Errorf("SET LIST shows another oper as super admin: %q", got)
	}

	// De-oper: it ends.
	h.opers = map[string]string{small.UID: "os"}
	h.Emit(&state.UserMode{UID: boss.UID, Changes: []state.ModeChange{{Add: false, Mode: 'o'}}})
	svc.Flush()
	if svc.clients[boss.UID].superAdmin {
		t.Errorf("SUPERADMIN stayed on after de-oper")
	}
	if lvl := svc.JoinLevel(boss.UID, "#hive"); lvl != store.LevelNone {
		t.Errorf("de-opered Boss has level %d", lvl)
	}

	// Quit: it ends.
	h.opers = map[string]string{boss.UID: "oa", small.UID: "os"}
	svc.Message(boss.UID, os, "SET SUPERADMIN ON")
	noticeTo(h, "Boss")
	c := svc.clients[boss.UID]
	h.Emit(&state.UserQuit{UID: boss.UID, Reason: "bye"})
	svc.Flush()
	if c.superAdmin {
		t.Errorf("SUPERADMIN stayed on after quit")
	}
}
