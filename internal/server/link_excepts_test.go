package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// A leaf (services off) uses the session exceptions of the services
// server: they reach it when the link syncs and when they change. Before,
// it held everyone to its own session_limit (seen on SKINTERNET).
func TestLeafUsesSessionExceptions(t *testing.T) {
	hub, leaf := linkedPair(t)
	leaf.s.onLoop(func() { leaf.s.cfg.Limits.SessionLimit = 2 })
	hub.s.onLoop(func() {
		if err := hub.s.store.SaveSessionExcept(store.SessionExcept{Mask: "127.0.0.1", Max: 10, Reason: "bouncer", SetBy: "test", SetAt: time.Now()}); err != nil {
			t.Error(err)
		}
		hub.s.svc.ReloadBans()
	})
	waitLink(t, "the exception on the leaf", func() bool {
		n := 0
		leaf.s.onLoop(func() { n = len(leaf.s.svc.LinkedExcepts()) })
		return n == 1
	})
	for _, n := range []string{"bnc1", "bnc2", "bnc3", "bnc4"} {
		login(t, leaf.s, n) // over the leaf's own limit of 2
	}

	// Taken off on the hub: off on the leaf too, and the limit is back.
	hub.s.onLoop(func() {
		hub.s.store.DelSessionExcept("127.0.0.1")
		hub.s.svc.ReloadBans()
	})
	waitLink(t, "the exception gone on the leaf", func() bool {
		n := 1
		leaf.s.onLoop(func() { n = len(leaf.s.svc.LinkedExcepts()) })
		return n == 0
	})
	// Refused as it connects, before it says anything (sending first could
	// lose the reply to a reset).
	c := raw(t, leaf.s)
	c.expect("Too many connections from your IP (the limit is 2)")
}
