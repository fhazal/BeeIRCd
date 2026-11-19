package server

import "testing"

// Every notice letter has InspIRCd 4's category word; anything else is an
// ANNOUNCEMENT.
func TestSnoCategory(t *testing.T) {
	for l, want := range map[byte]string{
		'c': "CONNECT", 'C': "REMOTECONNECT", 'q': "QUIT", 'Q': "REMOTEQUIT",
		'o': "OPER", 'O': "OPER", 'x': "XLINE", 'X': "XLINE", 'k': "KILL", 'K': "KILL",
		'r': "REHASH", 't': "STATS", 'l': "LINK", 'L': "REMOTELINK", 'f': "FILTER",
		'd': "DNSBL", 'n': "NICK", 'N': "REMOTENICK", 'g': "GLOBOPS", 'G': "GLOBOPS",
		'a': "ANNOUNCEMENT", 'v': "OVERRIDE", 'z': "ANNOUNCEMENT",
	} {
		if got := snoCategory(l); got != want {
			t.Errorf("snoCategory(%q) = %q, want %q", l, got, want)
		}
	}
}

// +* adds every letter and -* takes them all away.
func TestApplySnomaskStar(t *testing.T) {
	for _, tt := range []struct{ cur, change, want string }{
		{"", "+*", "acdfgklnoqrtvxCGKLNOQX"},
		{"", "*", "acdfgklnoqrtvxCGKLNOQX"},
		{snomaskDefault, "-*", ""},
		{"", "+*-k", "acdfglnoqrtvxCGKLNOQX"},
	} {
		if got := applySnomask(tt.cur, tt.change); got != tt.want {
			t.Errorf("applySnomask(%q, %q) = %q, want %q", tt.cur, tt.change, got, tt.want)
		}
	}
}

// /mode nick +s +* sets every letter, and somebody who isn't an oper can't
// set +s at all.
func TestSnomaskStarAndNonOperPlusS(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("MODE Boss +s +*")
	b.expect(" 008 Boss +acdfgklnoqrtvxCGKLNOQX :Server notice mask")

	plain := login(t, s, "plain")
	plain.send("MODE plain +s")
	plain.expect(" 481 plain :Permission Denied - Only operators may set user mode s")
}

// The oper chat letters are g and G, worded GLOBOPS.
func TestGlobopsLetters(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("MODE Boss +s +g")
	b.sync()
	s.do(func() { s.snoticeOpers('g', "From ab: text") })
	b.expect("*** GLOBOPS: From ab: text")
}
