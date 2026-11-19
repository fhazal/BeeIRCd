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

	// Lines are Anope 2.0.20's, each from the service it belongs to.
	ns := ":NickServ!services@irc.test.local " + logSrc
	cs := ":ChanServ!services@irc.test.local " + logSrc
	osl := ":OperServ!services@irc.test.local " + logSrc
	asl := ":AdminServ!services@irc.test.local " + logSrc
	gl := ":Global!services@irc.test.local " + logSrc

	// Commands that only show something log nothing.
	u.send("NS INFO user")
	u.send("CS LIST *")
	u.send("MS LIST")
	b.quiet(logSrc)

	// Refused: an unidentified user, a non-oper messaging OperServ.
	g := login(t, s, "guest")
	b.expect(gl + "USERS: guest!~guest@127.0.0.1 (guest Real) connected to the network (irc.test.local)")
	g.send("CS REGISTER #g")
	b.expect(cs + "Access denied for unregistered user guest!~guest@127.0.0.1 with command REGISTER")
	u.send("OS STATS")
	b.expect(osl + "Denied access to OperServ from user!~user@127.0.0.1 (non-oper)")

	// IDENTIFY: failed, unknown account, then working. No password, ever.
	g.send("NS IDENTIFY user wrong-secret")
	line := b.expect(ns + "COMMAND: guest!~guest@127.0.0.1 used IDENTIFY and failed to identify to account user")
	g.send("NS IDENTIFY nobody x123456")
	b.expect(ns + "COMMAND: guest!~guest@127.0.0.1 used IDENTIFY and failed to identify to nonexistent account nobody")
	g.send("NS IDENTIFY user " + pw)
	line += b.expect(ns + "COMMAND: guest!~guest@127.0.0.1 used IDENTIFY and identified for account user")
	u.send("NS SET PASSWORD new-secret-1")
	line += b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used SET to change their password")
	for _, secret := range []string{"wrong-secret", "new-secret-1", pw} {
		if strings.Contains(line, secret) {
			t.Fatalf("password in the log: %s", line)
		}
	}

	// ChanServ: COMMAND with "on #chan", OVERRIDE for an oper without access.
	joinAll("#own", u)
	u.send("CS REGISTER #own")
	b.expect(cs + "COMMAND: user!~user@127.0.0.1 (user) used REGISTER on #own")
	joinAll("#own", g)
	u.send("CS KICK #own guest bye")
	b.expect(cs + "COMMAND: user!~user@127.0.0.1 (user) used KICK on #own for guest")
	u.send("CS SET PEACE #own ON")
	b.expect(cs + "COMMAND: user!~user@127.0.0.1 (user) used SET on #own to enable peace")
	b.send("CS DROP #own")
	b.expect("Type the channel name twice to drop it, as a confirmation: /cs drop #own #own")
	b.send("CS DROP #own #own")
	b.expect(cs + "OVERRIDE: Boss!~boss@127.0.0.1 (Boss) used DROP on #own (founder was: user)")

	// OperServ and its IRCd-style aliases: ADMIN, Anope's text, no audit copy.
	b.send("OS AKILL ADD +1h *@203.0.113.9 spam")
	b.expect(osl + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used AKILL on *@203.0.113.9 (spam), expires in 1 hour [affects 0 user(s) (0%)]")
	b.send("GLINE *@198.51.100.7 1d bad")
	b.expect(osl + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used AKILL on *@198.51.100.7 (bad), expires in 1 day [affects 0 user(s) (0%)]")
	b.send("OS AKILL DEL *@203.0.113.9")
	b.expect(osl + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used AKILL to remove *@203.0.113.9 from the list")
	b.send("AS SET max-users 500")
	b.expect(asl + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used SET to change the configuration value of limits:max_users to 500")
	u.send("OPER user nope") // a server notice, not an Anope log line
	b.quiet(logSrc + "failed to log into")

	// Deopping a bot puts it back.
	b.send("OS MODE #services -o NickServ")
	b.expect(csSrc + "MODE #services +o NickServ")

	// USERS lines are on by default (Anope's users = "connect disconnect nick").
	ld := login(t, s, "loud")
	b.expect(gl + "USERS: loud!~loud@127.0.0.1 (loud Real) connected to the network (irc.test.local)")
	ld.send("NICK louder")
	b.expect(gl + "USERS: loud!~loud@127.0.0.1 (loud Real) changed nick to louder")
	ld.send("QUIT :bye")
	b.expect(gl + "USERS: louder!~loud@127.0.0.1 (loud Real) disconnected from the network (irc.test.local)")
	b.send("AS SET log-connects off")
	b.expect("log-connects is now off")
	login(t, s, "quietone")
	b.quiet(logSrc + "USERS: quietone")

	// SASL PLAIN is not logged by Anope 2.0.20 (only EXTERNAL is).
	gone := registered(t, s, "Sas")
	gone.send("QUIT")
	gone.expect("ERROR :Closing Link") // nick free again
	sc := raw(t, s)
	sc.nick = "Sas"
	sc.send("CAP REQ :sasl")
	sc.expect("ACK :sasl")
	sc.send("NICK Sas")
	sc.send("USER sas 0 * :Sas")
	sc.send("AUTHENTICATE PLAIN")
	sc.expect("AUTHENTICATE +")
	sc.send("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte("\x00Sas\x00"+pw)))
	sc.expect(" 903 ")
	b.quiet("M_SASL")

	// Moving it: the bots leave the old channel.
	b.send("AS SET log-channel off")
	b.expect(":NickServ!services@irc.test.local PART #services :No longer the log channel")
	b.expect("log-channel is now off (was #services).")
	u.send("NS LOGOUT")
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

// Event lines from the server's side: a kill and a K-line hit.
func TestLogChannelEvents(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("AS SET log-channel #services")
	b.expect("log-channel is now #services")
	joinAll("#services", b)
	gl := ":Global!services@irc.test.local " + logSrc
	osl := ":OperServ!services@irc.test.local " + logSrc

	g := login(t, s, "guest")
	b.expect(gl + "USERS: guest!~guest@127.0.0.1 (guest Real) connected to the network (irc.test.local)")
	b.send("KILL guest :flooding")
	b.expect(gl + "USERS: guest!~guest@127.0.0.1 was killed by Boss (Reason: flooding)")
	g.expect("KILL guest")

	v := login(t, s, "victim")
	b.expect(gl + "USERS: victim!~victim@127.0.0.1 (victim Real) connected")
	b.send("KLINE +1h victim go away")
	b.expect(osl + "KLINE: *@127.0.0.1 hit victim!~victim@127.0.0.1 (set by Boss: go away)")
	v.expect("ERROR")
}
