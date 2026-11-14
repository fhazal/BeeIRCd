package server

import (
	"bufio"
	"crypto/tls"
	"strings"
	"testing"
)

// User modes, KNOCK, SILENCE, SETNAME, CHG*/SET* and SA*.

// tlsLogin connects over TLS and registers as nick.
func tlsLogin(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	conn, err := tls.Dial("tcp", s.Addrs()[1].String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &tc{t: t, conn: conn, r: bufio.NewReader(conn), nick: nick}
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick + " Real")
	c.expect(":" + nick + " MODE " + nick + " +iz")
	return c
}

func TestUserModesAnyone(t *testing.T) {
	s := testServer(t, nil)
	a, b := login(t, s, "alice"), login(t, s, "bob")
	a.send("VERSION")
	a.expect("BOT=B")
	a.expect("SILENCE=32")

	// +B: WHOIS 335 and B in WHO.
	b.send("MODE bob +B")
	b.expect("MODE bob +B")
	a.send("WHOIS bob")
	a.expectNumeric("335", "bob", ":is a Bot on")
	a.send("WHO bob")
	if l := a.expectNumeric("352"); !strings.Contains(l, " HB ") {
		t.Errorf("WHO flags miss B: %s", l)
	}

	// Messages from a bot carry the IRCv3 bot tag.
	tagged := capLogin(t, s, "tina", "message-tags")
	b.send("PRIVMSG tina :beep")
	if l := tagged.expect("PRIVMSG tina :beep"); !strings.Contains(strings.SplitN(l, " ", 2)[0], "bot") {
		t.Errorf("no bot tag: %s", l)
	}

	// +d: no channel messages, PMs still come.
	joinAll("#d", a, b)
	b.send("MODE bob +d")
	b.expect("MODE bob +d")
	a.send("PRIVMSG #d :hello deaf")
	a.send("PRIVMSG bob :private still works")
	b.expect("private still works")
	b.quiet("hello deaf")

	// +D: no PMs (531), opers excepted (tested below).
	b.send("MODE bob +D")
	b.expect("MODE bob +D")
	a.send("PRIVMSG bob :hi")
	a.expectNumeric("531", "bob", ":User does not accept private messages")
	b.send("MODE bob -D")
	b.expect("MODE bob -D")

	// +R: only identified people.
	b.send("MODE bob +R")
	b.expect("MODE bob +R")
	a.send("PRIVMSG bob :hi")
	a.expectNumeric("531", "bob", ":You must identify to a registered nick")
	b.send("MODE bob -R")
	b.expect("MODE bob -R")

	// +T: no CTCPs, ACTION still fine.
	b.send("MODE bob +T")
	b.expect("MODE bob +T")
	a.send("PRIVMSG bob :\x01VERSION\x01")
	a.expectNumeric("531", "bob", ":User does not accept CTCPs")
	a.send("PRIVMSG bob :\x01ACTION waves\x01")
	b.expect("ACTION waves")

	// +p: channels hidden in WHOIS from others.
	b.send("MODE bob +p")
	b.expect("MODE bob +p")
	a.send("WHOIS bob")
	a.expectNumeric("312", "bob")
	a.quiet(" 319 ")

	// Users can't set oper or server modes.
	a.send("MODE alice +HWqz")
	a.send("MODE alice")
	if l := a.expectNumeric("221"); strings.ContainsAny(l[strings.LastIndex(l, " "):], "HWqz") {
		t.Errorf("non-oper got oper modes: %s", l)
	}
}

func TestUserModesTLS(t *testing.T) {
	s := testServer(t, nil)
	sec, plain := tlsLogin(t, s, "sec"), login(t, s, "plain")
	sec.send("MODE sec -z")
	sec.send("MODE sec")
	sec.expectNumeric("221", "+iz")
	// +Z: PMs only with TLS users, both ways.
	sec.send("MODE sec +Z")
	sec.expect("MODE sec +Z")
	plain.send("PRIVMSG sec :hi")
	plain.expectNumeric("531", "sec", ":You must be connected via TLS")
	sec.send("PRIVMSG plain :hi")
	sec.expectNumeric("531", "plain", ":Recipient is not connected via TLS and you are +Z")
}

func TestOperUserModes(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := login(t, s, "ember")
	b.send("MODE Boss +HWq")
	b.expect("MODE Boss +HWq")
	// +H: non-opers don't see 313 or the * in WHO.
	e.send("WHOIS Boss")
	e.expectNumeric("312", "Boss")
	e.quiet(" 313 ")
	e.send("WHO Boss")
	if l := e.expectNumeric("352"); strings.Contains(l, " H* ") {
		t.Errorf("+H oper shows * in WHO: %s", l)
	}
	// +W: Boss sees the WHOIS.
	b.expect("*** ember (~ember@127.0.0.1) did a /whois on you.")
	// +q: can't be kicked.
	joinAll("#q", e, b)
	e.send("MODE #q +o Boss") // ember created #q, so is op
	e.send("KICK #q Boss :out")
	e.expectNumeric("972", "KICK", ":user is unkickable (user mode +q)")
	// Opers get past +D.
	e.send("MODE ember +D")
	e.expect("MODE ember +D")
	b.send("PRIVMSG ember :oper here")
	e.expect("oper here")
	// De-opering drops H W q.
	b.send("MODE Boss -o")
	if l := b.expect("MODE Boss -"); !strings.Contains(l, "H") || !strings.Contains(l, "W") || !strings.Contains(l, "q") {
		t.Errorf("-o kept oper-only modes: %s", l)
	}
}

func TestSilence(t *testing.T) {
	s := testServer(t, nil)
	a, b := login(t, s, "alice"), login(t, s, "bob")
	b.send("SILENCE +alice")
	b.expect(":bob!~bob@127.0.0.1 SILENCE +alice!*@*")
	b.send("SILENCE")
	b.expectNumeric("271", ":alice!*@*")
	b.expectNumeric("272")
	a.send("PRIVMSG bob :you there?")
	a.send("INVITE bob #x")
	a.sync()
	b.quiet("you there?")
	b.send("SILENCE -alice!*@*")
	b.expect("SILENCE -alice!*@*")
	a.send("PRIVMSG bob :now?")
	b.expect("now?")
	for i := range maxSilence {
		b.send("SILENCE +n" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "!*@*")
	}
	b.send("SILENCE +full!*@*")
	b.expectNumeric("511", "full!*@*")
	a.send("SILENCE nobody")
	a.expectNumeric("401", "nobody")
}

func TestKnock(t *testing.T) {
	s := testServer(t, nil)
	op, k := login(t, s, "op"), login(t, s, "knocker")
	joinAll("#k", op)
	k.send("KNOCK #k please")
	k.expectNumeric("480", ":Cannot knock on #k (Channel is not invite only!)")
	op.send("MODE #k +i")
	op.expect("MODE #k +i")
	k.send("KNOCK #k please")
	k.expect("Knocked on #k")
	op.expect(":irc.test.local NOTICE @#k :[Knock] by knocker!~knocker@127.0.0.1 (please)")
	k.send("KNOCK #k again")
	k.expect("Knocked on #k")
	k.send("KNOCK #k three")
	k.expect("Knocked on #k")
	k.send("KNOCK #k four")
	k.expectNumeric("480", ":Cannot knock on #k (You are KNOCK flooding)")
	op.send("MODE #k +K")
	op.expect("MODE #k +K")
	k2 := login(t, s, "knock2")
	k2.send("KNOCK #k hi")
	k2.expectNumeric("480", ":Cannot knock on #k (No knocks are allowed! (+K))")
	k2.send("KNOCK #nothere")
	k2.expectNumeric("480", ":Cannot knock on #nothere (Channel does not exist!)")
}

func TestSetname(t *testing.T) {
	s := testServer(t, nil)
	a := capLogin(t, s, "alice", "setname")
	b := capLogin(t, s, "bob", "setname")
	c := login(t, s, "carol")
	joinAll("#n", a, b, c)
	a.send("SETNAME :Alice in Hiveland")
	a.expect(":alice!~alice@127.0.0.1 SETNAME :Alice in Hiveland")
	b.expect(":alice!~alice@127.0.0.1 SETNAME :Alice in Hiveland")
	c.quiet("SETNAME")
	c.send("WHOIS alice")
	c.expectNumeric("311", "alice", "~alice", "127.0.0.1", "*", ":Alice in Hiveland")
	a.send("SETNAME :" + strings.Repeat("x", 51))
	a.expect("FAIL SETNAME INVALID_REALNAME")
	c.send("SETNAME :Carol")
	c.expect("Your real name is now Carol")
}

func TestChgAndSetCommands(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := capLogin(t, s, "ember", "chghost")
	e.send("CHGHOST ember x.y")
	e.expectNumeric("481")
	b.send("CHGHOST ember hive.example.org")
	e.expect(" 396 ember hive.example.org :is now your displayed host")
	b.expect("CHGHOST: Boss changed the virtual hostname of ember")
	b.send("CHGIDENT ember bee")
	e.expect(" 396 ember hive.example.org")
	b.expect("CHGIDENT: Boss changed the username of ember")
	b.send("CHGNAME ember :Ember of the Hive")
	b.expect("CHGNAME: Boss changed the realname of ember to be Ember of the Hive")
	b.send("WHOIS ember")
	b.expectNumeric("311", "ember", "bee", "hive.example.org", "*", ":Ember of the Hive")
	b.send("CHGHOST ember bad_host!")
	b.expect("/ChgHost Error")
	b.send("SETHOST boss.example.org")
	b.expect("Your nick!user@host-mask is now (Boss!")
	b.send("SETIDENT queen")
	b.expect("Your nick!user@host-mask is now (Boss!queen@boss.example.org)")
}

func TestSACommands(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := login(t, s, "ember")
	e.send("SAJOIN ember #x")
	e.expectNumeric("481")
	b.send("SAJOIN ember #hive,#comb")
	e.expect(":ember!~ember@127.0.0.1 JOIN #hive")
	e.expect("*** You were forced to join #hive,#comb")
	b.expect("SAJOIN: Boss used SAJOIN to make ember join #hive,#comb")
	b.send("SAPART ember #comb :tidy up")
	e.expect("PART #comb :tidy up")
	e.expect("*** You were forced to part #comb (tidy up)")
	b.send("SATOPIC #hive :Welcome to the hive")
	e.expect("TOPIC #hive :Welcome to the hive")
	b.expect("Boss used SATOPIC on #hive, new topic: Welcome to the hive")
	b.send("SAMODE #hive +m")
	e.expect("MODE #hive +m")
	b.expect("Boss used SAMODE: #hive +m")
	b.send("SANICK ember amber")
	e.expect(":ember!~ember@127.0.0.1 NICK amber")
	b.expect("Boss used SANICK to change ember to amber")
	b.send("SANICK NickServ x")
	b.expectNumeric("481", ":Cannot use this command on a network service")
}
