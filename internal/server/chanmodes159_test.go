package server

import (
	"bufio"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// Step 1.5.9a: channel modes (PLAN-1.5.9.md).

// loginTLS registers nick over the TLS port.
func loginTLS(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	conn, err := tls.Dial("tcp", s.Addrs()[1].String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &tc{t: t, conn: conn, r: bufio.NewReader(conn), nick: nick}
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick)
	c.expect(":" + nick + " MODE " + nick + " +i")
	return c
}

// noBoot lets tests count join floods right after the start.
func noBoot(s *Server) { s.floodBoot = 0 }

func TestContentModes(t *testing.T) {
	s := testServer(t, nil)
	bee, nova, ember := login(t, s, "Bee"), login(t, s, "nova"), login(t, s, "ember")
	joinAll("#a", bee, nova, ember)

	nova.send("MODE #a +c")
	nova.expect(" 482 nova #a :You're not channel operator")
	bee.send("MODE #a +cCT")
	ember.expect("MODE #a +cCT")

	nova.send("PRIVMSG #a :\x034red\x03 text")
	nova.expect(" 404 nova #a :Color is not permitted in this channel")
	nova.send("PRIVMSG #a :\x01VERSION\x01")
	nova.expect(" 404 nova #a :CTCPs are not permitted in this channel")
	nova.send("PRIVMSG #a :\x01ACTION waves\x01")
	ember.expect("PRIVMSG #a :\x01ACTION waves\x01")
	nova.send("NOTICE #a :hello all")
	nova.send("PRIVMSG #a :plain words")
	ember.expect("PRIVMSG #a :plain words") // the NOTICE never came first
	// Ops skip the content modes.
	bee.send("PRIVMSG #a :\x034op colour")
	ember.expect("PRIVMSG #a :\x034op colour")

	bee.send("MODE #a -c+S")
	ember.expect("MODE #a -c+S")
	nova.send("PRIVMSG #a :\x0304,01loud\x03 and \x04ff0000hex")
	ember.expect(":nova!~nova@127.0.0.1 PRIVMSG #a :loud and hex")

	// +N, +V, +Q.
	bee.send("MODE #a +NVQ")
	ember.expect("MODE #a +NVQ")
	nova.send("NICK nova2")
	nova.expect(" 447 nova :Can not change nickname while on #a (+N)")
	bee.send("NICK Bee2") // ops may
	bee.expect(":Bee!~bee@127.0.0.1 NICK Bee2")
	bee.send("INVITE ember #a")
	bee.expect(" 518 Bee2 :Cannot invite (+V) at channel #a")
	bee.send("KICK #a nova")
	bee.expect(" 972 Bee2 KICK :channel is +Q")

	// +O and +P are for opers, +Z for the server.
	bee.send("MODE #a +P")
	bee.expect(" 481 Bee2 :Permission Denied - only IRC operators may set +P")
	bee.send("MODE #a +Z")
	bee.send("MODE #a")
	bee.expect(" 324 Bee2 #a +CNQSTVnt")
}

func TestParamModes(t *testing.T) {
	s := testServer(t, nil)
	bee, nova := login(t, s, "Bee"), login(t, s, "nova")
	joinAll("#a", bee, nova)

	for _, bad := range []struct{ mode, arg, why string }{
		{"f", "10j", "Use +f ["},
		{"f", "[10x]:15", "Use +f ["},
		{"f", "[10j#K]:15", "Type j can take the actions: iR"},
		{"f", "[10j#i99]:15", "The minutes after an action must be 0-60"},
		{"F", "loose", "Unknown flood profile"},
		{"L", "#a", "A channel can't link to itself"},
		{"E", "1:10", "Use +E <lines>:<seconds>"},
		{"B", "10", "Use +B <min length>:<percent>"},
		{"J", "0", "Use +J <seconds>"},
	} {
		bee.send("MODE #a +" + bad.mode + " " + bad.arg)
		bee.expect(" 696 Bee #a " + bad.mode + " " + bad.arg + " :" + bad.why)
	}
	bee.send("MODE #a +fFLEBJ [5m#M3,10j]:20 STRICT #spill 2:60 10:70 30")
	nova.expect("MODE #a +fFLE [10j,5m#M3]:20 strict #spill 2:60") // MODES=4 per line
	nova.expect("MODE #a +BJ 10:70 30")
	bee.send("MODE #a")
	bee.expect(" 324 Bee #a +BEFJLfnt 10:70 2:60 strict 30 #spill [10j,5m#M3]:20")

	// +E: the second same line in 60 s is refused.
	nova.send("PRIVMSG #a :same again")
	bee.expect("PRIVMSG #a :same again")
	nova.send("PRIVMSG #a :same again")
	nova.expect(" 404 nova #a :Your message repeats what you sent before (+E)")
	// +B: 70% capitals in 10+ letters.
	nova.send("PRIVMSG #a :WHY IS EVERYONE SHOUTING")
	nova.expect(" 404 nova #a :Your message cannot be 70% or more capital letters if it's 10 or more letters long (+B)")
	nova.send("PRIVMSG #a :OK then")
	bee.expect("PRIVMSG #a :OK then")
	// +J: no rejoin for 30 s after a kick.
	bee.send("KICK #a nova :out")
	nova.expect("KICK #a nova :out")
	nova.send("JOIN #a")
	nova.expect(" 495 nova #a :You must wait 30 seconds after being kicked to rejoin (+J)")
	bee.send("MODE #a -fFLEBJ")
	bee.expect("MODE #a -fFLEBJ")
	bee.send("MODE #a")
	bee.expect(" 324 Bee #a +nt")
}

func TestLinkMode(t *testing.T) {
	s := testServer(t, nil)
	bee, nova, ember := login(t, s, "Bee"), login(t, s, "nova"), login(t, s, "ember")
	joinAll("#full", bee)
	bee.send("MODE #full +lL 1 #spill")
	bee.expect("MODE #full +lL 1 #spill")
	nova.send("JOIN #full")
	nova.expect(" 470 nova #full #spill :[Link] #full has become full, so you are automatically being transferred to the linked channel #spill")
	nova.expect(":nova!~nova@127.0.0.1 JOIN #spill")
	// A loop of +L channels stops after a few hops.
	nova.send("MODE #spill +iL #full")
	nova.expect("MODE #spill +iL #full")
	ember.send("JOIN #full")
	ember.expect(" 470 ember #full #spill ")
	ember.expect(" 470 ember #spill #full ")
	ember.expect(" 473 ember #spill :Cannot join channel (+i)")
}

func TestDelayJoin(t *testing.T) {
	s := testServer(t, nil)
	bee, nova, ember := login(t, s, "Bee"), login(t, s, "nova"), login(t, s, "ember")
	joinAll("#d", bee)
	bee.send("MODE #d +D")
	bee.expect("MODE #d +D")
	joinAll("#d", nova, ember)
	bee.quiet("JOIN #d")
	bee.send("NAMES #d")
	expectNames(t, bee.expect(" 353 Bee = #d :"), "@Bee")
	nova.send("NAMES #d") // nova sees itself
	expectNames(t, nova.expect(" 353 nova = #d :"), "@Bee nova")
	bee.send("WHOIS nova")
	bee.quiet(" 319 Bee nova ")

	nova.send("PRIVMSG #d :hi all")
	bee.expect(":nova!~nova@127.0.0.1 JOIN #d")
	bee.expect(":nova!~nova@127.0.0.1 PRIVMSG #d :hi all")
	// ember stays hidden; its part reaches only ember.
	ember.send("PART #d")
	ember.expect(":ember!~ember@127.0.0.1 PART #d")
	nova.quiet("PART #d")
	ember.send("JOIN #d")
	ember.expect(" 366 ember #d ")
	// A status shows the member first.
	bee.send("MODE #d +v ember")
	nova.expect(":ember!~ember@127.0.0.1 JOIN #d")
	nova.expect("MODE #d +v ember")
	// -D shows everyone still hidden.
	joinAll("#d", login(t, s, "quiet"))
	bee.send("MODE #d -D")
	nova.expect(":quiet!~quiet@127.0.0.1 JOIN #d")
}

func TestSecureOnly(t *testing.T) {
	s := testServer(t, nil)
	safe, plain := loginTLS(t, s, "safe"), login(t, s, "plain")
	joinAll("#tls", safe)
	safe.send("MODE #tls +z")
	safe.expect("MODE #tls +z")
	safe.expect(":irc.test.local MODE #tls +Z")
	plain.send("JOIN #tls")
	plain.expect(" 489 plain #tls :Cannot join channel (Secure connection is required)")
	joinAll("#tls", loginTLS(t, s, "safe2"))
	safe.send("MODE #tls -z")
	safe.expect(":irc.test.local MODE #tls -Z")
}

func TestPermanentChannel(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	joinAll("#keep", b)
	b.send("MODE #keep +P")
	b.expect("MODE #keep +P")
	b.send("PART #keep")
	b.expect("PART #keep")
	b.send("MODE #keep")
	b.expect(" 324 Boss #keep +Pnt")
	b.send("MODE #keep -P") // empty and not permanent: gone
	b.sync()
	b.send("MODE #keep")
	b.expect(" 403 Boss #keep :No such channel")
}

func TestFloodGuard(t *testing.T) {
	s := testServerWith(t, nil, noBoot)
	bee := login(t, s, "Bee")
	joinAll("#f", bee)
	bee.send("MODE #f +f [3j#i1,3t,4m#M]:30")
	bee.expect("MODE #f +f [3j#i1,4m#M,3t]:30")

	// Join flood: the 4th join in 30 s sets +i for a minute.
	for _, n := range []string{"j1", "j2", "j3", "j4"} {
		joinAll("#f", login(t, s, n))
	}
	bee.expect(":irc.test.local NOTICE @#f :*** Channel joinflood detected (limit is 3 per 30 seconds), setting mode +i")
	bee.expect(":irc.test.local MODE #f +i")
	done := make(chan struct{})
	s.post(func() { s.floodTick(time.Now().Add(2 * time.Minute)); close(done) })
	<-done
	bee.expect(":irc.test.local MODE #f -i")

	// Text flood: the 4th line from one person in 30 s is a kick.
	talk := login(t, s, "talk")
	joinAll("#f", talk)
	for i := 0; i < 3; i++ {
		talk.send("PRIVMSG #f :line " + string(rune('a'+i)))
		bee.expect("PRIVMSG #f :line ")
	}
	talk.send("PRIVMSG #f :one too many")
	bee.expect(":irc.test.local KICK #f talk :Flooding (limit is 3 lines per 30 seconds)")
	bee.quiet("one too many")

	// Message flood from many people: +M (stays, no minutes given).
	bee.send("MODE #f -f+f [20m#M]:30")
	for i := 0; i < 21; i++ {
		c := login(t, s, "m"+string(rune('a'+i)))
		joinAll("#f", c)
		c.send("PRIVMSG #f :hello")
	}
	bee.expect("*** Channel msg/noticeflood detected (limit is 20 per 30 seconds), setting mode +M")
	bee.expect(":irc.test.local MODE #f +M")
}

func TestDefaultFloodProfile(t *testing.T) {
	s := testServerWith(t, func(c *config.Config) { c.Channels.FloodProfile = "very-strict" }, noBoot)
	bee := login(t, s, "Bee")
	joinAll("#p", bee)
	// very-strict: more than 10 joins in 15 s -> +R for 10 minutes.
	for i := 0; i < 11; i++ {
		joinAll("#p", login(t, s, "p"+string(rune('a'+i))))
	}
	bee.expect("*** Channel joinflood detected (limit is 10 per 15 seconds), setting mode +R")
	bee.expect(":irc.test.local MODE #p +R")
	// +F off switches it off for one channel.
	joinAll("#q", bee)
	bee.send("MODE #q +F off")
	bee.expect("MODE #q +F off")
	for i := 0; i < 11; i++ {
		joinAll("#q", login(t, s, "q"+string(rune('a'+i))))
	}
	bee.quiet("MODE #q +R")
}

func TestModeLockParams(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	bee.send("CS MODE #bchat LOCK ADD +fC [5j]:10")
	bee.expect(csLine("Bee", "The mode lock of #bchat is now +Cntf [5j]:10."))
	bee.expect(csSrc + "MODE #bchat +Cf [5j]:10")
	bee.send("CS MODE #bchat LOCK ADD +F wild")
	bee.expect(csLine("Bee", "+F wild: Unknown flood profile (use very-strict, strict, normal, relaxed, very-relaxed, off)"))
	bee.send("MODE #bchat -f")
	bee.expect("MODE #bchat -f")
	bee.expect(csSrc + "MODE #bchat +f [5j]:10")

	bee.send("CS SET #bchat OVERFLOW #more")
	bee.expect(csLine("Bee", "People who can't join #bchat now go to #more."))
	bee.expect(csSrc + "MODE #bchat +L #more")
	bee.send("CS MODE #bchat LOCK LIST")
	bee.expect(csLine("Bee", "The mode lock of #bchat is +CntLf #more [5j]:10."))
	bee.send("CS SET #bchat OVERFLOW OFF")
	bee.expect(csLine("Bee", "#bchat has no overflow channel now."))
	bee.expect(csSrc + "MODE #bchat -L")
	bee.send("CS MODE #bchat LOCK DEL f")
	bee.expect(csLine("Bee", "The mode lock of #bchat is now +Cnt."))
}

func TestFloodProfileSettingAndHelp(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("AS SET flood-profile wild")
	b.expect("flood-profile is one of very-strict, strict, normal, relaxed, very-relaxed, off.")
	b.send("AS SET flood-profile strict")
	b.expect("flood-profile is now strict (was normal)")
	b.send("HELPOP CHMODES")
	b.expect(" 704 Boss CHMODES :***** Channel modes *****")
	b.expect(" 705 Boss CHMODES : F <name>   flood profile:")
	b.send("HELPOP")
	b.expect(" 705 Boss * :Other topics: CHMODES (channel modes).")
}
