package server

import (
	"strings"
	"testing"
)

// The ERROR a peer sends when it closes a link loses its "Closing Link:
// name (" wrapper, so the split notice reads "Server X split: Server
// updating" as InspIRCd's does.
func TestClosingReason(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"Closing Link: hub.test.local (Server updating)", "Server updating"},
		{"closing link: 10.0.0.1 (Ping timeout: 120 seconds)", "Ping timeout: 120 seconds"},
		{"Closing Link: hub.test.local (Killed (Boss (bye)))", "Killed (Boss (bye))"},
		{"Bad password", "Bad password"},
		{"  ", "closed the link"},
		{"", "closed the link"},
	} {
		if got := closingReason(tt.in); got != tt.want {
			t.Errorf("closingReason(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Connects, nick changes, STATS and quits here read like InspIRCd 4's.
func TestLocalSnoticeTexts(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("MODE Boss +s +cqnt")
	b.sync()

	dave := login(t, s, "dave")
	l := b.expect("): dave (~dave@127.0.0.1) [127.0.0.1] [")
	if !strings.Contains(l, "*** CONNECT: Client connecting on port ") || !strings.Contains(l, " (class ") || !strings.HasSuffix(l, "\x0F]") {
		t.Errorf("connect notice %q", l)
	}

	dave.send("NICK davey")
	dave.expect(" NICK davey")
	b.expect("*** NICK: User dave changed their nickname to davey")

	dave.send("STATS u")
	dave.expect(" 219 davey u ")
	b.expect("*** STATS: Stats 'u' requested by davey (~dave@127.0.0.1)")

	dave.send("QUIT :bye")
	b.expect("*** QUIT: Client exiting: davey (~dave@127.0.0.1) [127.0.0.1] [Quit: bye]")
}

// Users on other servers: C, N, Q and O once the link is synced.
func TestRemoteSnoticeTexts(t *testing.T) {
	hub, leaf := linkedPair(t)
	w := login(t, hub.s, "watch")
	w.send("NS REGISTER " + pw)
	w.expect("is now registered to you.")
	operEntryOn(t, hub, "watch", "gk", []string{"*@*"})
	w.send("OPER watch " + operPW)
	w.expect(" 381 watch :You are now an IRC operator")
	w.send("MODE watch +s +CNQO")
	w.expect(" 008 watch +acdfkloqrxCKLNOQX ")

	carl := login(t, leaf.s, "carl")
	l := w.expect("*** REMOTECONNECT: Client connecting at leaf.test.local: carl (~carl@127.0.0.1) [127.0.0.1] [")
	if !strings.HasSuffix(l, "\x0F]") {
		t.Errorf("remote connect notice %q", l)
	}

	carl.send("NICK carly")
	carl.expect(" NICK carly")
	w.expect("*** REMOTENICK: User carl changed their nickname to carly")

	carl.send("NS REGISTER " + pw)
	carl.expect("is now registered to you.")
	operEntryOn(t, hub, "carly", "gk", []string{"*@*"})
	carl.send("OPER carly " + operPW)
	carl.expect(" 381 carly :You are now an IRC operator")
	w.expect("*** OPER: From leaf.test.local: carly (~carl@127.0.0.1) [127.0.0.1] is now a server operator of type gk")

	carl.send("QUIT :bye")
	w.expect("*** REMOTEQUIT: Client exiting on server leaf.test.local: carly (~carl@127.0.0.1) [127.0.0.1] [Quit: bye]")
}
