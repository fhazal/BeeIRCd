package server

import "testing"

// Our oper notices (oper-up, logout, failed OPER) never left this server,
// so opers on a linked UnrealIRCd did not see them. They now go to
// UnrealIRCd links as SENDSNO o; another BeeIRCd does not get them twice.
func TestOperNoticesReachUnreal(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	two := login(t, hub.s, "hubtwo")
	two.send("OPER nosuchkit171 x")
	waitLink(t, "failed OPER sent to b", func() bool {
		return hub.logHas("link b.test.local -> :0HB SENDSNO o :hubtwo (") && hub.logHas("nosuchkit171")
	})
	netadminOn(t, hub, two, "hubtwo")
	waitLink(t, "oper-up sent to a", func() bool {
		return hub.logHas("link a.test.local -> :0HB SENDSNO o :hubtwo (~hubtwo@127.0.0.1) [127.0.0.1] is now")
	})
	hop.send("MODE hubop -o")
	waitLink(t, "logout sent to a", func() bool {
		return hub.logHas("link a.test.local -> :0HB SENDSNO o :hubop (")
	})

	// A BeeIRCd peer is left out.
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.peer.Bee = true
		}
	})
	three := login(t, hub.s, "hubthree")
	netadminOn(t, hub, three, "hubthree")
	three.send("PING :done")
	three.expect(" :done")
	if hub.logHas("SENDSNO o :hubthree") {
		t.Errorf("oper notice sent to a BeeIRCd link")
	}
}
