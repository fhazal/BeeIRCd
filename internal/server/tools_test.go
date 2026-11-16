package server

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// WATCH, caller ID (+g, ACCEPT), DCC rules, CYCLE/REMOVE/UNINVITE, the
// oper tools, WHOWAS across a restart and ident lookups.

func TestWatch(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("WATCH +ember +ghost")
	bee.expect(" 604 Bee ember ~ember 127.0.0.1 ")
	bee.expect(" 605 Bee ghost * * 0 :is offline")
	ember.send("QUIT :bye")
	bee.expect(" 601 Bee ember ~ember 127.0.0.1 ")
	ghost := login(t, s, "ghost")
	if l := bee.expect(" 600 Bee ghost ~ghost 127.0.0.1 "); !strings.HasSuffix(l, ":logged online") {
		t.Errorf("logon line %q", l)
	}
	bee.send("WATCH l")
	bee.expect(" 604 Bee ghost ")
	bee.expect(" 607 Bee :End of WATCH l")
	bee.send("WATCH S")
	bee.expect(" 603 Bee :You have 2 and are on 0 WATCH entries")
	bee.expect(" 606 Bee :")
	bee.expect(" 607 Bee :End of WATCH S")
	ghost.send("WATCH +Bee")
	ghost.expect(" 604 ghost Bee ")
	bee.send("WATCH S")
	bee.expect(" 603 Bee :You have 2 and are on 1 WATCH entries")

	// MONITOR and WATCH on the same nick each speak their own numerics.
	bee.send("MONITOR + ember")
	bee.expect(" 731 Bee :ember")
	login(t, s, "ember")
	bee.expect(" 730 Bee :ember!~ember@127.0.0.1")
	bee.expect(" 600 Bee ember ")

	bee.send("WATCH -ghost")
	bee.expect(" 602 Bee ghost ~ghost 127.0.0.1 ")
	ghost.send("NICK spirit")
	ghost.expect("NICK spirit")
	bee.quiet(" 601 ")
	bee.send("WATCH C")
	bee.send("WATCH S")
	bee.expect(" 603 Bee :You have 0 and are on 1 WATCH entries")
	bee.send("MONITOR L")
	bee.expect(" 732 Bee :ember") // clearing WATCH left MONITOR alone
}

func TestCallerID(t *testing.T) {
	s := testServer(t, nil)
	bee, ember, nova := login(t, s, "Bee"), login(t, s, "ember"), login(t, s, "nova")
	bee.send("MODE Bee +g")
	bee.expect("MODE Bee +g")
	ember.send("PRIVMSG Bee :hi")
	ember.expect(" 716 ember Bee :is in +g mode")
	ember.expect(" 717 ember Bee :has been informed that you messaged them.")
	bee.expect(" 718 Bee ember ~ember@127.0.0.1 :is messaging you")
	ember.send("PRIVMSG Bee :hi again")
	ember.expect(" 716 ember Bee ")
	ember.quiet(" 717 ") // told once a minute
	ember.send("NOTICE Bee :a notice")
	ember.sync()
	bee.quiet("a notice")

	bee.send("ACCEPT ember")
	bee.send("ACCEPT ember")
	bee.expect(" 457 Bee ember :is already on your accept list")
	ember.send("PRIVMSG Bee :third time")
	bee.expect(":ember!~ember@127.0.0.1 PRIVMSG Bee :third time")
	bee.send("ACCEPT *")
	bee.expect(" 281 Bee ember")
	bee.expect(" 282 Bee :End of /ACCEPT list.")
	bee.send("ACCEPT -ember,-ghost")
	bee.expect(" 401 Bee ghost ")
	bee.send("ACCEPT -ember")
	bee.expect(" 458 Bee ember :is not on your accept list")
	ember.send("PRIVMSG Bee :and now?")
	ember.expect(" 716 ember Bee ")

	// Whoever a +g person writes to may answer.
	bee.send("PRIVMSG nova :hello")
	nova.expect("PRIVMSG nova :hello")
	nova.send("PRIVMSG Bee :hi back")
	bee.expect(":nova!~nova@127.0.0.1 PRIVMSG Bee :hi back")
}

func TestDCCRules(t *testing.T) {
	s, _, _ := fileServer(t, `
[[deny_dcc]]
filename = "*.exe"
reason = "Possible virus"

[[deny_dcc]]
filename = "*.zip"
reason = "Archive"
soft = true

[[allow_dcc]]
filename = "safe*.zip"
soft = true
`)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	dcc := func(file string) string { return "\x01DCC SEND " + file + " 2130706433 5000 100\x01" }
	ember.send("PRIVMSG Bee :" + dcc("C:\\tools\\Setup.EXE"))
	ember.expect(" 531 ember Bee :Cannot DCC SEND file Setup.EXE (Possible virus)")
	ember.send("PRIVMSG Bee :" + dcc(`"my files.zip"`))
	ember.expect(" 531 ember Bee :Cannot DCC SEND file my files.zip (Archive): Bee has not allowed files like this from you")
	bee.expect("ember (~ember@127.0.0.1) tried to DCC SEND you a file named 'my files.zip'")
	bee.expect("/DCCALLOW +ember")
	ember.send("PRIVMSG Bee :" + dcc("safe1.zip"))
	bee.expect("DCC SEND safe1.zip")
	ember.send("PRIVMSG Bee :" + dcc("notes.txt"))
	bee.expect("DCC SEND notes.txt")

	bee.send("DCCALLOW +ember")
	bee.expect(" 617 Bee :ember has been added to your DCC allow list")
	ember.send("PRIVMSG Bee :" + dcc("pics.zip"))
	bee.expect("DCC SEND pics.zip")
	ember.send("PRIVMSG Bee :" + dcc("still.exe"))
	ember.expect(" 531 ember Bee :Cannot DCC SEND file still.exe")
	bee.send("DCCALLOW LIST")
	bee.expect(" 618 Bee :ember (~ember@127.0.0.1)")
	bee.expect(" 619 Bee :End of DCCALLOW LIST")
	bee.send("DCCALLOW -ember")
	bee.expect(" 617 Bee :ember has been removed from your DCC allow list")
	bee.send("DCCALLOW HELP")
	bee.expect(" 620 Bee :/DCCALLOW ")
	bee.expect(" 619 Bee :End of DCCALLOW HELP")
	bee.send("DCCALLOW")
	bee.expect("No command specified for DCCALLOW")

	ember.send("JOIN #x")
	ember.expect(" 366 ")
	ember.send("PRIVMSG #x :" + dcc("worm.exe"))
	ember.expect(" 404 ember #x :Cannot DCC SEND file worm.exe (Possible virus)")
}

func TestCycleRemoveUninvite(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #x")
	bee.expect(" 366 ")
	ember.send("JOIN #x")
	bee.expect(":ember!~ember@127.0.0.1 JOIN #x")
	ember.send("CYCLE #x")
	bee.expect(":ember!~ember@127.0.0.1 PART #x :Cycling")
	bee.expect(":ember!~ember@127.0.0.1 JOIN #x")
	ember.send("CYCLE #nowhere")
	ember.expect(" 403 ember #nowhere ")
	bee.send("MODE #x +i")
	ember.expect("MODE #x +i")
	ember.send("CYCLE #x")
	ember.expect(" 473 ember #x :Not cycled, you could not get back in")

	ember.send("REMOVE #x Bee")
	ember.expect(" 482 ember #x ")
	bee.send("REMOVE #x ember :time out")
	ember.expect(":ember!~ember@127.0.0.1 PART #x :Removed by Bee: time out")
	bee.send("FPART ember #x") // the old order
	bee.expect(" 441 Bee ember #x ")

	bee.send("INVITE ember #x")
	ember.expect("INVITE ember #x")
	bee.send("UNINVITE ember #x")
	bee.expect(" 494 Bee #x ember :Uninvited")
	ember.expect(" 493 ember :You were uninvited from #x by Bee")
	ember.send("JOIN #x")
	ember.expect(" 473 ember #x ")
	bee.send("UNINVITE ember #x")
	bee.expect(" 505 Bee ember #x :Is not invited to channel #x")
	ember.send("UNINVITE Bee #x")
	ember.expect(" 442 ember #x ")
}

func TestOperTools(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e, n := login(t, s, "ember"), login(t, s, "nova")
	for _, cmd := range []string{"SAKICK #x ember", "SAQUIT ember :x", "CLEARCHAN #x", "NICKLOCK ember x", "NICKUNLOCK ember", "SETIDLE 5",
		"CHECK ember", "TLINE *@*", "TSCTL ALLTIME", "ALLTIME", "SDESC :x", "JUMPSERVER a.example.net NEW"} {
		e.send(cmd)
		e.expect(" 481 ember ")
	}
	e.send("JOIN #hive")
	e.expect(" 366 ")
	n.send("JOIN #hive")
	n.expect(" 366 ")

	b.send("SAKICK #hive nova :out you go")
	e.expect(":irc.test.local KICK #hive nova :out you go")
	b.expect("Boss used SAKICK to kick nova from #hive (out you go)")
	b.send("SAKICK #hive nova")
	b.expect(" 441 Boss nova #hive ")
	b.send("SAKICK #hive ChanServ")
	b.expect(" 481 Boss :Cannot use this command on a network service")

	b.send("NICKLOCK nova held")
	n.expect(":nova!~nova@127.0.0.1 NICK held")
	b.expect(" 947 Boss held :Nickname now locked.")
	n.send("NICK free")
	n.expect(" 447 held :You may not change your nickname")
	b.send("CHECK held")
	b.expect(" 802 Boss nicklock :locked")
	b.send("NICKUNLOCK held")
	b.expect(" 945 Boss held :Nickname now unlocked.")
	n.send("NICK nova")
	n.expect(" NICK nova")
	b.send("NICKUNLOCK nova")
	b.expect(" 946 Boss nova ")
	b.send("NICKLOCK nova ember")
	b.expect(" 433 Boss ember ")

	b.send("SETIDLE 2h")
	b.expect(" 944 Boss :Idle time set.")
	b.send("WHOIS Boss")
	f := strings.Fields(b.expect(" 317 Boss Boss "))
	if idle, _ := strconv.Atoi(f[4]); idle < 7200 || idle > 7300 {
		t.Errorf("idle after SETIDLE 2h: %v", f)
	}
	b.send("SETIDLE soon")
	b.expect(" 948 Boss :Invalid idle time.")

	b.send("TRACE")
	b.expect(" 204 Boss Oper netadmin Boss [")
	b.expect(" 205 Boss User users ember [~ember@127.0.0.1] ")
	b.expect(" 262 Boss irc.test.local :End of TRACE")
	e.send("TRACE")
	if l := e.expect(" 20"); !strings.Contains(l, " 205 ember User users ember ") {
		t.Errorf("a user's TRACE starts with %q", l)
	}
	e.expect(" 262 ember ")

	b.send("CHECK ember")
	b.expect(" 802 Boss START :ember")
	b.expect(" 802 Boss realnuh :ember!~ember@127.0.0.1")
	b.expect(" 802 Boss onip :127.0.0.1")
	b.expect(" 802 Boss onchans :@#hive")
	b.expect(" 802 Boss END :ember")
	b.send("CHECK #hive")
	b.expect(" 802 Boss membercount :1")
	b.expect(" 802 Boss member :@ember!~ember@127.0.0.1 ember Real")
	b.expect(" 802 Boss END :#hive")
	b.send("CHECK *@127.0.0.1")
	b.expect(" 802 Boss match :1 ")
	b.expect(" 802 Boss matches :3")
	b.send("TLINE 127.0.0.1")
	b.expect("*** TLINE: Counted 3 user(s). Matched '*@127.0.0.1' against 3 user(s) (100.00% of the userbase). 3 by hostname and 0 by IP address.")
	b.send("TLINE *@*.example.org")
	b.expect("against nobody")

	b.send("TSCTL")
	b.expect("use TSCTL ALLTIME")
	b.send("TSCTL ALLTIME")
	b.expect("*** Server=irc.test.local TStime=")
	b.send("ALLTIME")
	b.expect("*** Server=irc.test.local TStime=")

	b.send("SDESC :The new hive")
	b.expect("The description of irc.test.local is now: The new hive")
	b.send("LINKS")
	b.expect(" 364 Boss irc.test.local irc.test.local :0 The new hive")
	b.send("SDESC :" + strings.Repeat("x", 51))
	b.expect("at most 50 characters")

	n.send("JOIN #hive")
	n.expect(" 366 ")
	b.send("JOIN #hive")
	b.expect(" 366 ")
	b.send("CLEARCHAN #hive :spring clean")
	e.expect("KICK #hive ember :spring clean")
	n.expect("KICK #hive nova :spring clean")
	b.expect("*** CLEARCHAN KICK: removed 2 from #hive")
	b.send("NAMES #hive")
	expectNames(t, b.expect(" 353 Boss "), "Boss")

	b.send("SAQUIT nova :gone fishing")
	n.expect("ERROR :Closing Link: 127.0.0.1 (gone fishing)")
	b.expect("Boss used SAQUIT to make nova quit (gone fishing)")
	e.send("JOIN #den")
	e.expect(" 366 ")
	b.send("CLEARCHAN #den KILL bye now")
	e.expect("ERROR :Closing Link: 127.0.0.1 (Killed (Boss (bye now)))")
}

func TestJumpServer(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	mover := login(t, s, "mover")
	b.send("JUMPSERVER")
	b.expect("JumpServer is off")
	b.send("JUMPSERVER other.example.net")
	b.expect("Usage: JUMPSERVER")
	b.send("JUMPSERVER bad_host! NEW")
	b.expect("is not a server name")
	b.send("JUMPSERVER other.example.net:7000 tls.example.net NEW moving day")
	b.expect("JumpServer is on: new connections go to other.example.net:7000 (TLS: tls.example.net:6697)")
	mover.sync() // NEW leaves the people here alone

	late := raw(t, s)
	late.nick = "late"
	late.send("NICK late")
	late.send("USER late 0 * :late")
	late.expect(" 010 late other.example.net 7000 :Please use this Server/Port instead")
	late.expect("ERROR :Closing Link: 127.0.0.1 (moving day)")
	secure := rawTLS(t, s, nil)
	secure.nick = "secure"
	secure.send("NICK secure")
	secure.send("USER secure 0 * :secure")
	secure.expect(" 010 secure tls.example.net 6697 ")

	b.send("JUMPSERVER")
	b.expect("JumpServer is on")
	b.send("JUMPSERVER other.example.net ALL")
	mover.expect(" 010 mover other.example.net 6667 ")
	mover.expect("ERROR :Closing Link: 127.0.0.1 (This server is sending everyone to other.example.net)")
	b.sync() // opers stay
	b.send("JUMPSERVER OFF")
	b.expect("JumpServer is now off.")
	login(t, s, "welcome")
}

func TestWhowasKeptOverRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "keep.db")
	tweak := func(c *config.Config) { c.Services.Database = db }
	s1 := testServer(t, tweak)
	a := login(t, s1, "roamer")
	a.send("QUIT :bye")
	a.expect("ERROR ")
	stopServer(s1)

	s2 := testServer(t, tweak)
	b := login(t, s2, "asker")
	b.send("WHOWAS roamer")
	b.expect(" 314 asker roamer ~roamer 127.0.0.1 * :roamer Real")
	b.send("WHOWAS nobody")
	b.expect(" 406 asker nobody ")
}

// fakeIdentd answers every question with answer ("" = hangs up).
func fakeIdentd(t *testing.T, answer string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			if answer != "" {
				fmt.Fprintf(conn, "%s : USERID : UNIX : %s\r\n", strings.TrimSpace(line), answer)
			}
			conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestIdentLookup(t *testing.T) {
	on := func(c *config.Config) { c.Server.IdentLookup = true }
	port := fakeIdentd(t, "queen bee!")
	s := testServerWith(t, on, func(s *Server) { s.identPort = port })
	x := login(t, s, "drone")
	x.send("WHOIS drone")
	x.expect(" 311 drone drone queenbee 127.0.0.1 * ")

	silent := fakeIdentd(t, "")
	s2 := testServerWith(t, on, func(s *Server) { s.identPort = silent })
	y := raw(t, s2)
	y.nick = "worker"
	y.send("NICK worker")
	y.send("USER worker 0 * :w")
	y.expect("*** No Ident response")
	y.expect(" 001 worker ")
	y.send("WHOIS worker")
	y.expect(" 311 worker worker ~worker 127.0.0.1 * ")

	for line, want := range map[string]string{
		"6193, 23 : USERID : UNIX : stjohns":            "stjohns",
		"6193 , 23 : USERID : OTHER , UTF-8 : a:b":      "ab",
		"6193, 23 : ERROR : NO-USER":                    "",
		"1, 23 : USERID : UNIX : spoof":                 "",
		"6193, 23 : USERID : UNIX : waytoolongusername": "waytoolong",
		"garbage": "",
	} {
		if got := identUser(line, 6193, 23); got != want {
			t.Errorf("identUser(%q) = %q, want %q", line, got, want)
		}
	}
}

// The tools reach people on another server: the command travels to their
// server, which carries it out.
func TestLinkOperTools(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	leaf.seen(t, "op")

	op.send("SAJOIN bob #x")
	bob.expect("JOIN #x")
	bob.expect("*** You were forced to join #x")
	op.send("SAPART bob #x :enough")
	bob.expect("PART #x :enough")
	op.send("SANICK bob bobby")
	bob.expect("NICK bobby")
	hub.seen(t, "bobby")
	op.send("NICKLOCK bobby robert")
	bob.expect("NICK robert")
	op.expect(" 947 op robert :Nickname now locked.")
	bob.send("NICK other")
	bob.expect(" 447 robert ")
	op.send("NICKUNLOCK robert")
	op.expect(" 945 op robert ")
	op.send("NICKUNLOCK robert")
	op.expect(" 946 op robert ")

	op.send("JOIN #y")
	op.expect(" 366 ")
	op.send("SAJOIN robert #y")
	bob.expect("JOIN #y")
	op.expect(":robert!~bob@127.0.0.1 JOIN #y")
	op.send("REMOVE #y robert :bye")
	bob.expect("PART #y :Removed by op: bye")
	op.expect(":robert!~bob@127.0.0.1 PART #y :Removed by op: bye")
	op.send("INVITE robert #y")
	bob.expect("INVITE robert #y")
	op.send("UNINVITE robert #y")
	op.expect(" 494 op #y robert :Uninvited")
	bob.expect(" 493 robert :You were uninvited from #y by op")
	op.send("MODE #y +i")
	op.expect("MODE #y +i")
	waitLink(t, "+i on the leaf", func() (set bool) {
		leaf.s.onLoop(func() { set = leaf.s.net.Channel("#y").Modes['i'] })
		return
	})
	bob.send("JOIN #y")
	bob.expect(" 473 robert #y ")
	op.send("SAJOIN robert #y")
	bob.expect("JOIN #y")
	op.send("SAKICK #y robert :and out")
	bob.expect("KICK #y robert :and out")

	op.send("TSCTL ALLTIME")
	op.expect("*** Server=hub.test.local TStime=")
	op.expect("*** Server=leaf.test.local TStime=")
	op.send("TRACE leaf.test.local")
	op.expect(" 205 op User users robert [~bob@127.0.0.1] ")
	op.expect(" 206 op Serv servers ")
	op.expect(" 262 op leaf.test.local :End of TRACE")
	op.send("SDESC :Hub of the hive")
	waitLink(t, "the leaf hears the new description", func() bool {
		desc := ""
		leaf.s.onLoop(func() {
			if r := leaf.s.links.servers["0HB"]; r != nil {
				desc = r.desc
			}
		})
		return desc == "Hub of the hive"
	})

	// Caller ID is kept by the server of the person with +g.
	ann := login(t, hub.s, "ann")
	leaf.seen(t, "ann")
	bob.send("MODE robert +g")
	bob.expect("MODE robert +g")
	waitLink(t, "+g on the hub", func() bool { u := hub.user("robert"); return u != nil && u.Modes['g'] })
	ann.send("PRIVMSG robert :hi")
	ann.expect(" 716 ann robert :is in +g mode")
	bob.expect(" 718 robert ann ~ann@127.0.0.1 ")
	bob.send("ACCEPT ann")
	bob.send("PING :accepted")
	bob.expect("PONG leaf.test.local :accepted")
	ann.send("PRIVMSG robert :hello")
	bob.expect("PRIVMSG robert :hello")

	op.send("SAQUIT robert :see you")
	bob.expect("ERROR :Closing Link: 127.0.0.1 (see you)")
	waitLink(t, "robert gone", func() bool { return hub.user("robert") == nil })
}

// CLEARCHAN G and Z also keep the people out for an hour.
func TestClearchanBans(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	pest := login(t, s, "pest")
	pest.send("JOIN #pit")
	pest.expect(" 366 ")
	b.send("CLEARCHAN #pit Z :spam run")
	pest.expect("ERROR :Closing Link: 127.0.0.1 (Killed (Boss (spam run)))")
	b.expect("*** CLEARCHAN Z: removed 1 from #pit")
	if l := closedWith(t, s.Addrs()[0].String()); !strings.Contains(l, "Z-lined: spam run") {
		t.Errorf("after CLEARCHAN Z a new connection got %q", l)
	}
	b.sync() // the oper is still here
	b.send("OS ZLINE DEL 127.0.0.1")
	b.expect("127.0.0.1")
	two := login(t, s, "two")
	two.send("JOIN #pit")
	two.expect(" 366 ")
	b.send("CLEARCHAN #pit G :spam again")
	two.expect("ERROR :Closing Link: 127.0.0.1 ")
	b.send("OS AKILL LIST")
	b.expect("*@127.0.0.1")
}
