package server

import "testing"

// UnrealIRCd's SENDSNO and SLOG were dropped. SENDSNO shows to opers whose
// mask has a matching letter; SLOG shows by its subsystem. Both go on to
// the other links, and a line from a user is ignored.
func TestUnrealSendSnoAndSlog(t *testing.T) {
	hub, hop, ann, _ := globopsTree(t)
	hop.send("MODE hubop +s +CL")
	hop.send("PING :umode")
	hop.expect(" :umode")

	hub.fromLinkTo(t, "a.test.local", ":0AA SENDSNO F :far connect notice")
	hop.expect("*** REMOTECONNECT: far connect notice")
	waitLink(t, "SENDSNO sent on to b", func() bool {
		return hub.logHas("link b.test.local -> :0AA SENDSNO F :far connect notice")
	})
	if hub.logHas("link a.test.local -> :0AA SENDSNO") {
		t.Errorf("SENDSNO sent back to where it came from")
	}

	hub.fromLinkTo(t, "a.test.local", ":0AA SENDSNO n :nick notice")
	hop.quiet("nick notice") // hubop has no n

	hub.fromLinkTo(t, "a.test.local", "@unrealircd.org/json-log={} :0AA SLOG info link SERVER_SYNCED :Link x is now synced")
	hop.expect("*** REMOTELINK: Link x is now synced")
	waitLink(t, "SLOG sent on to b", func() bool {
		return hub.logHas("link b.test.local -> @unrealircd.org/json-log={} :0AA SLOG info link SERVER_SYNCED :Link x is now synced")
	})

	hub.fromLinkTo(t, "a.test.local", ":"+ann+" SENDSNO c :from a user")
	hub.fromLinkTo(t, "a.test.local", ":"+ann+" SLOG info link X :from a user")
	hop.quiet("from a user")
	if hub.logHas("from a user") && hub.logHas("-> :"+ann+" SENDSNO") {
		t.Errorf("a user's SENDSNO was passed on")
	}
}

// UnrealIRCd logs a spamfilter hit under its tkl subsystem. It showed as a
// ban (XLINE) notice; it is now a FILTER one, and a real ban stays XLINE.
func TestUnrealSlogSpamfilterIsFilter(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	hop.send("MODE hubop +s +fX")
	hop.send("PING :umode")
	hop.expect(" :umode")
	hub.fromLinkTo(t, "a.test.local", "@unrealircd.org/json-log={} :0AA SLOG info tkl SPAMFILTER_MATCH :[Spamfilter] kit hit")
	hop.expect("*** FILTER: [Spamfilter] kit hit")
	hub.fromLinkTo(t, "a.test.local", "@unrealircd.org/json-log={} :0AA SLOG info tkl TKL_ADD :G-Line added kit")
	hop.expect("*** XLINE: G-Line added kit")
}
