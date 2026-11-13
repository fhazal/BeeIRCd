package main

import (
	"fmt"
	"time"
)

// bots checks BotServ (v1.0) as a channel founder sees it. It never opers
// up, so it assigns ChanServ (a service bot anyone may assign, like in
// Anope) instead of making a bot with BOT ADD.
func (lc *liveCheck) bots() {
	PW := lc.pw
	ch := fmt.Sprintf("#zt6%d", time.Now().Unix()%10000)
	join := func(c *client) string {
		c.send("JOIN " + ch)
		return c.wait(fmt.Sprintf(" 366 %s %s ", c.nick, ch))
	}
	a, b := lc.connect("zt6a", ""), lc.connect("zt6b", "")
	a.clear()
	b.clear()
	a.send("NS REGISTER " + PW + " zt6a@example.org")
	if a.wait("at least 30 seconds", 3*time.Second) != "" {
		println("waiting out reg_delay (30 s)...")
		time.Sleep(31 * time.Second)
		a.send("NS REGISTER " + PW + " zt6a@example.org")
	}
	lc.ok("zt6a registers", a.wait("registered", 8*time.Second))
	join(a)
	a.send("CS REGISTER " + ch + " live bot test")
	lc.ok("channel registered", a.wait("is registered to"))
	join(b)

	b.send("BS BOT ADD ztbot bot bot.host x")
	lc.ok("BOT ADD is for opers", b.wait("BotServ.* :Access denied"))
	a.send("BS BOTLIST")
	lc.ok("BOTLIST lists the service bots", a.wait("BotServ.* :  ChanServ "))
	b.send("BS ASSIGN " + ch + " ChanServ")
	lc.ok("ASSIGN needs the ASSIGN privilege", b.wait("BotServ.* :Access denied"))
	a.send("BS ASSIGN " + ch + " ChanServ")
	lc.ok("ASSIGN", a.wait("Bot ChanServ has been assigned to "+ch))
	lc.ok("the bot joins", b.wait(":ChanServ!.* JOIN "+ch))
	a.send("BS SAY " + ch + " hello from the bot")
	lc.ok("SAY", b.wait(":ChanServ!.* PRIVMSG "+ch+" :hello from the bot"))
	a.send("BS INFO " + ch)
	lc.ok("INFO #channel", a.wait("Bot nick: ChanServ"))

	// Kickers.
	a.send("BS SET " + ch + " DONTKICKOPS ON")
	a.wait("won't kick ops")
	a.send("BS KICK CAPS " + ch + " ON")
	lc.ok("KICK CAPS ON", a.wait("Bot will now kick for caps"))
	b.send("PRIVMSG " + ch + " :THIS IS A VERY LOUD LINE")
	lc.ok("caps kicker kicks", b.wait("KICK "+ch+" zt6b :Turn caps lock OFF!"))
	join(b)
	a.send("BS BADWORDS " + ch + " ADD ztwasp")
	lc.ok("BADWORDS ADD", a.wait("ztwasp added to "+ch+" bad words list."))
	a.send("BS KICK BADWORDS " + ch + " ON")
	a.wait("kick for badwords")
	b.send("PRIVMSG " + ch + " :a ztwasp here")
	lc.ok("badwords kicker kicks", b.wait("KICK "+ch+" zt6b :Watch your language!"))
	join(b)

	// Fantasy and greet.
	a.send("PRIVMSG " + ch + " :!voice zt6b")
	lc.ok("!voice from the bot", b.wait(":ChanServ!.* MODE "+ch+` \+v zt6b`))
	a.send("NS SET GREET buzzing in")
	lc.ok("NS SET GREET", a.wait("Greet message for zt6a changed to buzzing in"))
	a.send("PART " + ch)
	a.wait("PART " + ch)
	join(a)
	lc.ok("the bot greets", b.wait(":ChanServ!.* PRIVMSG "+ch+` :\[zt6a\] buzzing in`))

	a.send("BS UNASSIGN " + ch)
	lc.ok("UNASSIGN", a.wait("There is no bot assigned to "+ch+" anymore."))

	// Clean up.
	a.send("CS DROP " + ch + " " + ch)
	lc.ok("channel dropped", a.wait("no longer registered"))
	a.send("NS DROP zt6a")
	lc.ok("zt6a dropped", a.wait("has been dropped"))
	a.send("QUIT :done")
	b.send("QUIT :done")
	time.Sleep(500 * time.Millisecond)
}
