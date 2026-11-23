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
	// On again: the entry stays and may still change below op level, but
	// can't be raised to op level or above by any path.
	svc.Message(wasp.UID, ns, "SET NEVEROP ON")
	noticeTo(h, "Wasp")
	const refused = "Wasp has NEVEROP on and can't be added to access lists."
	for _, line := range []string{"AOP #hive ADD Wasp", "SOP #hive ADD Wasp", "ACCESS #hive ADD Wasp 5", "ACCESS #hive ADD Wasp 100", "FLAGS #hive Wasp +o", "FLAGS #hive MODIFY Wasp +O"} {
		svc.Message(bee.UID, cs, line)
		if got := noticeTo(h, "Bee"); !strings.Contains(got, refused) {
			t.Errorf("raise %s: %q", line, got)
		}
	}
	if lvl, _ := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); lvl != 3 {
		t.Fatalf("Wasp's VOP entry became level %d", lvl)
	}
	svc.Message(bee.UID, cs, "ACCESS #hive ADD Wasp 4")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "NEVEROP") {
		t.Errorf("a change below op level was refused: %q", got)
	}

	// An entry at op level from before NEVEROP stays and may be lowered.
	svc.Message(wasp.UID, ns, "SET NEVEROP OFF")
	noticeTo(h, "Wasp")
	svc.Message(bee.UID, cs, "SOP #hive ADD Wasp")
	noticeTo(h, "Bee")
	svc.Message(wasp.UID, ns, "SET NEVEROP ON")
	noticeTo(h, "Wasp")
	if lvl, _ := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); lvl != 10 {
		t.Fatalf("Wasp's SOP entry is level %d", lvl)
	}
	svc.Message(bee.UID, cs, "AOP #hive ADD Wasp")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "NEVEROP") || !strings.Contains(got, "moved from SOP to AOP") {
		t.Errorf("lowering SOP to AOP: %q", got)
	}
	svc.Message(bee.UID, cs, "FLAGS #hive Wasp -t")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "NEVEROP") {
		t.Errorf("FLAGS on an op-level entry without raising it was refused: %q", got)
	}
	svc.Message(bee.UID, cs, "ACCESS #hive DEL Wasp")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "NEVEROP") || !strings.Contains(got, "Wasp") {
		t.Errorf("removing the entry: %q", got)
	}
	if lvl, _ := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); lvl != 0 {
		t.Fatalf("Wasp still has level %d after DEL", lvl)
	}
}

// CLONE doesn't copy a NEVEROP account's entry onto another channel.
func TestNeverOpClone(t *testing.T) {
	svc, h := newServices(t)
	ns, cs := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp")
	svc.Message(wasp.UID, ns, "REGISTER honeypot42")
	h.waitFor("Wasp", "registered")
	svc.Message(bee.UID, cs, "AOP #hive ADD Wasp")
	noticeTo(h, "Bee")
	svc.Message(wasp.UID, ns, "SET NEVEROP ON")
	noticeTo(h, "Wasp")
	if err := h.Emit(&state.Join{UID: bee.UID, Channel: "#comb", TS: 1}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	svc.Message(bee.UID, cs, "REGISTER #comb the comb")
	h.waitFor("Bee", "You are its Founder")
	svc.Message(bee.UID, cs, "CLONE #hive #comb ACCESS")
	noticeTo(h, "Bee")
	comb, _ := svc.store.ChannelByName("#comb")
	if lvl, _ := svc.store.ChanAccess(comb, svc.AccountID(wasp.UID)); lvl != 0 {
		t.Fatalf("CLONE gave Wasp level %d on #comb", lvl)
	}
}
