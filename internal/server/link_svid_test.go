package server

import (
	"testing"
)

// Anope (unreal4 protocol) logs a user in with SVS2MODE <uid> +d <account>
// and out with +d 0. That +d is the services account, not deaf: the user
// must keep getting channel messages and get the account.
func TestSVSModeAccountLogin(t *testing.T) {
	hub, leaf := linkedPair(t)
	eve := login(t, leaf.s, "eve")
	amy := login(t, leaf.s, "amy")
	eve.send("JOIN #full")
	eve.expect(" 366 eve #full ")
	amy.send("JOIN #full")
	amy.expect(" 366 amy #full ")
	hub.seen(t, "eve")
	uid := leaf.user("eve").UID

	leaf.fromLink(t, ":"+hub.sid+" SVS2MODE "+uid+" +d eveacct")
	waitLink(t, "eve's account", func() bool { u := leaf.user("eve"); return u != nil && u.Account == "eveacct" })
	if leaf.user("eve").Modes['d'] {
		t.Fatal("SVS2MODE +d <account> made eve deaf")
	}
	amy.send("PRIVMSG #full :still hear me")
	eve.expect("still hear me")

	leaf.fromLink(t, ":"+hub.sid+" SVSMODE "+uid+" +id 0")
	waitLink(t, "eve logged out", func() bool { u := leaf.user("eve"); return u != nil && u.Account == "" })
	if u := leaf.user("eve"); u.Modes['d'] || !u.Modes['i'] {
		t.Fatalf("SVSMODE +id 0: modes %v, want +i and not deaf", u.Modes)
	}
	amy.send("PRIVMSG #full :and now")
	eve.expect("and now")
}
