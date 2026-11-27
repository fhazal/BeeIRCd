package server

import "testing"

// UnrealIRCd sends an oper's SDESC as ":nick SDESC :text" (sdesc.c). It was
// dropped (only a UID was looked up); it now changes that server's
// description here and goes on with the oper's UID.
func TestUnrealSdescByNick(t *testing.T) {
	hub, _, ann, _ := globopsTree(t)
	hub.fromLinkTo(t, "a.test.local", ":ann SDESC :Leaf by nick")
	waitLink(t, "description kept", func() bool {
		var d string
		hub.s.onLoop(func() {
			if r := hub.s.links.servers["0AA"]; r != nil {
				d = r.desc
			}
		})
		return d == "Leaf by nick"
	})
	waitLink(t, "SDESC sent on as the UID", func() bool {
		return hub.logHas("link b.test.local -> :" + ann + " SDESC :Leaf by nick")
	})
}
