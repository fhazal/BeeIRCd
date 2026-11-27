package server

import "testing"

// UnrealIRCd names the answering server by name, not SID, on the numerics
// and notices it sends back for a routed query (":sand.unreal.local 391
// nick ..."). They showed to our user as from our own server; they now keep
// the far server's name, as on a real UnrealIRCd network.
func TestRemoteReplyKeepsServerName(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	hub.fromLinkTo(t, "a.test.local", ":a.test.local 391 hubop a.test.local :Friday kit171")
	hop.expect(":a.test.local 391 hubop a.test.local :Friday kit171")
	hub.fromLinkTo(t, "a.test.local", ":0AA 391 hubop a.test.local :Friday by sid")
	hop.expect(":a.test.local 391 hubop a.test.local :Friday by sid")
	hub.fromLinkTo(t, "a.test.local", ":a.test.local NOTICE hubop :*** Connect: kit171 notice")
	hop.expect(":a.test.local NOTICE hubop :*** Connect: kit171 notice")
}
