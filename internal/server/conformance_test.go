package server

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// Protocol conformance step: the gaps irctest found (AUDIT-TESTING.md §9),
// INFO, HELP numerics and the repeat /oper notice.

// next reads the next line, whatever it is.
func (c *tc) next() string {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("%s: reading the next line: %v", c.nick, err)
	}
	c.check(line)
	return strings.TrimRight(line, "\r\n")
}

func TestEmptyRealnameRefused(t *testing.T) { // A1
	s := testServer(t, nil)
	c := raw(t, s)
	c.nick = "bee"
	c.send("NICK bee")
	c.send("USER bee 0 * :")
	c.expect(" 461 bee USER :Not enough parameters")
	c.send("USER bee 0 * :Bee")
	c.expect(" 001 bee ")
}

func TestMuteException(t *testing.T) { // A2
	s := testServer(t, nil)
	op, b := login(t, s, "Op"), login(t, s, "Bee")
	joinAll("#m", op, b)
	op.send("MODE #m +b ~m:Bee!*@*")
	op.expect("MODE #m +b ~m:Bee!*@*")
	b.send("PRIVMSG #m :muted")
	b.expectNumeric("404", "#m")
	op.send("MODE #m +e ~m:Bee!*@*")
	op.expect("MODE #m +e ~m:Bee!*@*")
	b.send("PRIVMSG #m :free again")
	op.expect("PRIVMSG #m :free again")
	// A mute exception does not lift a real ban.
	op.send("MODE #m +b Bee!*@*")
	op.expect("MODE #m +b Bee!*@*")
	b.send("PART #m")
	b.send("JOIN #m")
	b.expectNumeric("474", "#m")
}

func TestKickChannelList(t *testing.T) { // A3
	s := testServer(t, nil)
	op, x, y := login(t, s, "Op"), login(t, s, "xx"), login(t, s, "yy")
	joinAll("#a", op, x, y)
	joinAll("#b", op, x, y)
	op.send("KICK #a,#b xx,yy :bye")
	op.expect(":Op!~op@127.0.0.1 KICK #a xx :bye")
	op.expect(":Op!~op@127.0.0.1 KICK #b yy :bye")
	op.send("KICK #a,#b xx")
	op.expectNumeric("461", "KICK")
}

func TestInputTooLong(t *testing.T) { // A4, A5
	s := testServer(t, nil)
	a := capLogin(t, s, "alice", "message-tags", "echo-message")
	b := capLogin(t, s, "bob", "message-tags")
	joinAll("#t", a, b)
	a.send("@+baz=" + strings.Repeat("a", 4089) + " TAGMSG #t") // tags 4096 bytes: fine
	b.expect("TAGMSG #t")
	a.send("@+baz=" + strings.Repeat("a", 4090) + " TAGMSG #t") // 4097: too long
	a.expectNumeric("417")
	a.send("PRIVMSG #t :" + strings.Repeat("b", 499)) // 511 bytes
	a.expectNumeric("417")
	b.quiet("TAGMSG")
	b.quiet("bbbb")
}

func TestWhoExactNickShowsInvisible(t *testing.T) { // A6
	s := testServer(t, nil)
	a, b := login(t, s, "alice"), login(t, s, "bob") // both +i, no shared channel
	a.send("WHO bob")
	a.expectNumeric("352", "*", "~bob")
	a.expectNumeric("315", "bob")
	a.send("WHO bo*")
	if l := a.expect(" 315 alice "); !strings.Contains(l, "bo*") {
		t.Fatal(l)
	}
	_ = b
}

func TestLabeledMessageToSelf(t *testing.T) { // A7-A9
	s := testServer(t, nil)
	c := capLogin(t, s, "foo", "echo-message", "batch", "labeled-response", "message-tags")
	for _, cmd := range []string{"PRIVMSG foo :hi", "NOTICE foo :hi", "@+typing=active TAGMSG foo"} {
		c.sync()
		line := "@label=12345 " + cmd
		if strings.HasPrefix(cmd, "@") {
			line = "@label=12345;" + cmd[1:]
		}
		c.send(line)
		verb := strings.Fields(strings.TrimPrefix(cmd, "@+typing=active "))[0]
		labels := 0
		for range 2 {
			l := c.next()
			if !strings.Contains(l, " "+verb+" foo") {
				t.Fatalf("%s: got %q, want two %s lines", verb, l, verb)
			}
			if strings.Contains(l, "label=12345") {
				labels++
			}
		}
		if labels != 1 {
			t.Fatalf("%s: %d lines carried the label, want 1", verb, labels)
		}
	}
}

func TestSASLAfterRegistrationOrder(t *testing.T) { // A10
	s := testServer(t, nil)
	old := registered(t, s, "Bee")
	old.send("QUIT")
	old.expect("ERROR :Closing Link")
	c := capLogin(t, s, "Bee2", "sasl")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte("Bee\x00Bee\x00"+pw)))
	c.expect(" 900 Bee2 ")
	if l := c.next(); !strings.Contains(l, " 903 Bee2 ") {
		t.Fatalf("after 900 came %q, want 903", l)
	}
	c.expect("MODE Bee2 +r")
}

func TestWhowasCount(t *testing.T) { // A11
	s := testServer(t, nil)
	a := login(t, s, "nick1")
	for range 2 {
		n := login(t, s, "nick2")
		n.send("QUIT :bye")
		n.expect("ERROR :Closing Link") // gone before the next nick2 arrives
	}
	a.send("WHOWAS nick2 1")
	a.expectNumeric("314", "nick2")
	if l := a.next(); !strings.Contains(l, " 312 ") {
		t.Fatal(l)
	}
	if l := a.next(); !strings.Contains(l, " 369 nick1 nick2 ") {
		t.Fatalf("want 369 after one entry, got %q", l)
	}
}

func TestSASLTooLong(t *testing.T) { // A12
	s := testServer(t, nil)
	c := raw(t, s)
	c.nick = "*"
	c.send("CAP REQ :sasl")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + strings.Repeat("A", 401))
	c.expect(" 905 * :SASL message too long")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	for range 21 { // 8400 bytes: over the 8192 limit on the last line
		c.send("AUTHENTICATE " + strings.Repeat("A", 400))
	}
	if l := c.expect(" 90"); !strings.Contains(l, " 904 * :SASL authentication failed") {
		t.Fatalf("too much SASL data: got %q, want 904", l)
	}
}

func TestInfoAndHelp(t *testing.T) { // A13, B1, B3
	s := testServer(t, nil)
	c := login(t, s, "Bee")
	c.send("INFO")
	c.expectNumeric("371", ":BeeIRCd")
	c.expectNumeric("374", ":End of /INFO list")
	c.send("INFO no.such.server")
	c.expectNumeric("402", "no.such.server")
	for _, cmd := range []string{"HELP", "HELPOP"} {
		c.send(cmd)
		c.expectNumeric("704", "*", ":***** BeeIRCd help *****")
		c.expectNumeric("706", "*")
		c.send(cmd + " privmsg")
		c.expectNumeric("704", "PRIVMSG", ":PRIVMSG <target[,target]> <text>")
		c.expectNumeric("705", "PRIVMSG")
		c.expectNumeric("706", "PRIVMSG", ":End of /HELP")
		c.send(cmd + " nothingsuch")
		c.expectNumeric("524", "NOTHINGSUCH")
	}
	c.send("HELP CHMODES")
	c.expectNumeric("704", "CHMODES", ":***** Channel modes *****")
	c.expectNumeric("705", "CHMODES", ": F <name>   flood profile:")
	c.send("VERSION")
	c.expect("ACCOUNTEXTBAN=a")
}

func TestOperAgainSaysWhy(t *testing.T) { // A14
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("OPER Boss wrong-password")
	b.expect("*** You are already an IRC operator (as Boss, class netadmin). To oper again, de-oper first: /mode Boss -o")
	b.quiet("464") // no password check ran, so no failure counted
	b.send("MODE Boss -o")
	b.expect("MODE Boss -os")
	b.send("OPER Boss " + operPW)
	b.expect(" 381 Boss :You are now an IRC operator")
}
