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

// The oper-up notice names the oper type like InspIRCd 4: the rank in
// bold, the account when known, and a full stop. UnrealIRCd's class
// names map onto ours.
func TestSnoOperUp(t *testing.T) {
	who := "ann (~ann@h) [1.2.3.4]"
	B := string(rune(2))
	if got, want := snoOperUp(who, "Network Administrator", "ann"),
		who+" is now a server operator of type "+B+"Network Administrator"+B+" (using account "+B+"ann"+B+")."; got != want {
		t.Errorf("local: %q, want %q", got, want)
	}
	if got, want := snoOperUp(who, "IRC Operator", ""),
		who+" is now a server operator of type "+B+"IRC Operator"+B+"."; got != want {
		t.Errorf("remote: %q, want %q", got, want)
	}
	s, _, _ := fileServer(t, "")
	for _, tt := range []struct{ class, title, want string }{
		{"netadmin-with-override", "", "Network Administrator"},
		{"locop", "", "Network Helper"},
		{"netadmin", "is the Boss", "Boss"},
		{"", "", "IRC Operator"},
	} {
		if got := s.operRank(tt.class, tt.title); got != tt.want {
			t.Errorf("operRank(%q, %q) = %q, want %q", tt.class, tt.title, got, tt.want)
		}
	}
}
