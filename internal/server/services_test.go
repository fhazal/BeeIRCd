package server

import (
	"encoding/base64"
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
	c.expect(nsLine(nick, "Password accepted. You are identified as "+nick+"."))
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
	bee.expect(nsLine("Bee", "You're identified as Bee already. To add Bee to it: /ns group Bee <password>"))

	other := login(t, s, "other")
	other.send("WHOIS Bee")
	other.expect(" 330 other Bee Bee :is logged in as")
	other.send("WHOIS NickServ")
	other.expect(" 313 other NickServ :is a Network Service")
	other.send("NS INFO Bee")
	other.expect(nsLine("other", "Bee · registered "))
	other.expect(nsLine("other", "  Last seen     online now"))
	other.expect(nsLine("other", "  Options       enforce, email hidden, host hidden"))
	other.quiet("bee@example.org")

	bee.send("QUIT")
	bee.expect("ERROR")

	again := login(t, s, "Bee")
	again.expect(nsLine("Bee", "Bee is a registered nick. Identify within 60 seconds: /ns identify <password>"))
	again.send("NS IDENTIFY wrongpass")
	again.expect(nsLine("Bee", "Wrong password for Bee."))
	again.send("NICKSERV IDENTIFY " + pw)
	again.expect(nsLine("Bee", "Password accepted. You are identified as Bee."))
	again.expect(" 900 Bee ")
	again.send("NS INFO")
	again.expect(nsLine("Bee", "  Email         bee@example.org")) // the owner sees it
	again.send("NS LOGOUT")
	again.expect(" 901 Bee Bee!~bee@127.0.0.1 :You are now logged out")
	again.expect(":irc.test.local MODE Bee -r")
	again.expect(nsLine("Bee", "You are no longer identified as Bee."))

	again.send("NS HELP")
	again.expect(nsLine("Bee", "NickServ keeps your nick yours. Commands:"))
	again.expect("REGISTER <password> [email]")
	again.expect(nsLine("Bee", "More on one command: /ns help <command>"))
	again.send("NS HELP GHOST")
	again.expect(nsLine("Bee", "Syntax: GHOST <nick> [password]"))
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
	impostor.expect(nsLine("Bee", "Bee is a registered nick. Identify within 0 seconds"))
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
	impostor.expect("Identify within")
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
	me.send("NS GHOST Bee")
	me.expect(nsLine("Bee_", "Give the password of Bee: /ns ghost Bee <password>"))
	me.send("NS GHOST Bee " + pw)
	old.expect("ERROR :Closing Link: 127.0.0.1 (Killed (NickServ (GHOST command used by Bee_)))")
	me.expect(nsLine("Bee_", "Bee was disconnected. The nick is free."))

	back := identified(t, s, "Bee")
	x := login(t, s, "x")
	x.send("NS RECOVER Bee " + pw)
	back.expect("Killed (NickServ (GHOST command used by x))")
	x.expect(" 900 x ")
	x.expect(":x!~x@127.0.0.1 NICK Bee")
	x.expect(nsLine("Bee", "You now have Bee."))

	me.send("NS GROUP Bee " + pw)
	me.expect(nsLine("Bee_", "Bee_ is now part of the account Bee."))
	me.expect(" 900 Bee_ ")
	x.send("NS INFO Bee_")
	x.expect(nsLine("Bee", "  Account       Bee"))
	x.expect(nsLine("Bee", "  Also uses     Bee"))
	x.send("NS UNGROUP Bee")
	x.expect(nsLine("Bee", "Bee is the account's own name and can't be ungrouped. To remove everything: /ns drop"))
	x.send("NS UNGROUP Bee_")
	x.expect(nsLine("Bee", "Bee_ is no longer part of Bee."))
	x.send("NS INFO Bee_")
	x.expect(nsLine("Bee", "Bee_ isn't registered."))
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

func TestAccessSetAndDrop(t *testing.T) {
	s := testServer(t, nil)
	bee := registered(t, s, "Bee")
	bee.send("NS ACCESS ADD nonsense")
	bee.expect(nsLine("Bee", "Masks look like ~bee@*.example.org."))
	bee.send("NS ACCESS ADD ~bee@127.0.0.*")
	bee.expect(nsLine("Bee", "Added ~bee@127.0.0.*. Connections from there are identified automatically."))
	bee.send("NS ACCESS LIST")
	bee.expect(nsLine("Bee", "  1  ~bee@127.0.0.*"))
	bee.send("NS SET ENFORCE OFF")
	bee.expect(nsLine("Bee", "Enforce is off: people using your nicks are only told they are registered."))
	bee.send("NS SET HIDE EMAIL OFF")
	bee.expect(nsLine("Bee", "Your email is shown to others in INFO."))
	bee.send("NS SET PASSWORD newhoney99")
	bee.expect(nsLine("Bee", "Your password is changed."))
	bee.send("QUIT")
	bee.expect("ERROR")

	auto := login(t, s, "Bee")
	auto.expect(nsLine("Bee", "You were identified as Bee because your host is on its access list."))
	other := login(t, s, "x") // different ident: not on the list
	other.send("NICK Bee_")
	other.send("NS IDENTIFY Bee newhoney99")
	other.expect(nsLine("Bee_", "Password accepted. You are identified as Bee."))

	auto.send("NS DROP")
	auto.expect(nsLine("Bee", "This removes the account Bee with its nicks (Bee) and all its memos, for good."))
	l := auto.expect(nsLine("Bee", "To go ahead within 2 minutes: /ns drop "))
	if first := l[strings.LastIndex(l, " ")+1:]; len(first) != 6 {
		t.Errorf("drop code %q should be 6 characters", first)
	}
	auto.send("NS DROP wrongcode")
	auto.expect(nsLine("Bee", "That code is wrong or too old."))
	l = auto.expect("To go ahead within 2 minutes: /ns drop ")
	code := l[strings.LastIndex(l, " ")+1:]
	auto.send("NS DROP " + code)
	auto.expect(nsLine("Bee", "The account Bee is gone."))
	other.expect(" 901 Bee_ ") // the other session was logged out too
	other.send("NS INFO Bee")
	other.expect(nsLine("Bee_", "Bee isn't registered."))
}

func TestSASL(t *testing.T) {
	s := testServer(t, nil)
	registered(t, s, "Bee").send("QUIT")

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
	c.expect(" 908 Bee PLAIN :are available SASL mechanisms")
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
	registered(t, cs, "Cee").send("QUIT")
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
	c.quiet("is a registered nick") // no nag: already identified
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
	bee.expect(msLine("Bee", "Syntax: SET LIMIT <n>, from 1 to 50"))
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
