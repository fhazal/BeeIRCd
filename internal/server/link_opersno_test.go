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

// An UnrealIRCd server sends its own oper-up notice (SLOG); ours from the
// oper's class came as well, so opers here saw it twice. Only the far one
// shows now; a BeeIRCd peer still gets ours (TestRemoteSnoticeTexts).
func TestUnrealOperUpShowsOnce(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	hop.send("MODE hubop +s +O")
	hop.send("PING :umode")
	hop.expect(" :umode")
	var uid string
	hub.s.onLoop(func() { uid = hub.s.net.UserByNick("bob").UID })
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+" operclass :netadmin")
	waitLink(t, "bob opered at the hub", func() bool {
		var c string
		hub.s.onLoop(func() { c = hub.s.net.UserByNick("bob").OperClass })
		return c == "netadmin"
	})
	hop.quiet("From a.test.local: bob")
}

// A failed OPER here was not sent to a linked InspIRCd server, whose opers
// see the other servers' ones under O; it now goes as SENDSNO o (the bridge
// makes it SNONOTICE O). An oper-up is left out: InspIRCd makes its own
// from the oper's type.
func TestOperFailReachesInsp(t *testing.T) {
	hub, _ := linkedPair(t)
	rec := bridged(t, hub)
	alice := login(t, hub.s, "alice")
	alice.send("OPER nosuchkit171 x")
	waitLink(t, "failed OPER sent to the InspIRCd link", func() bool {
		return rec.sent("SENDSNO o :alice (") && rec.sent("nosuchkit171")
	})
	netadminOn(t, hub, alice, "alice")
	alice.send("PING :done")
	alice.expect(" :done")
	if rec.sent("is now a server operator") {
		t.Errorf("oper-up sent to the InspIRCd link")
	}
}
