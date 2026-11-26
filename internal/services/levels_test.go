package services

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// regNick connects nick and registers it (pass honeypot42).
func regNick(t *testing.T, svc *Services, h *fakeHost, uid, nick string) *state.User {
	t.Helper()
	u := helpUser(t, h, uid, nick)
	svc.Message(u.UID, h.net.UserByNick("NickServ"), "REGISTER honeypot42")
	h.waitFor(nick, "registered")
	return u
}

// A FLAGS entry ranks by what its letters need now: after LEVELS SET
// raised that, a lower op could still ACCESS DEL / ADD / AOP ADD it
// because the rank stored when it was set was used.
func TestFlagsRankAfterLevelsSet(t *testing.T) {
	svc, h := newServices(t)
	cs := h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	wasp := regNick(t, svc, h, "1LFAAAAAB", "Wasp")
	moth := regNick(t, svc, h, "1LFAAAAAC", "Moth")
	for _, line := range []string{"FLAGS #hive Wasp +t", "LEVELS #hive SET TOPIC 50", "ACCESS #hive ADD Moth 20"} {
		svc.Message(bee.UID, cs, line)
	}
	noticeTo(h, "Bee")
	reg, _ := svc.store.ChannelByName("#hive")
	if r := svc.accountLevel(reg, svc.AccountID(wasp.UID)).level; r != 50 {
		t.Fatalf("Wasp ranks %d, want 50", r)
	}
	for _, line := range []string{"FLAGS #hive Wasp -t", "VOP #hive ADD Wasp", "ACCESS #hive ADD Wasp 10", "ACCESS #hive DEL Wasp"} {
		svc.Message(moth.UID, cs, line)
		if got := noticeTo(h, "Moth"); !strings.Contains(got, "can't change Wasp") {
			t.Errorf("Moth (20) %s on Wasp (50): %q", line, got)
		}
	}
	if _, flags := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); flags != "t" {
		t.Errorf("Wasp's entry changed to %q", flags)
	}
}

// AOP ADD for someone whose FLAGS entry ranks as AOP said "already on
// the AOP list", yet that list leaves FLAGS entries out.
func TestXopAddFlagsEntry(t *testing.T) {
	svc, h := newServices(t)
	cs := h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	wasp := regNick(t, svc, h, "1LFAAAAAB", "Wasp")
	svc.Message(bee.UID, cs, "FLAGS #hive Wasp +o")
	noticeTo(h, "Bee")
	reg, _ := svc.store.ChannelByName("#hive")
	if lvl, flags := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); flags == "" || lvl != store.LevelAOP {
		t.Fatalf("setup: Wasp level %d flags %q", lvl, flags)
	}
	svc.Message(bee.UID, cs, "AOP #hive ADD Wasp")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Wasp added to the AOP list of #hive.") {
		t.Errorf("AOP ADD: %q", got)
	}
	if _, flags := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); flags != "" {
		t.Errorf("Wasp is still a FLAGS entry (%q)", flags)
	}
	svc.Message(bee.UID, cs, "AOP #hive LIST")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Wasp") {
		t.Errorf("AOP LIST misses Wasp: %q", got)
	}
	svc.Message(bee.UID, cs, "AOP #hive ADD Wasp")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Wasp is on the AOP list of #hive already.") {
		t.Errorf("second AOP ADD: %q", got)
	}
}

// ACCESS ADD at the level a FLAGS entry was stored at said "at level AOP
// already" and left the +o entry as it was, as AOP ADD did before.
func TestAccessAddFlagsEntry(t *testing.T) {
	svc, h := newServices(t)
	cs := h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	wasp := regNick(t, svc, h, "1LFAAAAAB", "Wasp")
	svc.Message(bee.UID, cs, "FLAGS #hive Wasp +o")
	noticeTo(h, "Bee")
	reg, _ := svc.store.ChannelByName("#hive")
	if lvl, flags := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); flags == "" || lvl != store.LevelAOP {
		t.Fatalf("setup: Wasp level %d flags %q", lvl, flags)
	}
	svc.Message(bee.UID, cs, "ACCESS #hive ADD Wasp AOP")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Wasp moved from level +o to AOP on #hive.") {
		t.Errorf("ACCESS ADD: %q", got)
	}
	if lvl, flags := svc.store.ChanAccess(reg, svc.AccountID(wasp.UID)); flags != "" || lvl != store.LevelAOP {
		t.Errorf("Wasp: level %d flags %q, want a plain AOP entry", lvl, flags)
	}
	svc.Message(bee.UID, cs, "ACCESS #hive ADD Wasp AOP")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Wasp is at level AOP on #hive already.") {
		t.Errorf("second ACCESS ADD: %q", got)
	}
}
