package server

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
)

const pw = "honeypot42"

func nsLine(nick, text string) string {
	return ":NickServ!services@irc.test.local NOTICE " + nick + " :" + text
}

func msLine(nick, text string) string {
	return ":MemoServ!services@irc.test.local NOTICE " + nick + " :" + text
}

// registered logs in nick and registers it.
func registered(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := login(t, s, nick)
	c.send("NS REGISTER " + pw)
	c.expect(nsLine(nick, nick+" is now registered to you."))
	c.expect(":irc.test.local MODE " + nick + " +r")
	return c
}

// identified logs in nick (already registered) and identifies.
func identified(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := login(t, s, nick)
	c.send("NS IDENTIFY " + pw)
	c.expect(nsLine(nick, "Password accepted - you are now recognized."))
	c.expect(" 900 " + nick + " ")
	return c
}

func TestRegisterAndIdentify(t *testing.T) {
	s := testServer(t, nil)
	bee := login(t, s, "Bee")
	bee.send("NS REGISTER short")
	bee.expect(nsLine("Bee", "Passwords need at least 6 characters."))
	bee.send("NS REGISTER bee123 not-an-email")
	bee.expect(nsLine("Bee", "not-an-email doesn't look like an email address. You can leave it out: /ns register <password>"))
	bee.send("PRIVMSG NickServ :REGISTER " + pw + " bee@example.org")
	bee.expect(nsLine("Bee", "Bee is now registered to you. Keep your password safe: you need it to identify."))
	bee.expect(":irc.test.local 900 Bee Bee!~bee@127.0.0.1 Bee :You are now logged in as Bee")
	bee.expect(":irc.test.local MODE Bee +r")
	bee.send("NS REGISTER " + pw)
	bee.expect(nsLine("Bee", "You're identified as Bee already. To add Bee to it: /ns group Bee"))

	other := login(t, s, "other")
	other.send("WHOIS Bee")
	other.expect(" 330 other Bee Bee :is logged in as")
	other.send("WHOIS NickServ")
	other.expect(" 313 other NickServ :is a Network Service")
	other.send("NS INFO Bee")
	other.expect(nsLine("other", "Bee · registered "))
	other.expect(nsLine("other", "  Last seen     online now"))
	other.expect(nsLine("other", "  Options       kill protection, security, private, auto-op, email hidden, usermask hidden"))
	other.quiet("bee@example.org")

	bee.send("QUIT")
	bee.expect("ERROR")

	again := login(t, s, "Bee")
	again.expect(nsLine("Bee", "If you do not change within 1 minute, I will change your nick."))
	again.send("NS IDENTIFY wrongpass")
	again.expect(nsLine("Bee", "Wrong password for Bee."))
	again.send("NICKSERV IDENTIFY " + pw)
	again.expect(nsLine("Bee", "Password accepted - you are now recognized."))
	again.expect(" 900 Bee ")
	again.send("NS INFO")
	again.expect(nsLine("Bee", "  Email         bee@example.org")) // the owner sees it
	again.send("NS LOGOUT")
	again.expect(" 901 Bee Bee!~bee@127.0.0.1 :You are now logged out")
	again.expect(":irc.test.local MODE Bee -r")
	again.expect(nsLine("Bee", "You are no longer identified as Bee."))

	again.send("NS HELP")
	again.expect(nsLine("Bee", "NickServ keeps your nick yours. Commands:"))
	again.expect(nsLine("Bee", "  REGISTER  register the nick you are using"))
	again.expect(nsLine("Bee", "More on one command: /ns help <command>"))
	again.send("NS HELP GHOST") // a hidden alias shows the command it stands for
	again.expect(nsLine("Bee", "Syntax: RECOVER <nick> [password]"))
	again.send("NS FLY")
	again.expect(nsLine("Bee", "Unknown command FLY. Type /ns help for a list."))
}

func TestEnforce(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Services.IdentifyWithin.Duration = 800 * time.Millisecond })
	bee := registered(t, s, "Bee")
	bee.send("QUIT")
	bee.expect("ERROR")

	friend := login(t, s, "friend")
	joinAll("#a", friend)
	impostor := login(t, s, "Bee")
	impostor.expect(nsLine("Bee", "If you do not change within 0 seconds, I will change your nick."))
	joinAll("#a", impostor)
	l := impostor.expect(" NICK Guest")
	guest := l[strings.LastIndex(l, " ")+1:]
	if !regexp.MustCompile(`^:?Guest\d{5}$`).MatchString(guest) {
		t.Fatalf("not a guest nick: %s", l)
	}
	impostor.expect("You didn't identify, so your nick was changed to Guest")
	friend.expect(":Bee!~bee@127.0.0.1 NICK Guest")

	// Changing to a registered nick later is enforced too; identifying stops it.
	impostor.send("NICK Bee")
	impostor.expect("If you do not change within")
	impostor.send("NS IDENTIFY " + pw)
	impostor.expect(" 900 Bee ")
	time.Sleep(time.Second)
	impostor.send("WHOIS Bee")
	impostor.expect(" 311 Bee Bee ")
}

func TestGhostRecoverGroupUngroup(t *testing.T) {
	s := testServer(t, nil)
	old := registered(t, s, "Bee")
	me := login(t, s, "Bee_")
	// GHOST is Anope's hidden alias for RECOVER: it takes the nick too.
	me.send("NS GHOST Bee")
	me.expect(nsLine("Bee_", "Give the password of Bee: /ns recover Bee <password>"))
	me.send("NS HELP")
	me.quiet("GHOST")
	me.send("NS GHOST Bee " + pw)
	old.expect("ERROR :Closing Link: 127.0.0.1 (Killed (NickServ (RECOVER command used by Bee_)))")
	me.expect(":Bee_!~bee_@127.0.0.1 NICK Bee")
	me.expect(nsLine("Bee", "You now have Bee."))
	me.send("NICK Bee_")
	me.expect("NICK Bee_")

	back := login(t, s, "Bee")
	back.send("NS IDENTIFY " + pw)
	back.expect(" 900 Bee ")
	x := login(t, s, "x")
	x.send("NS RELEASE Bee " + pw)
	back.expect("Killed (NickServ (RECOVER command used by x))")
	x.expect(":x!~x@127.0.0.1 NICK Bee")
	x.expect(nsLine("Bee", "You now have Bee."))

	me.send("NS GROUP Bee")
	me.expect(nsLine("Bee_", "Bee_ is now part of the account Bee.")) // identified already, by RECOVER
	x.send("NS INFO Bee_")
	x.expect(nsLine("Bee", "  Account       Bee"))
	x.expect(nsLine("Bee", "  Also uses     Bee"))
	// UNGROUP makes the nick an account of its own, with the same password
	// (Anope 2.0.20).
	x.send("NS UNGROUP Bee_")
	x.expect(nsLine("Bee", "Bee_ has been ungrouped from Bee. It is an account of its own now, with the same password."))
	x.send("NS INFO Bee_")
	x.expect(nsLine("Bee", "Bee_ · registered "))
	x.quiet("  Account       ")
	x.send("NS UNGROUP")
	x.expect(nsLine("Bee", "Your nick isn't grouped with any other, so you can't ungroup it."))
	me.send("NS IDENTIFY Bee_ " + pw)
	me.expect(nsLine("Bee_", "Password accepted - you are now recognized."))
	// Ungrouping the account's name: the account takes its next nick's name.
	me.send("NICK Bee_two")
	me.expect("NICK Bee_two")
	me.send("NS GROUP Bee_")
	me.expect("Bee_two is now part of the account Bee_.")
	me.send("NS UNGROUP Bee_")
	me.expect(" 900 Bee_two ")
	me.expect(nsLine("Bee_two", "Bee_ has been ungrouped from Bee_two. It is an account of its own now, with the same password."))
}

func TestLockout(t *testing.T) {
	s := testServer(t, nil)
	registered(t, s, "Bee")
	x := login(t, s, "x")
	for i := 0; i < 5; i++ {
		x.send("NS IDENTIFY Bee nope" + strings.Repeat("!", i))
		x.expect(nsLine("x", "Wrong password for Bee."))
	}
	x.send("NS IDENTIFY Bee " + pw)
	x.expect(nsLine("x", "Too many wrong passwords for Bee. Try again in 10 minutes."))
}

func TestSetAndDrop(t *testing.T) {
	s := testServer(t, nil)
	bee := registered(t, s, "Bee")
	bee.send("JOIN #hive")
	bee.expect(" 366 ")
	bee.send("CS REGISTER #hive bees")
	bee.expect("You are its Founder")
	bee.send("NS ALIST")
	bee.expect(nsLine("Bee", "Channels where Bee has access:"))
	bee.expect(nsLine("Bee", "  #hive                Founder"))
	bee.expect(nsLine("Bee", "1 channel."))

	bee.send("NS SET KILL 30")
	bee.expect(nsLine("Bee", "Syntax: SET KILL {ON | QUICK | IMMED | OFF}"))
	bee.send("NS SET KILL QUICK")
	bee.expect(nsLine("Bee", "Kill protection is on for Bee, quick: people who don't identify within 20 seconds get renamed."))
	bee.send("NS SET KILL IMMED")
	bee.expect(nsLine("Bee", "The IMMED option is not available on this network."))
	bee.send("NS SET PROTECT ON") // Anope 2.1's name still works, hidden
	bee.expect(nsLine("Bee", "Kill protection is on for Bee: people who don't identify within 60 seconds get renamed."))
	bee.send("NS SET ENFORCE OFF") // and our old one
	bee.expect(nsLine("Bee", "Kill protection is off for Bee: people using the nick are only told it is registered."))
	bee.send("NS SET HIDE EMAIL OFF")
	bee.expect(nsLine("Bee", "Your email is shown to others in INFO."))
	bee.send("NS SET HIDE USERMASK OFF")
	bee.expect(nsLine("Bee", "Your last user@host is shown to others in INFO."))
	bee.send("NS SET HIDE QUIT ON")
	bee.expect(nsLine("Bee", "Your last quit message is hidden from others in INFO."))
	bee.send("NS SET PASSWORD newhoney99")
	bee.expect(nsLine("Bee", "Your password is changed."))
	bee.send("QUIT :off to the meadow")
	bee.expect("ERROR")

	again := login(t, s, "Bee")
	again.expect(nsLine("Bee", "This nickname is registered and protected.  If it is your"))
	again.quiet("identified as Bee because")
	other := login(t, s, "x")
	other.send("NS INFO Bee")
	other.expect(nsLine("x", "  Last host     ~bee@127.0.0.1"))
	other.quiet("off to the meadow")
	other.send("NICK Bee_")
	other.send("NS IDENTIFY Bee newhoney99")
	other.expect(nsLine("Bee_", "Password accepted - you are now recognized."))
	other.send("NS INFO Bee")
	other.expect(nsLine("Bee_", "  Options       security, private, auto-op, quit hidden"))
	// Shown only while nobody uses the account.
	other.send("NS SET HIDE QUIT OFF")
	other.expect("Your last quit message is shown to others in INFO.")
	other.send("NS LOGOUT")
	other.expect(" 901 Bee_ ")
	again.send("NS INFO Bee")
	again.expect(nsLine("Bee", "  Last quit     Quit: off to the meadow"))
	other.send("NS IDENTIFY Bee newhoney99")
	other.expect(" 900 Bee_ ")

	// DROP takes one nick at a time (Anope 2.0.20): a grouped nick goes,
	// the account stays.
	other.send("NS GROUP Bee")
	other.expect("Bee_ is now part of the account Bee.")
	other.send("NS DROP Bee_")
	other.expect(nsLine("Bee_", "Bee_ has been dropped."))
	other.send("NS INFO Bee_")
	other.expect(nsLine("Bee_", "Bee_ isn't registered."))

	// Dropping the account's name moves the name to the next nick.
	other.send("NICK Bee_")
	other.send("NS GROUP Bee")
	other.expect("Bee_ is now part of the account Bee.")
	other.send("NS DROP")
	other.expect(nsLine("Bee_", "Syntax: DROP <nick>"))
	other.send("NS DROP Bee")
	other.expect(" 900 Bee_ ")
	other.expect(nsLine("Bee_", "Bee has been dropped. The account is now called Bee_."))
	other.send("NS INFO Bee")
	other.expect(nsLine("Bee_", "Bee isn't registered."))

	// The last nick takes the account with it.
	other.send("NS DROP Bee_")
	other.expect(" 901 Bee_ ")
	other.expect(nsLine("Bee_", "Bee_ has been dropped."))
	other.send("NS INFO Bee_")
	other.expect(nsLine("Bee_", "Bee_ isn't registered."))
}

func TestSASL(t *testing.T) {
	s := testServer(t, nil)
	gone := registered(t, s, "Bee")
	gone.send("QUIT")
	gone.expect("ERROR :Closing Link") // nick free again

	plain := func(user, pass string) string {
		return base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
	}
	c := raw(t, s)
	c.nick = "Bee"
	c.send("CAP LS 302")
	c.expect("sasl=PLAIN")
	c.send("CAP REQ :sasl account-notify")
	c.expect("CAP * ACK :sasl account-notify")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("AUTHENTICATE SCRAM-SHA-256")
	c.expect(" 908 Bee PLAIN,EXTERNAL :are available SASL mechanisms")
	c.expect(" 904 Bee :SASL authentication failed")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + plain("Bee", "wrong-one"))
	c.expect(" 904 Bee :SASL authentication failed")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE *")
	c.expect(" 906 Bee :SASL authentication aborted")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + plain("Bee", pw))
	c.expect(" 900 Bee Bee!~bee@127.0.0.1 Bee :You are now logged in as Bee")
	c.expect(" 903 Bee :SASL authentication successful")
	c.send("AUTHENTICATE PLAIN")
	c.expect(" 907 Bee :You have already authenticated using SASL")
	c.send("CAP END")
	c.expect(":Bee MODE Bee +ir")

	// With cloaks on, the early "logged in" line already shows the cloak.
	on := true
	cs := testServer(t, func(c *config.Config) { c.Cloak.Enabled = &on })
	old := registered(t, cs, "Cee")
	old.send("QUIT")
	old.expect("ERROR :Closing Link") // the nick is free before Cee comes back
	k := raw(t, cs)
	k.nick = "Cee"
	k.send("CAP REQ :sasl")
	k.send("NICK Cee")
	k.send("USER cee 0 * :Cee")
	k.send("AUTHENTICATE PLAIN")
	k.expect("AUTHENTICATE +")
	k.send("AUTHENTICATE " + plain("Cee", pw))
	if l := k.expect(" 900 Cee "); !strings.Contains(l, "@beenet-") {
		t.Errorf("900 shows the real host: %s", l)
	}
	c.quiet("registered and protected") // no nag: already identified
	c.send("WHOIS Bee")
	c.expect(" 330 Bee Bee Bee :is logged in as")
}

func TestMemoServ(t *testing.T) {
	s := testServer(t, nil)
	bee := registered(t, s, "Bee")
	ember := registered(t, s, "ember")
	nova := registered(t, s, "nova")
	nova.send("QUIT")
	nova.expect("ERROR")

	stranger := login(t, s, "stranger")
	stranger.send("MS SEND ember hi")
	stranger.expect(msLine("stranger", "Identify first: /ns identify <password>"))

	bee.send("MS SEND ember Meeting on Friday at 21:00")
	ember.expect(msLine("ember", "You have a new memo from Bee. Read it: /ms read 1"))
	bee.expect(msLine("Bee", "Memo sent to ember. They're online, so they were told right away."))
	ember.send("MS LIST")
	ember.expect(msLine("ember", "  #   From  Sent"))
	ember.expect(regexp.QuoteMeta("  1   Bee   ") + "")
	ember.expect(msLine("ember", "1 memo, 1 new. Read one: /ms read <num>"))
	ember.send("MS READ 1")
	ember.expect(msLine("ember", "Memo 1 from Bee · "))
	ember.expect(msLine("ember", "  Meeting on Friday at 21:00"))
	ember.send("MS READ NEW")
	ember.expect(msLine("ember", "You have no new memos."))
	ember.send("MS DEL READ")
	ember.expect(msLine("ember", "1 memo you had read deleted."))

	bee.send("MS SEND nova leaf2 is ready")
	bee.expect(msLine("Bee", "Memo sent to nova. They'll see it the next time they identify."))
	n := identified(t, s, "nova")
	n.expect(msLine("nova", "You have 1 new memo. Read it: /ms read 1"))

	ember.send("MS IGNORE ADD Bee")
	ember.expect(msLine("ember", "Memos from Bee are now dropped."))
	bee.send("MS SEND ember are you there?")
	bee.expect(msLine("Bee", "Memo sent to ember."))
	ember.quiet("new memo")
	ember.send("MS LIST")
	ember.expect(msLine("ember", "You have no memos."))

	bee.send("MS SEND nobody hello")
	bee.expect(msLine("Bee", "nobody isn't a registered nick."))
	bee.send("MS SEND #bchat hello")
	bee.expect(msLine("Bee", "#bchat isn't registered with ChanServ."))
	bee.send("MS SET LIMIT 99")
	bee.expect(msLine("Bee", "Syntax: SET LIMIT [#chan] <n>, from 1 to 50"))
	bee.send("MS HELP")
	bee.expect(msLine("Bee", "MemoServ keeps messages for people who are away. Commands:"))
}

func TestAccountNotifyAndTag(t *testing.T) {
	s := testServer(t, nil)
	watcher := capLogin(t, s, "watcher", "account-notify", "account-tag", "extended-join", "message-tags")
	bee := login(t, s, "Bee")
	joinAll("#a", watcher, bee)
	bee.send("NS REGISTER " + pw)
	watcher.expect(":Bee!~bee@127.0.0.1 ACCOUNT Bee")
	bee.send("PRIVMSG #a :hello")
	l := watcher.expect("PRIVMSG #a :hello")
	if !strings.Contains(l, "account=Bee") {
		t.Errorf("no account tag: %s", l)
	}
	bee.send("PART #a")
	bee.send("JOIN #a")
	watcher.expect(":Bee!~bee@127.0.0.1 JOIN #a Bee :Bee Real")
	bee.send("NS LOGOUT")
	watcher.expect(":Bee!~bee@127.0.0.1 ACCOUNT *")
}

func TestReservedAndServiceNicks(t *testing.T) {
	s := testServer(t, nil)
	c := login(t, s, "Bee")
	c.send("NICK ChanServ")
	c.expect(" 432 Bee ChanServ :Reserved for services")
	c.send("NICK nickserv")
	c.expect(" 432 Bee nickserv :Reserved for services")
	c.send("LUSERS")
	c.expect(" 251 Bee :There are 0 users and 1 invisible on 1 servers")
}

func TestServicesDataSurvivesRestartAndExpires(t *testing.T) {
	db := filepath.Join(t.TempDir(), "services.db")
	useDB := func(c *config.Config) { c.Services.Database = db }
	s1 := testServer(t, useDB)
	bee := registered(t, s1, "Bee")
	bee.send("QUIT")
	bee.expect("ERROR")
	stopServer(s1) // one server per database
	// Make an account nobody used for 100 days, before the next start.
	s2 := testServerWith(t, func(c *config.Config) {
		c.Services.Database = db
		c.Listen = []config.Listen{{Address: "127.0.0.1:0"}, {Address: "127.0.0.1:0", TLS: true}}
	}, func(s *Server) {
		h, _ := passwd.Hash(pw)
		old := time.Now().Add(-100 * 24 * time.Hour)
		if _, err := s.store.CreateAccount("Dusty", h, "", old); err != nil {
			t.Fatal(err)
		}
	})
	c := identified(t, s2, "Bee") // still registered after a restart
	c.send("NS INFO Dusty")
	c.expect(nsLine("Bee", "Dusty isn't registered.")) // expired at start
}

func TestMemoRanges(t *testing.T) {
	s := testServer(t, nil)
	bee := registered(t, s, "Bee")
	ember := registered(t, s, "ember")
	for i := 1; i <= 5; i++ {
		ember.send(fmt.Sprintf("MS SEND Bee note %d", i))
		ember.expect("Memo sent to Bee.")
	}
	bee.send("MS READ 2-3")
	bee.expect(msLine("Bee", "Memo 2 from ember"))
	bee.expect(msLine("Bee", "  note 2"))
	bee.expect(msLine("Bee", "Memo 3 from ember"))
	bee.send("MS LIST NEW")
	bee.expect(msLine("Bee", "  1   ember"))
	bee.expect(msLine("Bee", "  4   ember"))
	bee.expect(msLine("Bee", "5 memos, 3 new. Read one: /ms read <num>"))
	bee.send("MS DEL 1,5")
	bee.expect(msLine("Bee", "2 memos deleted. The ones after them moved up."))
	bee.send("MS DEL READ")
	bee.expect(msLine("Bee", "2 memos you had read deleted."))
	bee.send("MS READ ALL")
	bee.expect(msLine("Bee", "  note 4"))
	bee.send("MS DEL LAST")
	bee.expect(msLine("Bee", "Memo 1 deleted. The ones after it moved up."))
	bee.send("MS READ 7")
	bee.expect(msLine("Bee", "You have no memos."))
	bee.send("MS DEL NEW")
	bee.expect(msLine("Bee", "Syntax: DEL [#chan] {<num> | <list> | LAST | READ | ALL}"))
}

func TestHelpLayout(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	x := registered(t, s, "x")

	// Everyday commands one line each, the rest by group; @+ hides ~&% ranks.
	x.send("CS HELP")
	x.expect(csLine("x", "ChanServ keeps a channel yours. Commands:"))
	x.expect(csLine("x", "  REGISTER  register a channel you op in"))
	x.expect(csLine("x", "Access lists"))
	l := x.expect(csLine("x", "  ACCESS, LEVELS, SOP, AOP"))
	if strings.Contains(l, "QOP") || strings.Contains(l, "HOP") {
		t.Errorf("QOP/HOP shown on an @+ server: %q", l)
	}
	x.expect(csLine("x", "Channel status"))
	l = x.expect(csLine("x", "  OP, DEOP"))
	if strings.Contains(l, "OWNER") || strings.Contains(l, "HALFOP") {
		t.Errorf("owner/halfop shown on an @+ server: %q", l)
	}
	x.expect(csLine("x", "Every command with a line each: /cs help all"))
	x.send("CS HELP ALL")
	x.expect(csLine("x", "All commands of ChanServ:"))
	x.expect(csLine("x", "  AKICK "))
	x.send("CS HELP OWNER")
	x.expect(csLine("x", "No help for OWNER. Type /cs help for a list."))

	// Oper-only commands are hidden from people who can't use them.
	x.send("NS HELP")
	x.expect(nsLine("x", "  RECOVER "))
	x.quiet("SASET")
	b.send("NS HELP")
	b.expect(nsLine("Boss", "Commands for IRC operators"))
	b.expect(nsLine("Boss", "  SASET, SUSPEND, UNSUSPEND, GETEMAIL"))

	b.send("AS OPER x ADD FLAGS g HOST *@127.0.0.1 PASSWORD Xoper-pass-1")
	b.expect(asLine("Boss", "x is an oper now: g (global)"))
	x.send("OPER x Xoper-pass-1")
	x.expect(" 381 x ")
	x.send("OS HELP")
	x.expect(osLine("x", "  GLOBAL      a notice to everyone"))
	x.expect(osLine("x", "  LOGONNEWS   lines shown after the MOTD"))
	x.expect(osLine("x", "  OPERNEWS    lines shown to opers when they oper up"))
	x.quiet("FORBID")
	x.send("OS HELP AKILL")
	x.expect(osLine("x", "No help for AKILL. Type /os help for a list."))
}

// HIDE STATUS for someone who isn't an oper says so (QA 1.0.0 round, bug 4).
func TestNickServHideStatusNonOper(t *testing.T) {
	s := testServer(t, nil)
	c := registered(t, s, "plain")
	c.send("NS SET HIDE STATUS ON")
	c.expect("plain has no oper entry now, so INFO shows no oper status anyway; this applies if it gets one.")
}
