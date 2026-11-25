package services

import (
	"testing"

	"github.com/fhazal/beeircd/internal/state"
)

// CLONE copied PERSIST but left ChanServ out of (or in) the target, so
// the channel closed when the last person left; it joins or parts now as
// SET PERSIST does.
func TestClonePersist(t *testing.T) {
	svc, h := newServices(t)
	cs := h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	if err := h.Emit(&state.Join{UID: bee.UID, Channel: "#comb", TS: 1}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	svc.Message(bee.UID, cs, "REGISTER #comb the comb")
	h.waitFor("Bee", "You are its Founder")

	svc.Message(bee.UID, cs, "SET #hive PERSIST ON")
	svc.Message(bee.UID, cs, "CLONE #hive #comb")
	noticeTo(h, "Bee")
	if reg, _ := svc.store.ChannelByName("#comb"); !reg.Persist {
		t.Fatal("CLONE didn't copy PERSIST")
	}
	if h.net.Channel("#comb").Member(cs) == nil {
		t.Fatal("ChanServ isn't in #comb after CLONE brought PERSIST on")
	}
	if err := h.Emit(&state.Part{UID: bee.UID, Channel: "#comb"}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	if h.net.Channel("#comb") == nil {
		t.Fatal("#comb closed though PERSIST is on")
	}

	svc.Message(bee.UID, cs, "SET #hive PERSIST OFF")
	svc.Message(bee.UID, cs, "CLONE #hive #comb")
	noticeTo(h, "Bee")
	if ch := h.net.Channel("#comb"); ch != nil && ch.Member(cs) != nil {
		t.Error("ChanServ stayed in #comb after CLONE turned PERSIST off")
	}
}
