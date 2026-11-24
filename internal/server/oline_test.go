package server

import (
	"strings"
	"testing"
)

// OperServ OLINE's host side: session-only flags, a flags change for an
// oper, and taking oper away; nobody else gets touched.
func TestSessionOper(t *testing.T) {
	s := testServer(t, nil)
	bee := login(t, s, "bee")
	bee.sync()
	run := func(flags string) (ok bool, now string) {
		s.onLoop(func() {
			uid := s.net.UserByNick("bee").UID
			ok = host{s}.SessionOper(uid, flags)
			now = s.clients[uid].operFlags
		})
		return
	}
	if ok, now := run("sk"); !ok || now != "ks" {
		t.Fatalf("SessionOper sk: %v %q", ok, now)
	}
	bee.expect(" 381 bee ")
	if ok, now := run("g"); !ok || now != "g" {
		t.Fatalf("SessionOper g: %v %q", ok, now)
	}
	if l := bee.expect("Your oper flags are now"); !strings.Contains(l, " g ") {
		t.Errorf("flags notice %q", l)
	}
	if ok, now := run(""); !ok || now != "" {
		t.Fatalf("SessionOper '': %v %q", ok, now)
	}
	bee.expect("MODE bee -o")
	var ok bool
	s.onLoop(func() { ok = host{s}.SessionOper("0XXNOBODY", "k") })
	if ok {
		t.Error("SessionOper on someone not here said ok")
	}
}
