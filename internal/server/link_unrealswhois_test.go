package server

import "testing"

// UnrealIRCd's SWHOIS was dropped. Now the lines show in WHOIS (320), go on
// to the other links, can be taken away by tag, and our opers' own lines
// go to UnrealIRCd.
func TestUnrealSwhois(t *testing.T) {
	hub, hop, ann, _ := globopsTree(t)
	bob := hub.user("bob").UID

	hub.fromLinkTo(t, "a.test.local", ":0AA SWHOIS "+bob+" + spamfilter 5 :is on probation")
	hub.fromLinkTo(t, "a.test.local", ":0AA SWHOIS "+bob+" + vouch 1 :is vouched for")
	hop.send("WHOIS bob")
	hop.expect(" 320 hubop bob :is vouched for")
	hop.expect(" 320 hubop bob :is on probation")
	waitLink(t, "sent on to b", func() bool {
		return hub.logHas("link b.test.local -> :0AA SWHOIS " + bob + " + vouch 1 :is vouched for")
	})
	if hub.logHas("link a.test.local -> :0AA SWHOIS") {
		t.Errorf("SWHOIS sent back to where it came from")
	}

	// Taken away by tag.
	hub.fromLinkTo(t, "a.test.local", ":0AA SWHOIS "+bob+" - vouch 0 :*")
	hop.send("WHOIS bob")
	hop.expect(" 320 hubop bob :is on probation")
	hop.send("PING :after")
	hop.quiet("is vouched for")

	// A user who is no oper cannot set one.
	hub.fromLinkTo(t, "a.test.local", ":"+bob+" SWHOIS "+bob+" + me 0 :forged")
	hop.send("WHOIS bob")
	hop.quiet("forged")

	// Ours go to UnrealIRCd, replacing the old ones.
	hub.s.onLoop(func() {
		for c := range hub.s.opers {
			if c.nick == "hubop" {
				c.operSwhois = []string{"looks very helpful"}
			}
		}
		hub.s.resendOperLooks()
	})
	hu := hub.user("hubop").UID
	waitLink(t, "our swhois to a", func() bool {
		return hub.logHas("link a.test.local -> :0HB SWHOIS " + hu + " + beeoper 0 :looks very helpful")
	})
	_ = ann
}
