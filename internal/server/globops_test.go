package server

import (
	"strings"
	"testing"
)

// GLOBOPS is for opers only, and shows with the g snomask letter.
func TestGlobopsLocal(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("MODE Boss +s +g")
	b.sync()
	b.send("GLOBOPS maintenance at noon")
	b.expect("*** GLOBOPS: From Boss: maintenance at noon")

	u := login(t, s, "alice")
	u.send("GLOBOPS nope")
	u.expect(" 481 alice ")
}

// A GLOBOPS crosses a bee link both ways, and shows as the letter G
// notice on the other side: ":<uid> GLOBOPS :<text>" on the wire.
func TestGlobopsOverLink(t *testing.T) {
	hub, leaf := linkedPair(t)
	hop := login(t, hub.s, "hubop")
	netadminOn(t, hub, hop, "hubop")
	lop := login(t, leaf.s, "leafop")
	netadminOn(t, leaf, lop, "leafop")
	for c, nick := range map[*tc]string{hop: "hubop", lop: "leafop"} {
		c.send("MODE " + nick + " +s +gG")
		c.send("PING :umode")
		c.expect(" :umode")
	}
	hub.seen(t, "leafop")
	leaf.seen(t, "hubop")

	lop.send("GLOBOPS hello hub")
	lop.expect("*** GLOBOPS: From leafop: hello hub")
	hop.expect("*** GLOBOPS: From leafop: hello hub")
	hop.send("GLOBOPS hello leaf")
	hop.expect("*** GLOBOPS: From hubop: hello leaf")
	lop.expect("*** GLOBOPS: From hubop: hello leaf")

	// A user without +o on the other end is not believed.
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	bob := hub.user("bob")
	hub.fromLink(t, ":"+bob.UID+" GLOBOPS :sneaky")
	hub.fromLink(t, ":"+leaf.sid+" GLOBOPS :from a server")
	hop.expect("*** GLOBOPS: from a server")
	if strings.Contains(hub.log.String(), "sneaky") {
		t.Errorf("a non-oper GLOBOPS was shown")
	}
}
