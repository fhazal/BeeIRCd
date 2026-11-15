package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/state"
)

// When a registered channel loses a merge to an older copy from another
// server, its modes and ranks go; ChanServ then gives everyone the ranks
// they earn again and puts the mode lock back. Before, the founder stayed
// without op (SKINTERNET after the hub restarted).
func TestChanServAfterOlderChannel(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	alice.send("NS REGISTER " + pw)
	alice.expect("is now registered to you.")
	alice.send("JOIN #reg")
	alice.expect(" 366 alice #reg ")
	alice.send("CS REGISTER #reg bees")
	alice.expect("You are its Founder.")
	bob := login(t, leaf.s, "bob")
	bob.send("JOIN #reg")
	bob.expect(" 366 bob #reg ")
	hub.seen(t, "bob")

	hub.fromLink(t, ":"+leaf.sid+" SJOIN 1000000 #reg +s :"+leaf.user("bob").UID)
	alice.expect("MODE #reg -")
	alice.expect(":ChanServ!")
	aliceUID := hub.user("alice").UID
	waitLink(t, "alice opped again", func() bool {
		var op bool
		hub.s.onLoop(func() {
			if ch := hub.s.net.Channel("#reg"); ch != nil && ch.TS == 1000000 {
				m := ch.Members[aliceUID]
				op = m != nil && m.Has(state.RankOp) && ch.Modes['r']
			}
		})
		return op
	})
}
