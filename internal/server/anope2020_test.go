package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// Services follow Anope 2.0.20 (PLAN-anope-2.0.20.md).

// NickServ ACCESS, SET SECURE and STATUS, and ChanServ SET SECURE.
func TestNickServAccessSecureStatus(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Services.IdentifyWithin.Duration = 600 * time.Millisecond })
	bee := founded(t, s, "Bee")
	bee.send("NS ACCESS ADD nobang")
	bee.expect(nsLine("Bee", "Masks look like user@host, for example ~bee@*.example.org."))
	bee.send("NS ACCESS ADD ~bee@127.0.0.*")
	bee.expect(nsLine("Bee", "~bee@127.0.0.* added to Bee's access list."))
	bee.send("NS ACCESS ADD ~BEE@127.0.0.*")
	bee.expect(nsLine("Bee", "~BEE@127.0.0.* is already on Bee's access list."))
	bee.send("NS ACCESS LIST")
	bee.expect(nsLine("Bee", "Access list for Bee:"))
	bee.expect(nsLine("Bee", "    ~bee@127.0.0.*"))
	bee.send("CS SET SECURE #bchat OFF")
	bee.expect(csLine("Bee", "Security is now off for #bchat."))
	bee.send("QUIT")
	bee.expect("ERROR")

	// SECURE on (the default): on the access list means no rename, but
	// nothing else.
	x := login(t, s, "x")
	joinAll("#bchat", x) // keeps #bchat open for the joins below
	imp := login(t, s, "Bee")
	imp.expect(nsLine("Bee", "Bee is a registered nick. If it's yours: /ns identify <password>"))
	time.Sleep(900 * time.Millisecond)
	imp.quiet(" NICK Guest")
	x.send("NS STATUS Bee x nobody")
	x.expect(nsLine("x", "STATUS Bee 1 "))
	x.expect(nsLine("x", "STATUS x 0 "))
	x.expect(nsLine("x", "STATUS nobody 0 "))
	joinAll("#bchat", imp)
	imp.quiet(csSrc + "MODE #bchat +o Bee")
	imp.send("NS IDENTIFY " + pw)
	imp.expect(" 900 Bee ")
	x.send("NS STATUS Bee")
	x.expect(nsLine("x", "STATUS Bee 3 Bee"))
	imp.send("NS SET SECURE OFF")
	imp.expect(nsLine("Bee", "Security is off for Bee: a connection matching the ACCESS list counts as the owner."))
	imp.send("NS INFO")
	imp.expect(nsLine("Bee", "  Options       kill protection, private, auto-op, email hidden, usermask hidden"))
	imp.send("QUIT")
	imp.expect("ERROR")

	// SECURE off: a recognised connection is left alone, STATUS says 2 and,
	// with the channel's SECURE off, it has the founder's access.
	again := login(t, s, "Bee")
	again.quiet("registered nick")
	x.send("NS STATUS Bee")
	x.expect(nsLine("x", "STATUS Bee 2 "))
	again.send("JOIN #bchat")
	again.expect(csSrc + "MODE #bchat +o Bee")
	again.send("CS SET SECURE #bchat ON")
	again.expect(csLine("Bee", "Security is now on for #bchat."))
	again.send("CS OP #bchat")
	again.expect(csLine("Bee", "Identify first: /ns identify <password>"))
	again.send("NS ACCESS LIST") // ACCESS itself needs the password
	again.expect(nsLine("Bee", "Identify first: /ns identify <password>"))

	again.send("NS IDENTIFY " + pw)
	again.expect(" 900 Bee ")
	again.send("NS ACCESS DEL ~bee@127.0.0.*")
	again.expect(nsLine("Bee", "~bee@127.0.0.* deleted from Bee's access list."))
	again.send("NS ACCESS DEL ~bee@127.0.0.*")
	again.expect(nsLine("Bee", "~bee@127.0.0.* isn't on Bee's access list."))
	again.send("NS ACCESS LIST")
	again.expect(nsLine("Bee", "Bee's access list is empty."))
	again.send("NS SET HIDE MASK ON") // Anope 2.1's name, hidden
	again.expect(nsLine("Bee", "Your last user@host is hidden from others in INFO."))
	again.send("NS SET HIDE BOGUS ON")
	again.expect(nsLine("Bee", "Syntax: SET HIDE {EMAIL | STATUS | USERMASK | QUIT} {ON | OFF}"))
	x.send("NS ACCESS LIST Bee")
	x.expect(nsLine("x", "Identify first: /ns identify <password>"))
}

func TestKillModes(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.Services.KillQuick.Duration = 300 * time.Millisecond
		c.Services.AllowKillImmed = true
	})
	bee := registered(t, s, "Bee")
	bee.send("NS SET KILL QUICK")
	bee.expect(nsLine("Bee", "Kill protection is on for Bee, quick: people who don't identify within 0 seconds get renamed."))
	bee.send("NS INFO")
	bee.expect(nsLine("Bee", "  Options       quick kill, security, private, auto-op, email hidden, usermask hidden"))
	bee.send("NICK Bee_")
	bee.expect(" NICK Bee_")
	imp := login(t, s, "Bee")
	imp.expect(nsLine("Bee", "Bee is a registered nick. Identify within 0 seconds"))
	imp.expect(" NICK Guest")

	bee.send("NICK Bee")
	bee.expect(" NICK Bee")
	bee.send("NS SET KILL IMMED")
	bee.expect(nsLine("Bee", "Kill protection is on for Bee, with no delay: people who use the nick without identifying are renamed at once."))
	bee.send("NICK Bee__")
	bee.expect(" NICK Bee__")
	imp2 := login(t, s, "Bee")
	imp2.expect(nsLine("Bee", "Bee is a registered nick, and you may not use it."))
	imp2.expect(" NICK Guest")
}

func TestUnbanEverywhereAndLevels(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	joinAll("#hive", bee)
	bee.send("CS REGISTER #hive")
	bee.expect(csLine("Bee", "#hive is registered to Bee. You are its Founder."))
	login(t, s, "other") // a channel Bee has no access to stays as it is
	bee.send("MODE #bchat +b *!*@127.0.0.1")
	bee.expect("MODE #bchat +b *!*@127.0.0.1")
	bee.send("MODE #hive +b *!~bee@*")
	bee.expect("MODE #hive +b *!~bee@*")
	bee.send("CS UNBAN")
	bee.expect(csSrc + "MODE #")
	bee.expect(csSrc + "MODE #")
	bee.expect(csLine("Bee", "You have been unbanned from 2 channels."))
	bee.send("CS UNBAN #bchat")
	bee.expect(csLine("Bee", "You aren't banned in #bchat."))
	// UNBANME is gone: UNBAN covers yourself too (Anope 2.0.20).
	bee.send("CS LEVELS #bchat SET UNBANME 1")
	bee.expect(csLine("Bee", "UNBANME isn't a privilege. See /cs levels #bchat list."))
	bee.send("CS HELP UNBAN")
	bee.expect(csLine("Bee", "Syntax: UNBAN [#chan [nick|mask]]"))
}

func TestOperServ2020(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := login(t, s, "user")
	b.send("OS STATS AKILL")
	b.expect(osLine("Boss", "Network bans (without +expiry they last 30 days; ELINEs stay until removed):"))
	b.expect(osLine("Boss", "  AKILL 0, SNLINE 0, SQLINE 0, ZLINE 0, SHUN 0, ELINE 0"))
	b.send("OS STATS UPTIME")
	b.expect(osLine("Boss", "Current users: 2 (1 ops)"))
	b.send("OS STATS UPLINK")
	b.expect(osLine("Boss", "Uplink: none, irc.test.local runs on its own"))
	b.send("OS STATS HASH")
	b.expect(osLine("Boss", "Tables (entries): users "))
	b.send("OS STATS bogus")
	b.expect(osLine("Boss", "Unknown STATS option: bogus"))
	b.send("OS STATS RESET")
	b.expect(osLine("Boss", "Statistics reset: the peak is now the number of people online."))

	// MODE CLEAR: modes and lists; CLEAR ALL also status, except opers'.
	joinAll("#m", b, u)
	b.send("MODE #m +ik key")
	b.expect("MODE #m +ik key")
	b.send("MODE #m +bo *!*@x.example user")
	b.expect("MODE #m +bo *!*@x.example user")
	b.send("OS MODE #m CLEAR")
	u.expect(":OperServ!services@irc.test.local MODE #m -")
	b.expect(osLine("Boss", "Non-status modes cleared on #m."))
	b.send("MODE #m")
	b.expect(" 324 Boss #m +")
	b.send("MODE #m b")
	b.expect(" 368 Boss #m ")
	b.send("OS MODE #m CLEAR ALL")
	u.expect(":OperServ!services@irc.test.local MODE #m -o user")
	b.expect(osLine("Boss", "All modes cleared on #m."))
	b.send("NAMES #m")
	expectNames(t, b.expect(" 353 Boss "), "@Boss user")

	// SNLINE in Anope's mask:reason form; a bare number of days; +0.
	b.send("OS SNLINE ADD +1h free bitcoin:crypto spam")
	b.expect(osLine("Boss", "SNLINE added: free bitcoin for 60 minutes (crypto spam)."))
	b.send("OS SNLINE ADD nothing:")
	b.expect(osLine("Boss", "Syntax: SNLINE ADD [+expiry] <mask>:<reason>"))
	b.send("OS AKILL ADD +2 *@198.51.100.50 two days")
	b.expect(osLine("Boss", "AKILL added: *@198.51.100.50 for 2 days (two days)."))
	b.send("OS AKILL ADD +0 *@198.51.100.51 for good")
	b.expect(osLine("Boss", "AKILL added: *@198.51.100.51 until removed (for good)."))
	b.send("OS AKILL ADD *@198.51.100.52 default")
	b.expect(osLine("Boss", "AKILL added: *@198.51.100.52 for 30 days (default)."))
	b.send("ZLINE 198.51.100.0/24 0 irc style, for good")
	b.expect(osLine("Boss", "ZLINE added: 198.51.100.0/24 until removed (irc style, for good)."))
}

// The log lines of the commands that changed, as Anope 2.0.20 writes them.
func TestLogChannel2020(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("AS SET log-channel #services")
	b.expect("log-channel is now #services")
	joinAll("#services", b)
	ns := ":NickServ!services@irc.test.local " + logSrc
	cs := ":ChanServ!services@irc.test.local " + logSrc
	u := founded(t, s, "user")
	u.send("NS ACCESS ADD ~user@*")
	b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used ACCESS to ADD mask ~user@* to user")
	u.send("NS ACCESS DEL ~user@*")
	b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used ACCESS to DELETE mask ~user@* from user")
	u.send("NS SET KILL QUICK")
	b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used SET to set kill quick for user")
	u.send("NS SET KILL OFF")
	b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used SET to disable kill for user")
	u.send("NS SET SECURE OFF")
	b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used SET to disable secure for user")
	u.send("NS SET HIDE USERMASK OFF")
	b.expect(ns + "COMMAND: user!~user@127.0.0.1 (user) used SET to change hide USERMASK to OFF for user")
	u.send("CS SET SECURE #bchat OFF")
	b.expect(cs + "COMMAND: user!~user@127.0.0.1 (user) used SET on #bchat to disable secure")
	u.send("CS UNBAN")
	b.expect(cs + "COMMAND: user!~user@127.0.0.1 (user) used UNBAN on all channels")
	u.send("NICK user2")
	u.send("NS GROUP user")
	u.expect("user2 is now part of the account user.")
	u.send("NS UNGROUP user2")
	b.expect(ns + "COMMAND: user2!~user@127.0.0.1 (user) used UNGROUP to make user2 leave group of user (email: none)")
	b.send("NS DROP user2")
	b.expect(ns + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used DROP to drop nickname user2 (group: user2) (email: none)")
}
