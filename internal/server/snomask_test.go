package server

import "testing"

func TestApplySnomask(t *testing.T) {
	for _, tt := range []struct{ cur, change, want string }{
		{"", "+cq-k", "cq"},
		{"", "cqk", "ckq"},
		{snomaskDefault, "+nN-k", "acdflnoqrxKLNOX"},
		{snomaskDefault, "-" + snomaskDefault, ""},
		{"ck", "+zZ!?Cq", "ckqC"}, // unknown letters are left out
		{"Kk", "", "kK"},          // lowercase first
		{"ckq", "-c+t", "kqt"},
	} {
		if got := applySnomask(tt.cur, tt.change); got != tt.want {
			t.Errorf("applySnomask(%q, %q) = %q, want %q", tt.cur, tt.change, got, tt.want)
		}
	}
}

// snomaskOf reads c's snomask on the server loop.
func snomaskOf(s *Server, nick string) (mask string, plusS bool) {
	s.do(func() {
		for _, c := range s.clients {
			if c.user != nil && c.user.Nick == nick {
				mask, plusS = c.snomask, c.user.Modes['s']
			}
		}
	})
	return
}

func TestSnomaskMode(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := registered(t, s, "Boss")
	b.send("OPER Boss " + operPW)
	b.expect(":irc.test.local MODE Boss +os")
	b.expect(" 381 Boss :You are now an IRC operator")
	b.expect(" 008 Boss +" + snomaskDefault + " :Server notice mask")
	ember := registered(t, s, "ember")

	for _, tt := range []struct {
		name, mode string
		echo       string // the MODE echo, "" = none expected
		want       string // the snomask after, "" = none (and no +s)
	}{
		{"add and take away", "+s +nN-k", "", "acdflnoqrxKLNOX"},
		{"letters without a sign add", "+s cqkX", "", "acdfklnoqrxKLNOX"},
		{"-s clears", "-s", ":Boss MODE Boss -s", ""},
		{"+s alone gives the default", "+s", ":Boss MODE Boss +s", snomaskDefault},
		{"+s again keeps the mask", "+s", "", snomaskDefault},
		{"param after other modes", "+ws -" + snomaskDefault + "+c", ":Boss MODE Boss +w", "c"},
		{"an empty mask is -s", "+s -c", ":Boss MODE Boss -s", ""},
		{"+s with letters from nothing", "+s +t", ":Boss MODE Boss +s", "t"},
	} {
		b.send("MODE Boss " + tt.mode)
		if tt.echo != "" {
			b.expect(tt.echo)
		}
		if tt.want != "" {
			b.expect(" 008 Boss +" + tt.want + " :Server notice mask")
		}
		b.sync()
		mask, on := snomaskOf(s, "Boss")
		if mask != tt.want || on != (tt.want != "") {
			t.Errorf("%s: MODE Boss %s: snomask %q, +s %v; want %q", tt.name, tt.mode, mask, on, tt.want)
		}
	}

	// Not an oper: the letters are taken off the line and nothing changes.
	ember.send("MODE ember +s +c")
	ember.quiet(" 008 ")
	ember.send("MODE ember")
	ember.expect(" 221 ember +i")
	if mask, on := snomaskOf(s, "ember"); mask != "" || on {
		t.Errorf("non-oper: snomask %q, +s %v", mask, on)
	}

	// Dropping oper clears the mask; /oper gives the default again.
	b.send("MODE Boss +s +x")
	b.expect(" 008 Boss +tx :Server notice mask")
	b.send("MODE Boss -o")
	b.expect(":Boss MODE Boss -os")
	b.sync()
	if mask, on := snomaskOf(s, "Boss"); mask != "" || on {
		t.Errorf("after -o: snomask %q, +s %v", mask, on)
	}
	b.send("OPER Boss " + operPW)
	b.expect(" 008 Boss +" + snomaskDefault + " :Server notice mask")
}

func TestSnoticeOpersByLetter(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := registered(t, s, "Boss")
	b.send("OPER Boss " + operPW)
	b.expect(" 008 Boss +" + snomaskDefault + " :Server notice mask")
	ember := registered(t, s, "ember")
	b.send("AS OPER ember ADD FLAGS gk HOST *@127.0.0.1")
	b.expect("ember is an oper now")
	b.send("AS OPER ember SET PASSWORD Ember-oper-1")
	b.expect("ember has a new oper password.")
	ember.send("OPER ember Ember-oper-1")
	ember.expect(" 008 ember +" + snomaskDefault + " :Server notice mask")

	ember.send("MODE ember +s +t-c")
	ember.expect(" 008 ember +adfkloqrtxKLOX :Server notice mask")
	ember.sync()

	s.do(func() { s.snoticeOpers('t', "letter t") })
	ember.expect("*** Notice -- letter t")
	b.quiet("letter t")

	// Connects (c) reach Boss only.
	login(t, s, "dave")
	b.expect("Notice -- Connect: dave")
	ember.quiet("Connect: dave")

	// Both have o.
	s.do(func() { s.snoticeOpers('o', "letter o") })
	b.expect("*** Notice -- letter o")
	ember.expect("*** Notice -- letter o")

	// Nobody has v; -s gets nothing at all.
	ember.send("MODE ember -s")
	ember.expect(":ember MODE ember -s")
	s.do(func() { s.snoticeOpers('v', "letter v") })
	b.quiet("letter v")
	s.do(func() { s.snoticeOpers('o', "letter o again") })
	b.expect("letter o again")
	ember.quiet("letter o again")
}
