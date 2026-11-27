package link

import "testing"

// A failed OPER here goes to an InspIRCd link as SENDSNO o; InspIRCd shows a
// remote server's failed OPERs under O, so the bridge sends SNONOTICE O. It
// was dropped. Other letters, and lines from the InspIRCd side, stay out.
func TestInspBridgeOperFailSno(t *testing.T) {
	b := testBridge(t)
	if got := b.Out(":0BE SENDSNO o :beeF (u@h) [ip] failed to log into the \x02x\x02 oper account because they specified the wrong password."); len(got) == 0 ||
		got[len(got)-1] != ":0BE SNONOTICE O :beeF (u@h) [ip] failed to log into the \x02x\x02 oper account because they specified the wrong password." {
		t.Errorf("SENDSNO o out: %q", got)
	}
	for _, l := range []string{":0BE SENDSNO f :a filter hit", ":1IN SENDSNO o :from them", ":0BEAAAAAA SENDSNO o :from a user"} {
		if got := b.Out(l); len(got) != 0 {
			t.Errorf("%q went out as %q", l, got)
		}
	}
}
