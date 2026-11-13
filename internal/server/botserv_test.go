package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// BotServ (Anope 2.0.20): BOT ADD/CHANGE/DEL, BOTLIST, ASSIGN, SAY, ACT,
// INFO, SET, greets, the bot leaving an empty channel.

func bsLine(nick, text string) string {
	return ":BotServ!services@irc.test.local NOTICE " + nick + " :" + text
}

func TestBotServBasics(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bee := registered(t, s, "Bee")

	// Only opers with flag b make bots.
	bee.send("BS BOT ADD Bzz bot bot.host Busy Bee")
	bee.expect(bsLine("Bee", "Access denied."))
	b.send("BS BOT ADD Bzz! bot bot.host x")
	b.expect(bsLine("Boss", "Bot nicks may only contain valid nick characters."))
	b.send("BS BOT ADD Bzz bot bad_host x")
	b.expect(bsLine("Boss", "Bot hosts may only contain valid host characters."))
	b.send("BS BOT ADD Bee bot bot.host x")
	b.expect(bsLine("Boss", "Nickname Bee is already registered!"))
	b.send("BS BOT ADD ChanServ bot bot.host x")
	b.expect(bsLine("Boss", "Bot ChanServ already exists."))
	b.send("BS BOT ADD Bzz bot bot.host Busy Bee")
	b.expect(bsLine("Boss", "Bzz!bot@bot.host (Busy Bee) added to the bot list."))
	b.send("BS BOT ADD bzz bot bot.host again")
	b.expect(bsLine("Boss", "Bot bzz already exists."))
	b.send("WHOIS Bzz")
	b.expect(" 311 Boss Bzz bot bot.host * :Busy Bee")

	bee.send("BS BOTLIST")
	bee.expect(bsLine("Bee", "Bot list:"))
	bee.expect("  Bzz ")
	bee.expect(" bots available.")

	// ASSIGN needs a registered channel and the ASSIGN privilege (founder).
	bee.send("JOIN #hive")
	bee.expect(" 366 Bee #hive ")
	bee.send("BS ASSIGN #hive Bzz")
	bee.expect(bsLine("Bee", "Channel #hive isn't registered."))
	bee.send("CS REGISTER #hive the hive")
	bee.expect("is registered to Bee")
	ann := registered(t, s, "Ann")
	ann.send("JOIN #hive")
	ann.expect(" 366 Ann #hive ")
	ann.send("BS ASSIGN #hive Bzz")
	ann.expect(bsLine("Ann", "Access denied."))
	bee.send("BS ASSIGN #hive Nope")
	bee.expect(bsLine("Bee", "Bot Nope does not exist."))
	bee.send("BS ASSIGN #hive Bzz")
	bee.expect(":Bzz!bot@bot.host JOIN #hive")
	bee.expect("MODE #hive +o Bzz")
	bee.expect(bsLine("Bee", "Bot Bzz has been assigned to #hive."))
	bee.send("BS ASSIGN #hive Bzz")
	bee.expect(bsLine("Bee", "Bot Bzz is already assigned to channel #hive."))

	// SAY and ACT.
	bee.send("BS SAY #hive hello hive")
	ann.expect(":Bzz!bot@bot.host PRIVMSG #hive :hello hive")
	bee.send("BS ACT #hive waves")
	ann.expect(":Bzz!bot@bot.host PRIVMSG #hive :\x01ACTION waves\x01")
	ann.send("BS SAY #hive hi")
	ann.expect(bsLine("Ann", "Access denied."))

	// Taking the bot's op is put back.
	bee.send("MODE #hive -o Bzz")
	bee.expect("MODE #hive -o Bzz")
	bee.expect("MODE #hive +o Bzz")

	// INFO and SET.
	bee.send("BS INFO Bzz")
	bee.expect(bsLine("Bee", "Information for bot Bzz:"))
	bee.expect("Mask: bot@bot.host")
	bee.expect("Used on: 1 channel(s)")
	bee.send("BS INFO #hive")
	bee.expect(bsLine("Bee", "Information for channel #hive:"))
	bee.expect("Bot nick: Bzz")
	bee.expect("Greet: Enabled")
	bee.send("BS SET #hive BANEXPIRE 2d")
	bee.expect(bsLine("Bee", "Ban expiry may not be longer than 1 day."))
	bee.send("BS SET #hive BANEXPIRE 30m")
	bee.expect(bsLine("Bee", "Bot bans will automatically expire after 30 minutes."))
	bee.send("BS SET #hive FANTASY OFF")
	bee.expect(bsLine("Bee", "Fantasy mode is now off on channel #hive."))
	bee.send("BS SET Bzz PRIVATE ON")
	bee.expect(bsLine("Bee", "Access denied."))
	b.send("BS SET Bzz PRIVATE ON")
	b.expect(bsLine("Boss", "Private mode of bot Bzz is now on."))
	ann.send("BS BOTLIST")
	ann.quiet("  * Bzz ")

	// Greets: NickServ SET GREET, shown by the bot to those with GREET.
	ann.send("NS SET GREET Buzz buzz!")
	ann.expect(nsLine("Ann", "Greet message for Ann changed to Buzz buzz!"))
	bee.send("CS ACCESS #hive ADD Ann 5")
	bee.expect("Ann")
	ann.send("PART #hive")
	ann.expect("PART #hive")
	ann.send("JOIN #hive")
	bee.expect(":Bzz!bot@bot.host PRIVMSG #hive :[Ann] Buzz buzz!")
	bee.send("NS INFO Ann")
	bee.expect("Greet         Buzz buzz!")

	// The bot leaves once nobody is left, and comes back with the first one.
	ann.send("PART #hive")
	ann.expect("PART #hive")
	bee.send("PART #hive")
	bee.expect("PART #hive")
	b.send("NAMES #hive")
	b.expect(" 366 Boss #hive ")
	bee.send("JOIN #hive")
	bee.expect(":Bzz!bot@bot.host JOIN #hive")

	// CHANGE the nick, then DEL.
	b.send("BS BOT CHANGE ChanServ x")
	b.expect(bsLine("Boss", "Bot ChanServ is not changeable."))
	b.send("BS BOT CHANGE Bzz Buzzy")
	bee.expect(":Bzz!bot@bot.host NICK Buzzy")
	b.expect(bsLine("Boss", "Bot Bzz has been changed to Buzzy!bot@bot.host (Busy Bee)."))
	bee.send("BS UNASSIGN #hive")
	bee.expect(":Buzzy!bot@bot.host PART #hive :UNASSIGN from Bee")
	bee.expect(bsLine("Bee", "There is no bot assigned to #hive anymore."))
	bee.send("BS SAY #hive hi")
	bee.expect(bsLine("Bee", "You must assign a bot to the channel before using this command."))
	b.send("BS BOT DEL ChanServ")
	b.expect(bsLine("Boss", "Bot ChanServ is not deletable."))
	b.send("BS BOT DEL Buzzy")
	b.expect(bsLine("Boss", "Bot Buzzy has been deleted."))
	b.send("WHOIS Buzzy")
	b.expect(" 401 Boss Buzzy ")
}

// Bots and channel settings survive a restart.
func TestBotServKeptOverRestart(t *testing.T) {
	s, _, _ := fileServer(t, "")
	db := s.cfg.Services.Database
	b := boss(t, s)
	bee := registered(t, s, "Bee")
	b.send("BS BOT ADD Bzz bot bot.host Busy Bee")
	b.expect("added to the bot list.")
	bee.send("JOIN #hive")
	bee.expect(" 366 Bee #hive ")
	bee.send("CS REGISTER #hive the hive")
	bee.expect("is registered to Bee")
	bee.send("BS ASSIGN #hive Bzz")
	bee.expect("has been assigned to #hive.")
	bee.send("BS SET #hive GREET OFF")
	bee.expect("Greet mode is now off on channel #hive.")

	bee.send("QUIT")
	bee.expect("ERROR")
	stopServer(s)
	s2 := testServer(t, func(c *config.Config) { c.Services.Database = db })
	c := login(t, s2, "Ann")
	c.send("WHOIS Bzz")
	c.expect(" 311 Ann Bzz bot bot.host * :Busy Bee")
	c.send("JOIN #hive")
	c.expect(":Bzz!bot@bot.host JOIN #hive")
	c.send("BS INFO Bzz")
	c.expect("Used on: 1 channel(s)")
}
