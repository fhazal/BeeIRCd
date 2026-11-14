package server

import (
	"testing"
	"time"
)

// A leaf keeps the hub's service bots out of its people counts and lists,
// and knows them as services (+S over the link, as in UnrealIRCd).
func TestLinkServicesNotPeople(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, leaf.s, "bob")
	leaf.seen(t, "NickServ")
	hub.seen(t, "bob")
	var people int
	var ns, bob bool
	leaf.s.onLoop(func() {
		people, _, _ = leaf.s.net.PeopleCounts()
		if u := leaf.s.net.UserByNick("NickServ"); u != nil {
			ns = u.Service
		}
		if u := leaf.s.net.UserByNick("bob"); u != nil {
			bob = u.Service
		}
	})
	if people != 1 || !ns || bob {
		t.Errorf("leaf: %d people (want 1, only bob), NickServ a service %v, bob a service %v", people, ns, bob)
	}
	hub.s.onLoop(func() { people, _, _ = hub.s.net.PeopleCounts() })
	if people != 1 {
		t.Errorf("hub: %d people, want 1", people)
	}
}

// Both ends know the link's lag within seconds of the sync, not only after
// a quiet minute.
func TestLinkLagMeasuredAtOnce(t *testing.T) {
	hub, leaf := linkedPair(t)
	// one tick each (the 1 s ticker may not have come round yet)
	hub.s.onLoop(func() { hub.s.linkTick(time.Now()) })
	leaf.s.onLoop(func() { leaf.s.linkTick(time.Now()) })
	waitLink(t, "lag on both ends", func() bool {
		var a, b float64
		hub.s.onLoop(func() {
			for _, st := range hub.s.linkStatus() {
				a = st.LagMS
			}
		})
		leaf.s.onLoop(func() {
			for _, st := range leaf.s.linkStatus() {
				b = st.LagMS
			}
		})
		return a > 0 && b > 0
	})
}
