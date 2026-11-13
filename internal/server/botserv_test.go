package server

import (
	"strings"
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

// Kickers and BADWORDS (Anope 2.0.20 bs_kick, bs_badwords).
func TestBotServKickers(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("BS BOT ADD Bzz bot bot.host Busy Bee")
	b.expect("added to the bot list.")
	bee := registered(t, s, "Bee")
	bee.send("JOIN #hive")
	bee.expect(" 366 Bee #hive ")
	bee.send("CS REGISTER #hive the hive")
	bee.expect("is registered to Bee")
	ann := registered(t, s, "Ann")
	rejoin := func() {
		ann.send("JOIN #hive")
		ann.expect(" 366 Ann #hive ")
	}
	rejoin()

	bee.send("BS KICK CAPS #hive ON")
	bee.expect(bsLine("Bee", "You must assign a bot to the channel before using this command."))
	bee.send("BS ASSIGN #hive Bzz")
	bee.expect("has been assigned to #hive.")
	ann.send("BS KICK CAPS #hive ON")
	ann.expect(bsLine("Ann", "Access denied."))

	// Caps: Anope's defaults, 10 capitals and 25%.
	bee.send("BS KICK CAPS #hive ON")
	bee.expect(bsLine("Bee", "Bot will now kick for caps (they must constitute at least 10 characters and 25% of the entire message)."))
	ann.send("PRIVMSG #hive :Hello THERE")
	ann.send("PRIVMSG #hive :WHY IS EVERYONE QUIET")
	bee.expect(":Ann!")
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Turn caps lock OFF!")
	rejoin()
	// The founder (an op) is kicked too until DONTKICKOPS; NOKICK never is.
	bee.send("BS SET #hive DONTKICKOPS ON")
	bee.expect("Bot won't kick ops on channel #hive.")
	bee.send("PRIVMSG #hive :I AM THE FOUNDER HERE")
	bee.send("BS KICK CAPS #hive OFF")
	bee.expect(bsLine("Bee", "Bot won't kick for caps anymore."))

	// Bad words, with a ban after 2 kicks that lifts itself (BANEXPIRE).
	bee.send("BS BADWORDS #hive LIST")
	bee.expect(bsLine("Bee", "#hive bad words list is empty."))
	bee.send("BS BADWORDS #hive ADD wasp")
	bee.expect(bsLine("Bee", "wasp added to #hive bad words list."))
	bee.send("BS BADWORDS #hive ADD Wasp")
	bee.expect(bsLine("Bee", "wasp already exists in #hive bad words list."))
	bee.send("BS BADWORDS #hive ADD hornet SINGLE")
	bee.expect(bsLine("Bee", "hornet added to #hive bad words list."))
	bee.send("BS BADWORDS #hive LIST")
	bee.expect(bsLine("Bee", "Bad words list for #hive:"))
	bee.expect("  1       wasp")
	bee.expect("  2       hornet  (SINGLE)")
	bee.expect(bsLine("Bee", "End of bad words list."))
	bee.send("BS SET #hive BANEXPIRE 30m")
	bee.expect("expire after 30 minutes.")
	bee.send("BS KICK BADWORDS #hive ON 2")
	bee.expect(bsLine("Bee", "Bot will now kick for badwords, and will place a ban after 2 kicks for the same user."))
	ann.send("PRIVMSG #hive :hornets are fine")
	ann.send("PRIVMSG #hive :a \x02WAS\x02P!")
	bee.expect("hornets are fine")
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Watch your language!")
	rejoin()
	ann.send("PRIVMSG #hive :that hornet again")
	bee.expect(":Bzz!bot@bot.host MODE #hive +b ~t:30:*!*@")
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Watch your language!")
	bee.send("MODE #hive -b " + "~t:30:*!*@127.0.0.1")
	bee.expect("MODE #hive -b")
	bee.send("BS BADWORDS #hive DEL 1-2")
	bee.expect(bsLine("Bee", "Deleted 2 entries from #hive bad words list."))
	bee.send("BS KICK BADWORDS #hive OFF")
	bee.expect("Bot won't kick for badwords anymore.")
	rejoin()

	// Colours, repeat and flood.
	bee.send("BS KICK COLORS #hive ON")
	bee.expect("Bot will now kick for colors.")
	ann.send("PRIVMSG #hive :\x0304red")
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Don't use colors on this channel!")
	rejoin()
	bee.send("BS KICK REPEAT #hive ON 0 2")
	bee.expect("Bot will now kick for repeats (users that repeat the same message 2 times).")
	for i := 0; i < 3; i++ {
		ann.send("PRIVMSG #hive :buzz")
	}
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Stop repeating yourself!")
	rejoin()
	bee.send("BS KICK REPEAT #hive OFF")
	bee.expect("anymore.")
	bee.send("BS KICK FLOOD #hive ON 0 3 10")
	bee.expect("Bot will now kick for flood (3 lines in 10 seconds).")
	for _, l := range []string{"one", "two", "three"} {
		ann.send("PRIVMSG #hive :" + l)
	}
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Stop flooding!")
	rejoin()

	// INFO shows them.
	bee.send("BS INFO #hive")
	bee.expect("Don't kick ops: Enabled")
	bee.expect("Bad words kicker: Disabled")
	bee.expect("    Colors kicker: Enabled")
	bee.expect("     Flood kicker: Enabled (3 lines in 10s)")
	bee.expect("    Repeat kicker: Disabled")

	// AMSG: the same line in two channels with the kicker on.
	bee.send("BS KICK AMSG #hive ON")
	bee.expect("Bot will now kick for AMSG.")
	bee.send("BS KICK FLOOD #hive OFF")
	bee.expect("anymore.")
	ann.send("JOIN #comb")
	ann.expect(" 366 Ann #comb ")
	ann.send("PRIVMSG #comb :hello all")
	ann.send("PRIVMSG #hive :hello all")
	bee.expect(":Bzz!bot@bot.host KICK #hive Ann :Don't use AMSGs!")
}

// Fantasy commands: !op, "Bzz deop", replies from the bot, FANTASIA.
func TestBotServFantasy(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("BS BOT ADD Bzz bot bot.host Busy Bee")
	b.expect("added to the bot list.")
	bee := registered(t, s, "Bee")
	bee.send("JOIN #hive")
	bee.expect(" 366 Bee #hive ")
	bee.send("CS REGISTER #hive the hive")
	bee.expect("is registered to Bee")
	bee.send("BS ASSIGN #hive Bzz")
	bee.expect("has been assigned to #hive.")
	ann := registered(t, s, "Ann")
	ann.send("JOIN #hive")
	ann.expect(" 366 Ann #hive ")

	// No FANTASIA: nothing happens.
	ann.send("PRIVMSG #hive :!op Ann")
	ann.send("PRIVMSG #hive :marker one")
	bee.expect("!op Ann")
	bee.expect("marker one")

	// The founder: !op and "<bot> deop"; the bot sets the modes.
	bee.send("PRIVMSG #hive :!op Ann")
	ann.expect(":Bzz!bot@bot.host MODE #hive +o Ann")
	bee.send("PRIVMSG #hive :Bzz: deop Ann")
	ann.expect(":Bzz!bot@bot.host MODE #hive -o Ann")
	bee.send("PRIVMSG #hive :!help")
	bee.expect(":Bzz!bot@bot.host NOTICE Bee :Fantasy commands")

	// FANTASIA (3) lets Ann use them; ChanServ's answers come from the bot.
	bee.send("CS ACCESS #hive ADD Ann 3")
	bee.expect("Ann")
	ann.send("PRIVMSG #hive :!op Bee")
	ann.expect(":Bzz!bot@bot.host NOTICE Ann :")
	ann.send("PRIVMSG #hive :!k Bee")
	ann.expect(":Bzz!bot@bot.host NOTICE Ann :")
	bee.send("PRIVMSG #hive :!kick Ann bye now")
	ann.expect(":Bzz!bot@bot.host KICK #hive Ann :")
	ann.send("JOIN #hive")
	ann.expect(" 366 Ann #hive ")

	// SET FANTASY OFF: plain text again.
	bee.send("BS SET #hive FANTASY OFF")
	bee.expect("Fantasy mode is now off on channel #hive.")
	bee.send("PRIVMSG #hive :!op Ann")
	bee.send("PRIVMSG #hive :marker two")
	ann.expect("!op Ann")
	ann.expect("marker two")
}

// Bug sweep: bot nick prefix, BOT DEL on a PERSIST channel, DROP with a
// service bot assigned.
func TestBotServSweep(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("BS BOT ADD Buzz bot bot.host Busy Bee")
	b.expect("added to the bot list.")
	bee := registered(t, s, "Bee")
	bee.send("JOIN #hive")
	bee.expect(" 366 Bee #hive ")
	bee.send("CS REGISTER #hive the hive")
	bee.expect("is registered to Bee")
	bee.send("BS ASSIGN #hive Buzz")
	bee.expect("has been assigned to #hive.")
	ann := registered(t, s, "Ann")
	ann.send("JOIN #hive")
	ann.expect(" 366 Ann #hive ")

	// Only the bot's own nick (with : or ,) starts a command.
	bee.send("PRIVMSG #hive :Buzzing op Ann")
	bee.send("PRIVMSG #hive :Buzz, voice Ann")
	if l := ann.expect("MODE #hive"); !strings.HasSuffix(l, "MODE #hive +v Ann") {
		t.Errorf("first mode = %q, want the +v (Buzzing must not run OP)", l)
	}

	// PERSIST held by the bot; deleting the bot hands it to ChanServ.
	bee.send("CS SET PERSIST #hive ON")
	bee.expect("Persist is now on")
	ann.send("PART #hive")
	ann.expect("PART #hive")
	bee.send("PART #hive")
	bee.expect("PART #hive")
	b.send("BS BOT DEL Buzz")
	b.expect(bsLine("Boss", "Bot Buzz has been deleted."))
	b.send("NAMES #hive")
	if l := b.expect(" 353 Boss = #hive :"); !strings.Contains(l, "ChanServ") {
		t.Errorf("ChanServ isn't keeping #hive open: %s", l)
	}

	// A service bot leaves with the registration.
	bee.send("JOIN #hive")
	bee.expect(" 366 Bee #hive ")
	bee.send("CS SET PERSIST #hive OFF")
	bee.expect("Persist is now off")
	bee.send("BS ASSIGN #hive OperServ")
	bee.expect(":OperServ!")
	bee.send("CS DROP #hive #hive")
	bee.expect("#hive is no longer registered.")
	b.send("NAMES #hive")
	if l := b.expect(" 366 Boss #hive "); strings.Contains(l, "OperServ") {
		t.Error(l)
	}
	bee.send("NAMES #hive")
	if l := bee.expect(" 353 Bee "); strings.Contains(l, "OperServ") {
		t.Errorf("OperServ stayed after DROP: %s", l)
	}
}
