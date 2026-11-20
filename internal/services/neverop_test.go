package services

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/state"
)

// regChannel registers nick (pass honeypot42) and #hive as its founder.
func regChannel(t *testing.T, svc *Services, h *fakeHost, uid, nick string) *state.User {
	t.Helper()
	u := helpUser(t, h, uid, nick)
	svc.Message(u.UID, h.net.UserByNick("NickServ"), "REGISTER honeypot42")
	h.waitFor(nick, "registered")
	if err := h.Emit(&state.Join{UID: u.UID, Channel: "#hive", TS: 1}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	svc.Message(u.UID, h.net.UserByNick("ChanServ"), "REGISTER #hive the hive")
	h.waitFor(nick, "You are its Founder")
	return u
}

// NickServ SET NEVEROP keeps an account off access lists: ACCESS ADD,
// FLAGS and the xOP lists refuse it; an entry it has already may change.
func TestNeverOp(t *testing.T) {
	svc, h := newServices(t)
	ns, cs := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp")
	svc.Message(wasp.UID, ns, "REGISTER honeypot42")
	h.waitFor("Wasp", "registered")
	svc.Message(wasp.UID, ns, "SET NEVEROP ON")
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "can no longer be added") {
		t.Fatalf("SET NEVEROP ON: %q", got)
	}
	svc.Message(wasp.UID, ns, "INFO Wasp")
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "never op") {
		t.Errorf("INFO for the owner misses never op: %q", got)
	}
	svc.Message(bee.UID, ns, "INFO Wasp")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "never op") {
		t.Errorf("INFO for others shows never op: %q", got)
	}
	noticeTo(h, "Bee")
	for _, line := range []string{"ACCESS #hive ADD Wasp 5", "FLAGS #hive Wasp +v", "AOP #hive ADD Wasp", "VOP #hive ADD Wasp", "SOP #hive ADD Wasp"} {
		svc.Message(bee.UID, cs, line)
		if got := noticeTo(h, "Bee"); !strings.Contains(got, "Wasp has NEVEROP on and can't be added to access lists.") {
			t.Errorf("%s: %q", line, got)
		}
	}
	reg, _ := svc.store.ChannelByName("#hive")
	if lvl, _ := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); lvl != 0 {
		t.Fatalf("Wasp got level %d", lvl)
	}

	svc.Message(wasp.UID, ns, "SET NEVEROP OFF")
	noticeTo(h, "Wasp")
	svc.Message(bee.UID, cs, "VOP #hive ADD Wasp")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "added to the VOP list") {
		t.Fatalf("VOP ADD after NEVEROP OFF: %q", got)
	}
	// On again: the entry stays and may still be changed.
	svc.Message(wasp.UID, ns, "SET NEVEROP ON")
	noticeTo(h, "Wasp")
	svc.Message(bee.UID, cs, "AOP #hive ADD Wasp")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "NEVEROP") {
		t.Errorf("changing an existing entry was refused: %q", got)
	}
}
