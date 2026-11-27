package server

import "testing"

// An oper on a linked UnrealIRCd or InspIRCd server sends only +o and
// their class (MD operclass, InspIRCd's OPERTYPE made into one), no
// BeeIRCd look: WHOIS here showed them as a plain IRC Operator. Their
// class's rank shows now, and the other user modes they have stay.
func TestRemoteOperRankFromClass(t *testing.T) {
	hub, _, _, _ := globopsTree(t)
	uid := hub.user("bob").UID
	hub.fromLinkTo(t, "a.test.local", ":"+uid+" UMODE2 +oBW")
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+" operclass :netadmin")
	waitLink(t, "bob opered at the hub", func() bool {
		u := hub.user("bob")
		return u != nil && u.Modes['o'] && u.OperClass == "netadmin"
	})
	if u := hub.user("bob"); !u.Modes['B'] {
		t.Errorf("bob's other modes were dropped: %v", u.Modes)
	}
	c := login(t, hub.s, "watcher")
	c.send("WHOIS bob")
	c.expect(" 313 watcher bob :is a Network Administrator")

	// InspIRCd's type name: NetAdmin.
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+" operclass :NetAdmin")
	waitLink(t, "class NetAdmin", func() bool { return hub.user("bob").OperClass == "NetAdmin" })
	c.send("WHOIS bob")
	c.expect(" 313 watcher bob :is a Network Administrator")

	// A class this server doesn't have: its name shows with the rank.
	hub.fromLinkTo(t, "a.test.local", ":0AA MD client "+uid+" operclass :Chief Bee")
	waitLink(t, "class Chief Bee", func() bool { return hub.user("bob").OperClass == "Chief Bee" })
	c.send("WHOIS bob")
	c.expect(" 313 watcher bob :is an IRC Operator (Chief Bee)")
}
