package server

import (
	"strings"
	"testing"
)

// The unrealircd.org/issued-by tag: opers with message-tags see who is
// behind a line (an oper command, services, an RPC call); others never do.
func TestIssuedByTag(t *testing.T) {
	s, _, _ := fileServer(t, "\n[rpc]\nlisten = \"127.0.0.1:0\"\n\n[[rpc.user]]\nname = \"tool\"\npassword = \""+rpcHash()+"\"\n")
	b := boss(t, s)
	b.send("CAP REQ :message-tags")
	b.expect("ACK :message-tags")
	w := capLogin(t, s, "watcher", "message-tags")
	v := login(t, s, "victim")
	joinAll("#hive", b, w, v)

	tagged := func(c *tc, want, issuer string) {
		t.Helper()
		l := c.expect(want)
		has := strings.Contains(l, "unrealircd.org/issued-by="+issuer)
		if c == b && !has {
			t.Errorf("oper's line lacks issued-by=%s: %s", issuer, l)
		}
		if c != b && strings.Contains(l, "issued-by") {
			t.Errorf("non-oper got issued-by: %s", l)
		}
	}
	oper := "OPER:Boss@irc.test.local"

	b.send("SAMODE #hive +m")
	tagged(b, "MODE #hive +m", oper)
	tagged(w, "MODE #hive +m", oper)
	b.send("SAJOIN victim #comb")
	b.send("SAPART victim #comb")
	b.send("JOIN #comb")
	b.expect(" 366 Boss #comb ")
	b.send("SAJOIN victim #comb")
	tagged(b, ":victim!~victim@127.0.0.1 JOIN #comb", oper)
	b.send("SAPART victim #comb")
	tagged(b, ":victim!~victim@127.0.0.1 PART #comb", oper)
	b.send("SAKICK #hive victim out")
	tagged(b, "KICK #hive victim :out", oper)
	tagged(w, "KICK #hive victim :out", oper)

	// A normal line from the oper carries nothing.
	b.send("PRIVMSG #hive :just chatting")
	if l := w.expect("PRIVMSG #hive :just chatting"); strings.Contains(l, "issued-by") {
		t.Errorf("plain message tagged: %s", l)
	}
	b.send("MODE #hive -m")
	if l := b.expect("MODE #hive -m"); strings.Contains(l, "issued-by") {
		t.Errorf("chanop MODE tagged: %s", l)
	}

	// Services: an OperServ KICK.
	v.send("JOIN #hive")
	b.expect(":victim!~victim@127.0.0.1 JOIN #hive")
	w.expect(":victim!~victim@127.0.0.1 JOIN #hive")
	b.send("OS KICK #hive victim be nice")
	tagged(b, "KICK #hive victim :be nice", "SERVICES:OperServ@")
	tagged(w, "KICK #hive victim :be nice", "")

	// RPC.
	rpcOK(t, s, "channel.set_mode", `{"channel":"#hive","modes":"+s"}`)
	tagged(b, "MODE #hive +s", "RPC:tool@irc.test.local")
	tagged(w, "MODE #hive +s", "")

	// KILL: the QUIT the channel sees.
	v.send("JOIN #hive")
	b.expect(":victim!~victim@127.0.0.1 JOIN #hive")
	w.expect(":victim!~victim@127.0.0.1 JOIN #hive")
	b.send("KILL victim :enough")
	tagged(b, ":victim!~victim@127.0.0.1 QUIT :Killed", oper)
	tagged(w, ":victim!~victim@127.0.0.1 QUIT :Killed", "")
	// After the command nothing is left behind.
	b.send("PRIVMSG #hive :after")
	b.send("TOPIC #hive :plain topic")
	if l := b.expect("TOPIC #hive :plain topic"); strings.Contains(l, "issued-by") {
		t.Errorf("issuer left behind: %s", l)
	}
}

// A line from a server link with the tag passes it on; links get it from
// ours, InspIRCd links (the bridge) never.
func TestIssuedLine(t *testing.T) {
	s := &Server{}
	o := &linkConn{}
	if got := s.issuedLine(o, ":0BE KILL 0BEAAAAAB :x"); got != ":0BE KILL 0BEAAAAAB :x" {
		t.Errorf("no issuer: %q", got)
	}
	s.issuer = "RPC:tool@irc.test.local:Belon the Bee"
	if got := s.issuedLine(o, ":0BE KILL 0BEAAAAAB :x"); got != `@unrealircd.org/issued-by=RPC:tool@irc.test.local:Belon\sthe\sBee :0BE KILL 0BEAAAAAB :x` {
		t.Errorf("tagged: %q", got)
	}
	if got := s.issuedLine(o, "@time=1 :0BE KILL 0BEAAAAAB :x"); got != `@unrealircd.org/issued-by=RPC:tool@irc.test.local:Belon\sthe\sBee;time=1 :0BE KILL 0BEAAAAAB :x` {
		t.Errorf("with tags: %q", got)
	}
}
