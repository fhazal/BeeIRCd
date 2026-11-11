package server

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// The services log channel ([services] log_channel) and channel mode +O.

const logSrc = "PRIVMSG #services :"

func TestOperOnlyMode(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := login(t, s, "user")
	joinAll("#staff", u)
	u.send("MODE #staff +O")
	u.expect(" 481 user :Permission Denied - only IRC operators may set +O")
	joinAll("#opers", b)
	b.send("MODE #opers +O")
	b.expect("MODE #opers +O")
	u.send("JOIN #opers")
	u.expect(" 520 user #opers :Cannot join channel (+O)")
	u.send("MODE #opers")
	u.expect(" 324 user #opers +")
}

func TestLogChannel(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)
	u := registered(t, s, "user")
	joinAll("#services", u) // a non-oper sitting there gets kicked when it becomes the log channel

	b.send("AS SET log-channel #services")
	u.expect(csSrc + "KICK #services user :This channel is for IRC operators (+O)")
	b.expect(asLine("Boss", "log-channel is now #services (was off). Saved to beeircd.conf."))
	conf, _ := os.ReadFile(path)
	if !strings.Contains(string(conf), `log_channel = "#services"`) {
		t.Fatalf("log_channel not saved:\n%s", conf)
	}
	u.send("JOIN #services")
	u.expect(" 520 user #services :Cannot join channel (+O)")

	joinAll("#services", b)
	b.send("NAMES #services")
	names := b.expect(" 353 Boss ")
	for _, bot := range []string{"@AdminServ", "@ChanServ", "@Global", "@MemoServ", "@NickServ", "@OperServ"} {
		if !strings.Contains(names, bot) {
			t.Fatalf("%s missing in %q", bot, names)
		}
	}

	// A command that works, one that is refused, an unknown one.
	u.send("CS REGISTER #nowhere")
	b.expect(":ChanServ!services@irc.test.local " + logSrc + "COMMAND: user!~user@127.0.0.1 (user) used REGISTER #nowhere → ")
	u.send("OS STATS")
	b.expect(":OperServ!services@irc.test.local " + logSrc + "ADMIN: user!~user@127.0.0.1 (user) used STATS → OperServ is for IRC operators.")
	u.send("NS FROB")
	b.expect(":NickServ!services@irc.test.local " + logSrc + "COMMAND: user!~user@127.0.0.1 (user) used FROB → Unknown command FROB.")

	// Passwords never show; a password check answers later.
	u.send("NS IDENTIFY wrong-secret")
	line := b.expect(":NickServ!services@irc.test.local " + logSrc + "COMMAND: user!~user@127.0.0.1 (user) used IDENTIFY **** → ")
	if strings.Contains(line, "wrong-secret") {
		t.Fatalf("password logged: %s", line)
	}
	u.send("NS SET PASSWORD new-secret-1")
	line = b.expect("used SET PASSWORD **** → ")
	if strings.Contains(line, "new-secret-1") {
		t.Fatalf("password logged: %s", line)
	}
	// The drop code stays hidden.
	u.send("NS DROP user")
	b.expect("used DROP user → (reply hidden)")

	// Oper commands log once (no audit copy), oper events come from OperServ.
	b.send("OS AKILL ADD +1h *@203.0.113.9 spam")
	b.expect(":OperServ!services@irc.test.local " + logSrc + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used AKILL ADD +1h *@203.0.113.9 spam → AKILL added")
	b.quiet(logSrc + "Boss used OperServ")
	b.send("AS OPER LIST")
	b.expect(":AdminServ!services@irc.test.local " + logSrc + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used OPER LIST → ")
	u.send("OPER user nope")
	b.expect(":OperServ!services@irc.test.local " + logSrc + "OPER: Failed /oper as user from user")

	// Deopping a bot puts it back.
	b.send("OS MODE #services -o NickServ")
	b.expect(csSrc + "MODE #services +o NickServ")

	// Connects are logged only with log-connects on.
	login(t, s, "quietone")
	b.quiet(logSrc + "Connect: quietone")
	b.send("AS SET log-connects on")
	b.expect("log-connects is now on")
	ld := login(t, s, "loud")
	b.expect(":Global!services@irc.test.local " + logSrc + "USERS: loud!~loud@127.0.0.1 (loud Real) [127.0.0.1] connected to the network (irc.test.local)")
	ld.send("NICK louder")
	b.expect(":Global!services@irc.test.local " + logSrc + "USERS: loud!~loud@127.0.0.1 (loud Real) changed nick to louder")
	ld.send("NICK loud")
	b.expect("changed nick to loud")
	b.send("KILL loud :bye now")
	b.expect(":Global!services@irc.test.local " + logSrc + "USERS: loud!~loud@127.0.0.1 (loud Real) disconnected from the network (irc.test.local) (Killed (Boss (bye now)))")

	// SASL, Anope's M_SASL lines from NickServ.
	registered(t, s, "Sas").send("QUIT")
	sc := raw(t, s)
	sc.nick = "Sas"
	sc.send("CAP REQ :sasl")
	sc.expect("ACK :sasl")
	sc.send("NICK Sas")
	sc.send("USER sas 0 * :Sas")
	sc.send("AUTHENTICATE PLAIN")
	sc.expect("AUTHENTICATE +")
	sc.send("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte("\x00Sas\x00nope")))
	b.expect(":NickServ!services@irc.test.local " + logSrc + "M_SASL: 127.0.0.1 (127.0.0.1) failed to identify for account Sas using SASL PLAIN (wrong password)")
	sc.send("AUTHENTICATE PLAIN")
	sc.expect("AUTHENTICATE +")
	sc.send("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte("\x00Sas\x00"+pw)))
	b.expect(":NickServ!services@irc.test.local " + logSrc + "M_SASL: 127.0.0.1 (127.0.0.1) identified to account Sas using SASL PLAIN")

	// Moving it: the bots leave the old channel.
	b.send("AS SET log-channel off")
	b.expect(":NickServ!services@irc.test.local PART #services :No longer the log channel")
	b.expect("log-channel is now off (was #services).")
	u.send("NS INFO user")
	b.quiet(logSrc)
}

func TestLogChannelAtStart(t *testing.T) {
	s, _, _ := fileServer(t, "log_channel = \"#services\"\n") // lands in [services], the last table
	b := boss(t, s)
	joinAll("#services", b)
	b.send("MODE #services")
	b.expect(" 324 Boss #services +")
	b.send("NAMES #services")
	if names := b.expect(" 353 Boss "); !strings.Contains(names, "@OperServ") || !strings.Contains(names, "@AdminServ") {
		t.Fatalf("bots missing: %s", names)
	}
	u := login(t, s, "user")
	u.send("JOIN #services")
	u.expect(" 520 user #services :Cannot join channel (+O)")
}
