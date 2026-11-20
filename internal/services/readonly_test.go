package services

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// OperServ SET READONLY refuses commands that change data, lets the ones
// that only look through, and needs oper flag a.
func TestReadOnly(t *testing.T) {
	svc, h := newServices(t)
	ns, cs, os := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ"), h.net.UserByNick("OperServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	small := helpUser(t, h, "1LFAAAAAC", "Small")
	wasp := helpUser(t, h, "1LFAAAAAD", "Wasp")
	h.opers = map[string]string{boss.UID: "oa", small.UID: "os"}

	svc.Message(small.UID, os, "SET READONLY ON")
	if got := noticeTo(h, "Small"); !strings.Contains(got, "You need oper flag a") || svc.readonly {
		t.Fatalf("non-admin oper turned READONLY on: %q", got)
	}
	svc.Message(boss.UID, os, "SET READONLY ON")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "now in read-only mode") || !svc.readonly {
		t.Fatalf("SET READONLY ON: %q", got)
	}
	svc.Message(small.UID, os, "SET LIST")
	if got := noticeTo(h, "Small"); !strings.Contains(got, "READONLY   ON") {
		t.Errorf("SET LIST: %q", got)
	}

	svc.Message(wasp.UID, ns, "REGISTER honeypot42")
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, readonlyRefusal) {
		t.Errorf("NS REGISTER in read-only mode: %q", got)
	}
	if a, _ := svc.store.AccountByNick("Wasp"); a != nil {
		t.Fatalf("Wasp got registered in read-only mode")
	}
	svc.Message(bee.UID, cs, "SET DESC #hive a new description")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, readonlyRefusal) {
		t.Errorf("CS SET in read-only mode: %q", got)
	}
	svc.Message(bee.UID, cs, "ACCESS #hive ADD Boss 5")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, readonlyRefusal) {
		t.Errorf("CS ACCESS ADD in read-only mode: %q", got)
	}
	svc.Message(bee.UID, cs, "INFO #hive")
	if got := noticeTo(h, "Bee"); strings.Contains(got, readonlyRefusal) || !strings.Contains(got, "the hive") {
		t.Errorf("CS INFO in read-only mode: %q", got)
	}
	svc.Message(bee.UID, cs, "ACCESS #hive LIST")
	if got := noticeTo(h, "Bee"); strings.Contains(got, readonlyRefusal) {
		t.Errorf("CS ACCESS LIST in read-only mode: %q", got)
	}
	svc.Message(bee.UID, ns, "INFO Bee")
	if got := noticeTo(h, "Bee"); strings.Contains(got, readonlyRefusal) {
		t.Errorf("NS INFO in read-only mode: %q", got)
	}

	svc.Message(boss.UID, os, "SET READONLY OFF")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "no longer in read-only mode") || svc.readonly {
		t.Fatalf("SET READONLY OFF: %q", got)
	}
	svc.Message(wasp.UID, ns, "REGISTER honeypot42")
	h.waitFor("Wasp", "registered")
}

// expireSetup registers Bee and #hive, has Bee leave, and
// returns a time far enough ahead that both are past their expiry.
func expireSetup(t *testing.T) (*Services, *fakeHost, time.Time) {
	t.Helper()
	svc, h := newServices(t)
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	h.Emit(&state.UserQuit{UID: bee.UID, Reason: "bye"})
	svc.Flush()
	return svc, h, time.Now().Add(10 * 365 * 24 * time.Hour)
}

// Nothing expires while READONLY is on; it does once it is off.
func TestReadOnlyStopsExpiry(t *testing.T) {
	svc, _, later := expireSetup(t)
	svc.readonly = true
	svc.expireChannels(later)
	svc.expireAccounts(later)
	if reg, _ := svc.store.ChannelByName("#hive"); reg == nil {
		t.Fatalf("#hive expired in read-only mode")
	}
	if a, _ := svc.store.AccountByNick("Bee"); a == nil {
		t.Fatalf("Bee expired in read-only mode")
	}
	svc.readonly = false
	svc.expireChannels(later)
	svc.expireAccounts(later)
	if reg, _ := svc.store.ChannelByName("#hive"); reg != nil {
		t.Errorf("#hive did not expire with READONLY off")
	}
	if a, _ := svc.store.AccountByNick("Bee"); a != nil {
		t.Errorf("Bee did not expire with READONLY off")
	}
}
