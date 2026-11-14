package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

func fullPrefixes(cfg *config.Config) { cfg.Server.Prefixes = "~&@%+" }

func TestFullPrefixesISupport(t *testing.T) {
	s := testServer(t, fullPrefixes)
	c := raw(t, s)
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.expect(" 004 Bee irc.test.local BeeIRCd-")
	l := c.expect(" 005 Bee ") + c.expect(" 005 Bee ")
	for _, tok := range []string{"PREFIX=(qaohv)~&@%+", "STATUSMSG=~&@%+"} {
		if !contains(l, tok) {
			t.Errorf("005 lacks %s: %s", tok, l)
		}
	}
}

func TestDefaultPrefixesRefuseNewRanks(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	ember := login(t, s, "ember")
	joinAll("#bchat", ember)
	bee.send("MODE #bchat +h ember")
	bee.expect(" 472 Bee h :is unknown mode char to me for #bchat")
	bee.send("CS HOP #bchat ADD ember")
	bee.expect(csLine("Bee", "HOP lists are only used on servers with ~&@%+ prefixes; this one uses @+."))
	bee.send("CS OWNER #bchat")
	bee.expect(csLine("Bee", "This server uses only @ and + prefixes, so +q doesn't exist here."))
}

func TestRankRules(t *testing.T) {
	s := testServer(t, fullPrefixes)
	op := login(t, s, "op")
	half := login(t, s, "half")
	user := login(t, s, "user")
	joinAll("#r", op, half, user)
	op.send("MODE #r +h half")
	half.expect(":op!~op@127.0.0.1 MODE #r +h half")
	op.send("MODE #r +q op")
	op.expect(" 482 op #r :You're not channel operator")

	// Halfops voice, ban, moderate and set the topic...
	half.send("MODE #r +vm user")
	user.expect(":half!~half@127.0.0.1 MODE #r +vm user")
	half.send("TOPIC #r :halfop topic")
	user.expect("TOPIC #r :halfop topic")
	// ...but don't op, set keys, or kick ops and halfops.
	half.send("MODE #r +o user")
	half.expect(" 482 half #r :You need a higher channel status for that")
	half.send("MODE #r +k secret")
	half.expect(" 482 half #r :You need a higher channel status for that")
	half.send("KICK #r op")
	half.expect(" 482 half #r :You can't kick op: their channel status protects them")
	half.send("KICK #r user :out")
	user.expect(":half!~half@127.0.0.1 KICK #r user :out")

	// NAMES shows the highest prefix, multi-prefix shows all.
	op.send("MODE #r +v half")
	op.send("NAMES #r")
	op.expect(" 353 op = #r :")
	op.send("CAP REQ :multi-prefix")
	op.expect("ACK")
	op.send("NAMES #r")
	line := op.expect(" 353 op = #r :")
	if !contains(line, "%+half") || !contains(line, "@op") {
		t.Errorf("multi-prefix NAMES: %s", line)
	}
	// Status messages reach that status and up.
	user2 := login(t, s, "user2")
	joinAll("#r", user2)
	op.send("PRIVMSG %#r :halfops only")
	half.expect("PRIVMSG %#r :halfops only")
	user2.quiet("halfops only")
}

func TestChanServQopHop(t *testing.T) {
	s := testServer(t, fullPrefixes)
	bee := founded(t, s, "Bee")
	bee.expect(csSrc + "MODE #bchat +q Bee") // the founder gets ~ on registering
	ember := registered(t, s, "ember")
	mira := registered(t, s, "mira")
	joinAll("#bchat", ember, mira)

	bee.send("CS QOP #bchat ADD ember")
	bee.expect(csLine("Bee", "ember added to the QOP list of #bchat."))
	ember.expect(csSrc + "MODE #bchat +qo ember ember")
	ember.send("CS HOP #bchat ADD mira")
	mira.expect(csSrc + "MODE #bchat +h mira")
	ember.send("CS SOP #bchat ADD mira")
	mira.expect(csSrc + "MODE #bchat +ao mira mira")
	mira.send("CS QOP #bchat ADD mira")
	mira.expect(csLine("mira", "You can't change mira: their level is the same as or above yours."))

	// An admin (&) is protected from ops; only owners take it away.
	plain := login(t, s, "plain")
	joinAll("#bchat", plain)
	bee.send("MODE #bchat +o plain")
	plain.send("MODE #bchat -a mira")
	plain.expect(" 482 plain #bchat :You're not channel operator")
	plain.send("KICK #bchat mira")
	plain.expect(" 482 plain #bchat :You can't kick mira: their channel status protects them")

	mira.send("CS DOWN #bchat")
	mira.expect(csSrc + "MODE #bchat -aoh mira mira mira")
	mira.send("CS UP #bchat")
	mira.expect(csSrc + "MODE #bchat +ao mira mira")
	mira.send("CS OWNER #bchat")
	mira.expect(csLine("mira", "You need to be QOP or higher on #bchat for that."))
	ember.send("CS DEOWNER #bchat")
	ember.expect(csSrc + "MODE #bchat -q ember")
	ember.send("CS OWNER #bchat")
	ember.expect(csSrc + "MODE #bchat +q ember")

	ember.send("CS ACCESS #bchat LIST")
	ember.expect(csLine("ember", "  –   Founder Bee    ~@"))
	ember.expect(csLine("ember", "  1   QOP     ember  ~@"))
	ember.expect(csLine("ember", "  2   SOP     mira   &@"))
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
