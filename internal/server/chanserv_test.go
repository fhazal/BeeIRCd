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
	kes.expect(csLine("Kestrel", "You can only give levels below your own (SOP) on #bchat."))
	kes.send("CS AOP #bchat ADD Bee")
	kes.expect(csLine("Kestrel", "Bee is the founder of #bchat."))
	ember.send("CS AOP #bchat DEL Kestrel")
	ember.expect(csLine("ember", "You need to be SOP or higher on #bchat for that."))

	mira.send("CS ACCESS #bchat LIST")
	mira.expect(csLine("mira", "Access list of #bchat"))
	mira.expect(csLine("mira", "  #   Level   Nick     Gets"))
	mira.expect(csLine("mira", "  –   Founder Bee      @"))
	mira.expect(csLine("mira", "  1   SOP     Kestrel  @"))
	mira.expect(csLine("mira", "4 entries. Change one: /cs access #bchat add <nick> <level>"))
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
	if l := x.expect(" QUIT "); !strings.HasPrefix(l, ":ember!") { // nick is free again before it's reused
		t.Fatalf("want ember's QUIT, got %q", l)
	}
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
	bee.expect(csLine("Bee", "A mode lock can hold CDKMNQRSTVcimnpstz, k, l and BEFHJLf, not q."))
	bee.send("CS SET #bchat MLOCK +ntk")
	bee.expect(csLine("Bee", "+k in a mode lock needs a key: +k <key>"))
	bee.send("CS SET #bchat MLOCK +nt")
	bee.expect("The mode lock of #bchat is now +nt.")
	bee.send("MODE #bchat -m")
	bee.expect("MODE #bchat -m")

	bee.send("CS SET #bchat SECUREOPS ON")
	bee.expect(csLine("Bee", "Secure ops is now on for #bchat."))
	bee.send("MODE #bchat +o x")
	x.expect(":Bee!~bee@127.0.0.1 MODE #bchat +o x")
	x.expect(csSrc + "MODE #bchat -o x")
	bee.send("CS OP #bchat x")
	bee.expect(csLine("Bee", "x may not hold +o on #bchat: SECUREOPS is on."))

	bee.send("CS SET #bchat SECUREOPS OFF")
	bee.expect("Secure ops is now off")
	bee.send("CS SET #bchat TOPICLOCK ON")
	bee.expect("Topic lock is now on")
	bee.send("MODE #bchat +o x")
	x.expect("MODE #bchat +o x")
	x.send("TOPIC #bchat :mine")
	x.expect(" 482 x #bchat :The topic is locked by ChanServ")
	bee.send("TOPIC #bchat :founder's topic")
	x.expect("TOPIC #bchat :founder's topic")
	x.send("CS TOPIC #bchat via services")
	x.expect(csLine("x", "Identify first: /ns identify <password>"))
	bee.send("CS TOPIC #bchat SET set by ChanServ")
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
	bee.expect(csLine("Bee", "  1   x!*@*  go away"))
	bee.send("CS AKICK #bchat VIEW")
	bee.expect(" x!*@*  added by Bee, ")
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
	ember.expect(csLine("ember", "You can't do that to Bee: their level is the same as or above yours."))
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
	u.send("CS INVITE #bchat") // INVITE is AOP and up by default (Anope)
	u.expect(csLine("uop", "You need to be AOP or higher on #bchat for that."))
	bee.send("CS LEVELS #bchat SET INVITE 1")
	bee.expect(csLine("Bee", "INVITE on #bchat now needs level 1."))
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
	other.expect(csLine("other", "  Last used     in use now"))
	other.quiet("Mode lock") // the details are for those with INFO (Anope)
	bee.send("CS INFO #bchat")
	bee.expect(csLine("Bee", "  Mode lock     +nt"))
	bee.expect(csLine("Bee", "  Options       keep topic, peace, security, secure founder, sign kick"))
	bee.expect(csLine("Bee", "  Ban type      2 (*!*@host)"))
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
	bee.expect("Type the channel name twice to drop it, as a confirmation: /cs drop #bchat #bchat")
	bee.send("CS DROP #bchat #bchat")
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

	joinAll("#bchat", ember)
	kes.send("MS SEND #bchat Can I get voice?")
	bee.expect(msLine("Bee", "There is a new memo on #bchat from Kestrel. Read it: /ms read #bchat 1"))
	ember.expect(msLine("ember", "There is a new memo on #bchat from Kestrel. Read it: /ms read #bchat 1"))
	kes.expect(msLine("Kestrel", "Memo sent to #bchat."))
	kes.send("MS LIST #bchat")
	kes.expect(msLine("Kestrel", "You need the MEMO privilege on #bchat for its memo box (SOP or higher there)."))
	ember.send("MS READ #bchat 1")
	ember.expect(msLine("ember", "Memo 1 on #bchat from Kestrel · "))
	ember.expect(msLine("ember", "  Can I get voice?"))
	bee.send("MS LIST #bchat")
	bee.expect(msLine("Bee", "Memo box of #bchat:"))
	bee.expect(" read")
	bee.expect(msLine("Bee", "1 memo, none new. Delete one: /ms del #bchat <num>"))

	bee.send("CS SET #bchat SUCCESSOR Kestrel")
	bee.expect("Kestrel gets #bchat")
	bee.send("NS DROP Bee")
	bee.expect(nsLine("Bee", "Bee has been dropped."))
	kes.send("CS INFO #bchat")
	kes.expect(csLine("Kestrel", "  Founder       Kestrel"))

	// Kestrel has no successor: dropping that account drops the channel.
	kes.send("NS DROP Kestrel")
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

func TestChanServAnopeForms(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	for _, n := range []string{"ember", "kes", "wren"} {
		registered(t, s, n)
	}

	// xOP: optional description, numbers and ranges, patterns.
	bee.send("CS AOP #bchat ADD ember helps with the wiki")
	bee.expect("ember added to the AOP list of #bchat.")
	bee.send("CS AOP #bchat ADD kes")
	bee.expect("kes added")
	bee.send("CS AOP #bchat ADD wren")
	bee.expect("wren added")
	bee.send("CS AOP #bchat LIST")
	bee.expect(csLine("Bee", "AOP list of #bchat:"))
	l := bee.expect("  1   ember  added by Bee, ")
	if !strings.HasSuffix(l, " · helps with the wiki") {
		t.Errorf("entry line %q lacks its description", l)
	}
	bee.send("CS AOP #bchat LIST k*")
	bee.expect("  2   kes ")
	bee.expect(csLine("Bee", "1 entry."))
	bee.send("CS AOP #bchat DEL 2-3")
	bee.expect(csLine("Bee", "kes, wren removed from the AOP list of #bchat."))
	bee.send("CS AOP #bchat DEL 9")
	bee.expect(csLine("Bee", "No entry 9 on the AOP list of #bchat."))

	// SET in Anope's order and in the old one; DESCRIPTION and DESC.
	bee.send("CS SET DESCRIPTION #bchat Anope order")
	bee.expect(csLine("Bee", "The description of #bchat is now: Anope order"))
	bee.send("CS SET #bchat DESC old order")
	bee.expect(csLine("Bee", "The description of #bchat is now: old order"))
	bee.send("CS SET SECURE #bchat maybe")
	bee.expect(csLine("Bee", "Syntax: SET SECURE #bchat ON|OFF"))

	// AKICK numbers, ENFORCE and CLEAR.
	bee.send("CS AKICK #bchat ADD *!*@one.example")
	bee.expect("Added *!*@one.example")
	bee.send("CS AKICK #bchat ADD *!*@two.example")
	bee.expect("Added *!*@two.example")
	bee.send("CS AKICK #bchat ADD *!*@three.example")
	bee.expect("Added *!*@three.example")
	bee.send("CS AKICK #bchat DEL 1,3") // same second: listed by mask, so 3 is two.example
	bee.expect(csLine("Bee", "Removed *!*@one.example, *!*@two.example from the AKICK list of #bchat."))
	bee.send("CS AKICK #bchat ADD *!~guest@*")
	bee.expect("Added")
	guest := login(t, s, "guest")
	joinAll("#bchat", guest)
	guest.expect(csSrc + "KICK #bchat guest")
	bee.send("CS AKICK #bchat ENFORCE")
	bee.expect(csLine("Bee", "AKICK enforced on #bchat: 0 people kicked."))
	bee.send("CS AKICK #bchat CLEAR")
	bee.expect(csLine("Bee", "The AKICK list of #bchat is now empty (2 removed)."))

	// INVITE someone else (AOP and up), REGISTER without a description.
	bee.send("MODE #bchat +i")
	bee.expect("MODE #bchat +i")
	bee.send("CS INVITE #bchat guest")
	guest.expect(csSrc + "INVITE guest #bchat")
	bee.expect(csLine("Bee", "guest is invited to #bchat."))
	guest.send("JOIN #bchat")
	guest.expect(" 366 guest #bchat") // an invite gets past the old ban, and the AKICK list is empty now
	bee.send("JOIN #nodesc")
	bee.expect(" 366 ")
	bee.send("CS REGISTER #nodesc")
	bee.expect("#nodesc is registered to Bee. You are its Founder.")
}

func TestChanServOperDropAndFounder(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bee := founded(t, s, "Bee")
	registered(t, s, "ember")
	b.send("CS SET FOUNDER #bchat ember")
	b.expect("Boss used ChanServ: SET FOUNDER #bchat ember (was Bee)")
	b.expect(csLine("Boss", "The founder of #bchat is now ember."))
	bee.send("CS SET SUCCESSOR #bchat Boss")
	bee.expect(csLine("Bee", "Only the founder of #bchat can do that."))
	b.send("CS DROP #bchat")
	b.expect("Type the channel name twice to drop it, as a confirmation: /cs drop #bchat #bchat")
	b.send("CS DROP #bchat #bchat")
	b.expect("Boss used ChanServ: DROP #bchat (founder ember)")
	b.expect(csLine("Boss", "#bchat is no longer registered."))
}

// "You need to be ..." names a level people know, not Anope's 9999
// (QA 1.0.0 round, bug 3).
func TestChanServNeedWordsNamesTheLevel(t *testing.T) {
	s := testServer(t, nil)
	f := founded(t, s, "Fee")
	v := registered(t, s, "Vee")
	f.send("CS VOP #bchat ADD Vee")
	f.expect("Vee added to the VOP list of #bchat.")
	v.send("CS SET #bchat KEEPTOPIC OFF")
	v.expect(csLine("Vee", "You need to be the founder on #bchat for that."))
}
