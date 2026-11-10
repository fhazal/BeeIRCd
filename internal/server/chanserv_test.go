package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/passwd"
)

func csLine(nick, text string) string {
	return ":ChanServ!services@irc.test.local NOTICE " + nick + " :" + text
}

const csSrc = ":ChanServ!services@irc.test.local "

// founded makes nick register and found #bchat.
func founded(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := registered(t, s, nick)
	joinAll("#bchat", c)
	c.send("CS REGISTER #bchat Be kind, stay curious")
	c.expect(csLine(nick, "#bchat is registered to "+nick+". You are its Founder."))
	c.expect(csSrc + "MODE #bchat +r")
	return c
}

func TestChanServRegisterAndLevels(t *testing.T) {
	s := testServer(t, nil)
	plain := login(t, s, "plain")
	plain.send("CS REGISTER #x desc")
	plain.expect(csLine("plain", "Identify first: /ns identify <password>"))

	bee := founded(t, s, "Bee")
	ember := registered(t, s, "ember")
	mira := registered(t, s, "mira")
	kes := registered(t, s, "Kestrel")
	joinAll("#bchat", ember, mira, kes)

	ember.send("CS REGISTER #bchat again")
	ember.expect(csLine("ember", "#bchat is registered already. Its founder is Bee."))
	ember.send("CS AOP #bchat ADD mira")
	ember.expect(csLine("ember", "You need to be SOP or higher on #bchat for that."))

	bee.send("CS SOP #bchat ADD Kestrel")
	bee.expect(csLine("Bee", "Kestrel added to the SOP list of #bchat."))
	kes.expect(csSrc + "MODE #bchat +o Kestrel")
	bee.send("CS AOP #bchat ADD ember")
	bee.expect(csLine("Bee", "ember added to the AOP list of #bchat."))
	mira.expect(csSrc + "MODE #bchat +o ember")
	kes.send("CS VOP #bchat ADD mira")
	kes.expect(csLine("Kestrel", "mira added to the VOP list of #bchat."))
	mira.expect(csSrc + "MODE #bchat +v mira")
	kes.send("CS AOP #bchat ADD mira")
	kes.expect(csLine("Kestrel", "mira moved from VOP to AOP on #bchat."))
	mira.expect(csSrc + "MODE #bchat +o mira")
	kes.send("CS SOP #bchat ADD mira")
	kes.expect(csLine("Kestrel", "Only the founder of #bchat can do that."))
	kes.send("CS AOP #bchat ADD Bee")
	kes.expect(csLine("Kestrel", "Bee is the founder of #bchat."))
	ember.send("CS AOP #bchat DEL Kestrel")
	ember.expect(csLine("ember", "You need to be SOP or higher on #bchat for that."))

	mira.send("CS ACCESS #bchat LIST")
	mira.expect(csLine("mira", "Access list of #bchat"))
	mira.expect("Level    Nick     Gets   Can also")
	mira.expect("Founder  Bee      @      everything")
	mira.expect("SOP      Kestrel  @      add/remove AOP, VOP, UOP · akick")
	mira.expect(csLine("mira", "4 entries. Change one: /cs aop #bchat del <nick>"))
	mira.send("CS AOP #bchat LIST")
	mira.expect(csLine("mira", "AOP list of #bchat:"))
	mira.expect("  1   ember  added by Bee, ")
	mira.expect(csLine("mira", "2 entries."))
	plain.send("CS ACCESS #bchat LIST")
	plain.expect(csLine("plain", "Identify first: /ns identify <password>"))

	kes.send("CS AOP #bchat DEL mira")
	kes.expect(csLine("Kestrel", "mira removed from the AOP list of #bchat."))
	bee.send("CS AOP #bchat CLEAR")
	bee.expect(csLine("Bee", "The AOP list of #bchat is now empty (1 removed)."))
}

func TestChanServJoinsAndIdentify(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	bee.send("TOPIC #bchat :Welcome to #bchat")
	bee.expect("TOPIC #bchat :Welcome to #bchat")
	ember := registered(t, s, "ember")
	bee.send("CS AOP #bchat ADD ember")
	bee.expect("ember added")
	bee.send("CS SET #bchat ENTRYMSG Read the rules at bajubesi.org")
	bee.expect("The entry message of #bchat is now")
	bee.send("PART #bchat")
	bee.expect("PART #bchat")

	// The channel is empty now. Opening it doesn't make a stranger its op,
	// and the topic and +r come back.
	x := login(t, s, "x")
	x.send("JOIN #bchat")
	x.expect(" 366 x #bchat ")
	x.expect(csSrc + "TOPIC #bchat :Welcome to #bchat")
	x.expect(csSrc + "MODE #bchat +r")
	x.expect(csSrc + "MODE #bchat -o x")
	x.expect(csLine("x", "[#bchat] Read the rules at bajubesi.org"))

	ember.send("JOIN #bchat")
	x.expect(csSrc + "MODE #bchat +o ember")

	// Identifying while already inside gives the modes too.
	ember.send("QUIT")
	late := login(t, s, "ember")
	late.send("JOIN #bchat")
	late.expect(" 366 ember #bchat ")
	x.quiet("+o ember")
	late.send("NS IDENTIFY " + pw)
	x.expect(csSrc + "MODE #bchat +o ember")
}

func TestChanServModeLockSecureTopicLock(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	x := login(t, s, "x")
	joinAll("#bchat", x)

	bee.send("CS SET #bchat MLOCK +ntm-s")
	bee.expect(csLine("Bee", "The mode lock of #bchat is now +mnt-s."))
	x.expect(csSrc + "MODE #bchat +m")
	bee.send("MODE #bchat -m+s")
	x.expect(":Bee!~bee@127.0.0.1 MODE #bchat -m+s")
	x.expect(csSrc + "MODE #bchat +m-s")
	bee.send("CS SET #bchat MLOCK +q")
	bee.expect(csLine("Bee", "A mode lock can hold MRimnpst, k and l, not q."))
	bee.send("CS SET #bchat MLOCK +ntk")
	bee.expect(csLine("Bee", "+k in a mode lock needs a key: SET #chan MLOCK +k <key>"))
	bee.send("CS SET #bchat MLOCK +nt")
	bee.expect("The mode lock of #bchat is now +nt.")
	bee.send("MODE #bchat -m")
	bee.expect("MODE #bchat -m")

	bee.send("CS SET #bchat SECURE ON")
	bee.expect(csLine("Bee", "Secure is now on for #bchat."))
	bee.send("MODE #bchat +o x")
	x.expect(":Bee!~bee@127.0.0.1 MODE #bchat +o x")
	x.expect(csSrc + "MODE #bchat -o x")
	bee.send("CS OP #bchat x")
	bee.expect(csLine("Bee", "x isn't AOP or higher on #bchat, and SECURE is on."))

	bee.send("CS SET #bchat SECURE OFF")
	bee.expect("Secure is now off")
	bee.send("CS SET #bchat TOPICLOCK ON")
	bee.expect("Topic lock is now on")
	bee.send("MODE #bchat +o x")
	x.expect("MODE #bchat +o x")
	x.send("TOPIC #bchat :mine")
	x.expect(" 482 x #bchat :The topic is locked by ChanServ (AOP and up may change it)")
	bee.send("TOPIC #bchat :founder's topic")
	x.expect("TOPIC #bchat :founder's topic")
	x.send("CS TOPIC #bchat via services")
	x.expect(csLine("x", "Identify first: /ns identify <password>"))
	bee.send("CS TOPIC #bchat set by ChanServ")
	x.expect(csSrc + "TOPIC #bchat :set by ChanServ")
}

func TestChanServAkickBanUnban(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	x := login(t, s, "x")
	joinAll("#bchat", x)

	bee.send("CS AKICK #bchat ADD x go away")
	bee.expect(csLine("Bee", "Added x!*@* to the AKICK list of #bchat."))
	x.expect(csSrc + "KICK #bchat x :go away")
	x.send("JOIN #bchat")
	x.expect(" 474 x #bchat :Cannot join channel (+b)")
	bee.send("CS UNBAN #bchat x")
	bee.expect(csLine("Bee", "1 ban removed in #bchat."))
	x.send("JOIN #bchat")
	x.expect(csSrc + "MODE #bchat +b x!*@*")
	x.expect(csSrc + "KICK #bchat x :go away")
	bee.send("CS AKICK #bchat LIST")
	bee.expect(csLine("Bee", "  1   x!*@*  by Bee · go away"))
	bee.send("CS AKICK #bchat DEL 1")
	bee.expect(csLine("Bee", "Removed x!*@* from the AKICK list of #bchat."))
	bee.send("CS UNBAN #bchat x")
	bee.expect("1 ban removed")
	joinAll("#bchat", x)

	bee.send("CS BAN #bchat x")
	x.expect(csSrc + "MODE #bchat +b *!*@127.0.0.1")
	x.expect(csSrc + "KICK #bchat x :Banned by Bee")
	bee.quiet("KICK #bchat Bee") // the founder matches the mask but outranks the ban
}

func TestChanServOpsKickInviteUOP(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	ember := registered(t, s, "ember")
	u := registered(t, s, "uop")
	x := login(t, s, "x")
	joinAll("#bchat", ember, x)
	bee.send("CS AOP #bchat ADD ember")
	x.expect("+o ember")
	bee.send("CS UOP #bchat ADD uop")
	bee.expect("uop added to the UOP list")

	ember.send("CS DEOP #bchat Bee")
	ember.expect(csLine("ember", "You can't do that to Bee: their level is above yours."))
	ember.send("CS KICK #bchat Bee")
	ember.expect(csLine("ember", "You can't kick Bee: their level is the same as or above yours."))
	ember.send("CS VOICE #bchat x")
	x.expect(csSrc + "MODE #bchat +v x")
	ember.send("CS KICK #bchat x out you go")
	x.expect(csSrc + "KICK #bchat x :(ember) out you go")
	ember.send("CS DEOP #bchat")
	bee.expect(csSrc + "MODE #bchat -o ember")
	x.send("CS OP #bchat")
	x.expect(csLine("x", "Identify first: /ns identify <password>"))

	bee.send("MODE #bchat +ik secret")
	bee.expect("MODE #bchat +ik secret")
	u.send("JOIN #bchat") // UOP gets past +i and +k
	u.expect(" 366 uop #bchat ")
	u.send("PART #bchat")
	u.expect("PART #bchat")
	u.send("CS INVITE #bchat")
	u.expect(csSrc + "INVITE uop #bchat")
	x.send("JOIN #bchat")
	x.expect(" 473 x #bchat ")

	bee.send("CS CLEAR #bchat MODES")
	bee.expect(csSrc + "MODE #bchat -")
	bee.expect(csLine("Bee", "Cleared modes in #bchat"))
	bee.send("MODE #bchat")
	bee.expect(" 324 Bee #bchat +nrt")
}

func TestChanServInfoListSetDrop(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	registered(t, s, "Kestrel")
	other := login(t, s, "other")

	bee.send("CS SET #bchat URL https://bajubesi.org")
	bee.expect("The URL of #bchat is now: https://bajubesi.org")
	bee.send("CS SET #bchat SUCCESSOR Kestrel")
	bee.expect(csLine("Bee", "Kestrel gets #bchat if Bee's account goes."))
	other.send("CS INFO #bchat")
	other.expect(csLine("other", "#bchat · registered "))
	other.expect(csLine("other", "  Founder       Bee"))
	other.expect(csLine("other", "  Successor     Kestrel"))
	other.expect(csLine("other", "  Description   Be kind, stay curious"))
	other.expect(csLine("other", "  URL           https://bajubesi.org"))
	other.expect(csLine("other", "  Mode lock     +nt"))
	other.expect(csLine("other", "  Last used     in use now"))
	other.expect(csLine("other", "  Options       keep topic"))
	other.send("CS LIST #b*")
	other.expect("#bchat               Be kind, stay curious")
	other.expect(csLine("other", "1 channel matches."))
	bee.send("CS SET #bchat PRIVATE ON")
	bee.expect("Private is now on")
	other.send("CS INFO #bchat")
	other.expect(csLine("other", "#bchat is private."))
	other.send("CS LIST *")
	other.expect(csLine("other", "0 channels match."))

	bee.send("CS DROP #bchat")
	l := bee.expect("To go ahead within 2 minutes: /cs drop #bchat ")
	code := l[strings.LastIndex(l, " ")+1:]
	bee.send("CS DROP #bchat " + code)
	bee.expect(csSrc + "MODE #bchat -r")
	bee.expect(csLine("Bee", "#bchat is no longer registered."))
}

func TestFounderHandOverAndChannelMemo(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	kes := registered(t, s, "Kestrel")
	ember := registered(t, s, "ember")
	bee.send("CS SOP #bchat ADD ember")
	bee.expect("ember added to the SOP list")

	kes.send("MS SEND #bchat Can I get voice?")
	bee.expect(":MemoServ!services@irc.test.local NOTICE Bee :You have a new memo from Kestrel. Read it: /ms read 1")
	ember.expect("You have a new memo from Kestrel")
	kes.expect(msLine("Kestrel", "Memo sent to the founder and SOPs of #bchat (2 people)."))
	ember.send("MS READ 1")
	ember.expect(msLine("ember", "  [#bchat] Can I get voice?"))

	bee.send("CS SET #bchat SUCCESSOR Kestrel")
	bee.expect("Kestrel gets #bchat")
	bee.send("NS DROP")
	l := bee.expect("To go ahead within 2 minutes: /ns drop ")
	bee.send("NS DROP " + l[strings.LastIndex(l, " ")+1:])
	bee.expect("The account Bee is gone.")
	kes.send("CS INFO #bchat")
	kes.expect(csLine("Kestrel", "  Founder       Kestrel"))

	// Kestrel has no successor: dropping that account drops the channel.
	kes.send("NS DROP")
	l = kes.expect("To go ahead within 2 minutes: /ns drop ")
	kes.send("NS DROP " + l[strings.LastIndex(l, " ")+1:])
	bee.expect(csSrc + "MODE #bchat -r")
	ember.send("CS INFO #bchat")
	ember.expect(csLine("ember", "#bchat isn't registered."))
}

func TestChannelExpiry(t *testing.T) {
	s := testServerWith(t, nil, func(s *Server) {
		h, _ := passwd.Hash(pw)
		a, err := s.store.CreateAccount("Owner", h, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.CreateChannel("#dusty", a, "old", time.Now().Add(-61*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.CreateChannel("#fresh", a, "new", time.Now()); err != nil {
			t.Fatal(err)
		}
	})
	c := login(t, s, "x")
	c.send("CS INFO #dusty")
	c.expect(csLine("x", "#dusty isn't registered."))
	c.send("CS INFO #fresh")
	c.expect(csLine("x", "#fresh · registered "))
}
