package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/state"
)

// skipReplay turns off the event replay check, for the load test.
var skipReplay bool

// skipLeakCheck turns off the per-server goroutine check (tests with
// several servers check once for all of them).
var skipLeakCheck bool

// testServer starts a server on free local ports and stops it at test end.
// tweak may change the config first.
func testServer(t *testing.T, tweak func(*config.Config)) *Server {
	t.Helper()
	return testServerWith(t, tweak, nil)
}

// testServerWith also lets setup change the server before it starts.
func testServerWith(t *testing.T, tweak func(*config.Config), setup func(*Server)) *Server {
	t.Helper()
	leakCheck := goroutineBaseline(t)
	if skipLeakCheck {
		// Several servers in one test: the count is checked once for all
		// of them (linkLeakCheck).
		leakCheck = func() {}
	}
	off := false
	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.Server{Name: "irc.test.local", Network: "BeeNet", SID: "0BE", ResolveHosts: &off},
		Listen: []config.Listen{{Address: "127.0.0.1:0"}, {Address: "127.0.0.1:0", TLS: true}},
		TLS:    config.TLS{Cert: filepath.Join(dir, "cert.pem"), Key: filepath.Join(dir, "key.pem"), SelfSigned: true},
		MOTD:   config.MOTD{Text: "Welcome to BeeNet\nbe kind, stay curious"},
		// Cloaks off and room for fast test clients; tests of those turn them back.
		Cloak:    config.Cloak{Enabled: &off, Secret: "0123456789abcdef-test"},
		Limits:   config.Limits{FloodBurst: 10000, FloodRate: 10000, SessionLimit: 1000},
		Services: config.Services{Database: filepath.Join(dir, "services.db"), RegDelay: &config.Duration{}, ForceEmail: &off, MemoSendDelay: &config.Duration{}},
		Update:   config.Update{Check: &off},
		GeoIP:    config.GeoIP{Enabled: &off},
	}
	if tweak != nil {
		tweak(cfg)
	}
	cfg.SetDefaults()
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	s, stopGate, err := newTestServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(s)
	}
	// A copy of the network fed only with events must end up the same as
	// the real one: proof that every change went through an event, which
	// server links will depend on.
	replica := s.net.Clone()
	var replayErr error
	s.beforeEmit = func(ev state.Event) {
		if replayErr != nil || skipReplay {
			return
		}
		if ok, where := state.Equal(s.net, replica); !ok {
			replayErr = fmt.Errorf("before %T: network built from events differs from the real one: %s", ev, where)
		}
	}
	s.sinks = append(s.sinks, func(ev state.Event) {
		if err := replica.Apply(state.CopyEvent(ev)); err != nil && replayErr == nil {
			replayErr = fmt.Errorf("replaying %T: %v", ev, err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { s.Serve(ctx); close(stopped) }()
	stop := func() {
		checked := make(chan struct{})
		s.post(func() {
			defer close(checked)
			if skipReplay {
				// The load test skips the comparison: it would cost more than the work measured.
			} else if replayErr != nil {
				t.Error(replayErr)
			} else if ok, where := state.Equal(s.net, replica); !ok {
				t.Errorf("network built from events differs from the real one: %s", where)
			}
			if err := s.store.VerifyCache(); err != nil {
				t.Error(err)
			}
		})
		select {
		case <-checked:
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("end-of-test checks did not run")
		}
		cancel()
		select {
		case <-stopped:
			stopGate()
			leakCheck()
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}
	var once sync.Once
	stopFns[s] = func() { once.Do(stop) }
	t.Cleanup(func() {
		stopFns[s]()
		delete(stopFns, s)
	})
	return s
}

// newTestServer makes a test server; the func it returns stops what it
// started besides the server, after the server stopped. With
// BEEIRCD_TEST_GATE set, gate_linux_test.go puts the client ports in a gate.
var newTestServer = func(cfg *config.Config) (*Server, func(), error) {
	s, err := New(cfg, log.New(io.Discard, "", 0))
	return s, func() {}, err
}

// stopFns stop test servers early; tests run one at a time.
var stopFns = map[*Server]func(){}

// stopServer runs the end-of-test checks on s and stops it now.
func stopServer(s *Server) { stopFns[s]() }

type tc struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	nick string
}

// raw connects without registering.
func raw(t *testing.T, s *Server) *tc {
	t.Helper()
	conn, err := net.Dial("tcp", s.Addrs()[0].String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &tc{t: t, conn: conn, r: bufio.NewReader(conn)}
}

// join connects and registers as nick, reading up to the end of the MOTD.
func login(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick + " Real")
	c.expect(":" + nick + " MODE " + nick + " +i")
	return c
}

func (c *tc) send(line string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s\r\n", line); err != nil {
		c.t.Fatalf("%s send %q: %v", c.nick, line, err)
	}
}

// expect reads until a line contains want and returns it.
func (c *tc) expect(want string) string {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var seen []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("%s: waiting for %q: %v\nseen:\n%s", c.nick, want, err, strings.Join(seen, ""))
		}
		c.check(line)
		if strings.Contains(line, want) {
			return strings.TrimRight(line, "\r\n")
		}
		seen = append(seen, line)
	}
}

// quiet checks that no line containing bad arrives for a short while.
func (c *tc) quiet(bad string) {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return
		}
		c.check(line)
		if strings.Contains(line, bad) {
			c.t.Fatalf("%s got unexpected %q", c.nick, strings.TrimSpace(line))
		}
	}
}

// sync makes sure everything sent so far was handled.
func (c *tc) sync() {
	c.t.Helper()
	c.send("PING :sync")
	c.expect("PONG irc.test.local :sync")
}

func TestRegistration(t *testing.T) {
	s := testServer(t, nil)
	c := raw(t, s)
	c.send("CAP LS 302")
	c.expect("CAP * LS")
	c.send("NICK Bee")
	c.send("USER bee 0 * :The Bee")
	c.quiet(" 001 ") // CAP not finished yet
	c.send("CAP REQ :draft/bogus")
	c.expect("CAP Bee NAK :draft/bogus")
	c.send("CAP END")
	c.expect(":irc.test.local 001 Bee :Welcome to BeeNet, Bee")
	c.expect(" 002 Bee :Your host is irc.test.local, running BeeIRCd")
	c.expect(" 004 Bee irc.test.local BeeIRCd-")
	l := c.expect(" 005 Bee ") + c.expect(" 005 Bee ")
	for _, tok := range []string{"CASEMAPPING=ascii", "PREFIX=(ov)@+", "CHANMODES=beI,k,BEFHJLWfl,CDGKMNOPQRSTUVZcimnprstuz", "NETWORK=BeeNet", "STATUSMSG=@+"} {
		if !strings.Contains(l, tok) {
			t.Errorf("005 lacks %s: %s", tok, l)
		}
	}
	c.expect(" 251 Bee :There are 0 users and 1 invisible on 1 servers")
	c.expect(" 375 Bee :- irc.test.local Message of the Day -")
	c.expect(" 372 Bee :- be kind, stay curious")
	c.expect(" 376 Bee :End of /MOTD command.")
	c.expect(":Bee MODE Bee +i")

	c.send("USER again 0 * :x")
	c.expect(" 462 Bee :You may not reregister")
	c.send("FLY away")
	c.expect(" 421 Bee FLY :Unknown command")
	c.send("WHOIS Bee")
	c.expect(" 311 Bee Bee ~bee 127.0.0.1 * :The Bee")
}

func TestBeforeRegistration(t *testing.T) {
	s := testServer(t, nil)
	login(t, s, "Bee")
	c := raw(t, s)
	c.send("JOIN #a")
	c.expect(" 451 * :You have not registered")
	c.send("NICK 1bad")
	c.expect(" 432 * 1bad :Erroneous nickname")
	c.send("NICK bee")
	c.expect(" 433 * bee :Nickname is already in use")
	c.send("NICK")
	c.expect(" 431 * :No nickname given")
	c.send("USER u 0 *")
	c.expect(" 461 * USER :Not enough parameters")
	c.send("NICK ember")
	c.send("USER ~!@# 0 * :x")
	c.expect(" 001 ember ")
	c.send("WHOIS ember")
	c.expect(" 311 ember ember ~user ")
}

func TestNickChange(t *testing.T) {
	s := testServer(t, nil)
	bee, ember, nova := login(t, s, "Bee"), login(t, s, "ember"), login(t, s, "nova")
	bee.send("JOIN #bchat")
	bee.expect(" 366 Bee #bchat ")
	ember.send("JOIN #bchat")
	ember.expect("JOIN #bchat")
	bee.expect("ember!~ember@127.0.0.1 JOIN #bchat")
	bee.send("NICK Bee_")
	bee.expect(":Bee!~bee@127.0.0.1 NICK Bee_")
	ember.expect(":Bee!~bee@127.0.0.1 NICK Bee_")
	nova.quiet("NICK Bee_")
	ember.send("NICK bee_")
	ember.expect(" 433 ember bee_ :Nickname is already in use")
	bee.send("NICK BEE_")
	bee.expect(" NICK BEE_")
	nova.send("WHOWAS Bee")
	nova.expect(" 314 nova Bee ~bee 127.0.0.1 * :Bee Real")
	nova.expect(" 369 nova Bee :End of WHOWAS")
}

func TestChannelBasics(t *testing.T) {
	s := testServer(t, nil)
	bee, ember, nova := login(t, s, "Bee"), login(t, s, "ember"), login(t, s, "nova")

	bee.send("JOIN #BChat")
	bee.expect(":Bee!~bee@127.0.0.1 JOIN #BChat")
	bee.expect(" 353 Bee = #BChat :@Bee")
	bee.expect(" 366 Bee #BChat :End of /NAMES list.")

	bee.send("TOPIC #bchat :Welcome to #bchat · be kind")
	bee.expect(" TOPIC #BChat :Welcome to #bchat · be kind")

	ember.send("JOIN #bchat")
	ember.expect(" 332 ember #BChat :Welcome to #bchat · be kind")
	ember.expect(" 333 ember #BChat Bee ")
	expectNames(t, ember.expect(" 353 ember = #BChat :"), "@Bee ember")

	ember.send("PRIVMSG #bchat :hi all")
	bee.expect(":ember!~ember@127.0.0.1 PRIVMSG #BChat :hi all")
	ember.quiet("PRIVMSG #BChat :hi all") // no echo to the sender

	nova.send("PRIVMSG #bchat :from outside")
	nova.expect(" 404 nova #BChat :Cannot send to channel (no external messages)")

	ember.send("TOPIC #bchat :mine now")
	ember.expect(" 482 ember #BChat :You're not channel operator")

	bee.send("MODE #bchat")
	bee.expect(" 324 Bee #BChat +nt")
	bee.expect(" 329 Bee #BChat ")

	nova.send("LIST")
	nova.expect(" 321 nova Channel :Users  Name")
	nova.expect(" 322 nova #BChat 2 :[+nt] Welcome to #bchat · be kind")
	nova.expect(" 323 nova :End of /LIST")

	nova.send("NAMES #bchat")
	nova.expect(" 366 nova #BChat :End of /NAMES list.") // everyone is +i and nova is outside

	ember.send("PART #bchat :bye now")
	bee.expect(":ember!~ember@127.0.0.1 PART #BChat :bye now")
	ember.expect("PART #BChat :bye now")
	ember.send("PART #bchat")
	ember.expect(" 442 ember #BChat :You're not on that channel")
	ember.send("PART #nowhere")
	ember.expect(" 403 ember #nowhere :No such channel")
	ember.send("JOIN bad")
	ember.expect(" 479 ember bad :Illegal channel name")

	bee.send("QUIT :see you")
	bee.expect("ERROR :Closing Link: 127.0.0.1 (Quit: see you)")
	ember.send("LIST")
	ember.expect(" 323 ember ")
	ember.quiet("#BChat") // the empty channel is gone
}

func TestPrivateMessagesAndAway(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("PRIVMSG ember :hello ember")
	ember.expect(":Bee!~bee@127.0.0.1 PRIVMSG ember :hello ember")
	bee.send("PRIVMSG nobody :hi")
	bee.expect(" 401 Bee nobody :No such nick/channel")
	bee.send("NOTICE nobody :hi")
	bee.quiet(" 401 ")
	bee.send("PRIVMSG ember")
	bee.expect(" 412 Bee :No text to send")

	ember.send("AWAY :lunch")
	ember.expect(" 306 ember :You have been marked as being away")
	bee.send("PRIVMSG ember :you there?")
	bee.expect(" 301 Bee ember :lunch")
	bee.send("WHOIS ember")
	bee.expect(" 311 Bee ember ~ember 127.0.0.1 * :ember Real")
	bee.expect(" 312 Bee ember irc.test.local :BeeNet IRC")
	bee.expect(" 301 Bee ember :lunch")
	bee.expect(" 317 Bee ember ")
	bee.expect(" 318 Bee ember :End of /WHOIS list.")
	bee.send("USERHOST ember Bee")
	bee.expect(" 302 Bee :ember=-~ember@127.0.0.1 Bee=+~bee@127.0.0.1")
	ember.send("AWAY")
	ember.expect(" 305 ember :You are no longer marked as being away")
	bee.send("ISON ember nobody Bee")
	bee.expect(" 303 Bee :ember Bee")
}

func TestModesAndKick(t *testing.T) {
	s := testServer(t, nil)
	bee, ember, nova := login(t, s, "Bee"), login(t, s, "ember"), login(t, s, "nova")
	for _, c := range []*tc{bee, ember, nova} {
		c.send("JOIN #a")
		c.expect(" 366 " + c.nick + " #a ")
	}
	bee.expect("nova!~nova@127.0.0.1 JOIN #a")

	bee.send("MODE #a +ov-n ember nova")
	nova.expect(":Bee!~bee@127.0.0.1 MODE #a +ov-n ember nova")
	nova.send("NAMES #a")
	expectNames(t, nova.expect(" 353 nova = #a :"), "@Bee @ember +nova")

	bee.send("MODE #a +m")
	nova.expect("MODE #a +m")
	ember.sync()
	nova.send("PRIVMSG #a :voiced can talk")
	ember.expect("PRIVMSG #a :voiced can talk")
	bee.send("MODE #a -v nova")
	nova.expect("MODE #a -v nova")
	nova.send("PRIVMSG #a :now muted")
	nova.expect(" 404 nova #a :Cannot send to channel (+m)")

	nova.send("MODE #a +t")
	nova.expect(" 482 nova #a :You're not channel operator")
	nova.send("MODE #a +Y")
	nova.expect(" 472 nova Y :is unknown mode char to me for #a")

	bee.send("MODE #a +k secret")
	nova.expect("MODE #a +k secret")
	bee.send("MODE #a +l 3")
	nova.expect("MODE #a +l 3")
	bee.send("MODE #a")
	bee.expect(" 324 Bee #a +klmt secret 3")

	bee.send("KICK #a nova :be nice")
	nova.expect(":Bee!~bee@127.0.0.1 KICK #a nova :be nice")
	ember.expect("KICK #a nova :be nice")
	nova.send("JOIN #a")
	nova.expect(" 475 nova #a :Cannot join channel (+k)")
	nova.send("JOIN #a secret")
	nova.expect(" 366 nova #a ")

	x := login(t, s, "x")
	x.send("JOIN #a secret")
	x.expect(" 471 x #a :Cannot join channel (+l)")

	bee.send("MODE #a -kl *")
	bee.expect("MODE #a -kl secret")

	nova.send("KICK #a Bee")
	nova.expect(" 482 nova #a :You're not channel operator")

	bee.send("MODE nova +i")
	bee.expect(" 502 Bee :Can't change mode for other users")
	bee.send("MODE Bee -i+w")
	bee.expect(":Bee MODE Bee -i+w")
	bee.send("MODE Bee")
	bee.expect(" 221 Bee +w")
	bee.send("MODE Bee +o")
	bee.quiet("MODE Bee +o")
}

func TestBansExceptsAndInvites(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #a")
	bee.expect(" 366 Bee #a ")
	bee.send("MODE #a +b ember")
	bee.expect("MODE #a +b ember!*@*")
	ember.send("JOIN #a")
	ember.expect(" 474 ember #a :Cannot join channel (+b)")
	bee.send("MODE #a +e *!~ember@*")
	bee.expect("MODE #a +e *!~ember@*")
	ember.send("JOIN #a")
	ember.expect(" 366 ember #a ")
	ember.send("MODE #a b")
	ember.expect(" 367 ember #a ember!*@* Bee ")
	ember.expect(" 368 ember #a :End of Channel Ban List")
	ember.send("MODE #a e")
	ember.expect(" 482 ember #a :You're not channel operator")
	bee.send("MODE #a -e *!~ember@*")
	bee.expect("MODE #a -e *!~ember@*")
	ember.send("PRIVMSG #a :still here?")
	ember.expect(" 404 ember #a :Cannot send to channel (you're banned)")

	bee.send("PART #a")
	bee.send("JOIN #b")
	bee.send("MODE #b +i")
	bee.expect("MODE #b +i")
	ember.send("JOIN #b")
	ember.expect(" 473 ember #b :Cannot join channel (+i)")
	bee.send("INVITE ember #b")
	bee.expect(" 341 Bee ember #b")
	ember.expect(":Bee!~bee@127.0.0.1 INVITE ember #b")
	ember.send("JOIN #b")
	ember.expect(" 366 ember #b ")
	ember.send("PART #b")
	ember.send("JOIN #b")
	ember.expect(" 473 ember #b ") // an invite works once

	bee.send("MODE #b +I ember")
	bee.expect("MODE #b +I ember!*@*")
	ember.send("JOIN #b")
	ember.expect(" 366 ember #b ")

	bee.send("MODE #b +R")
	bee.expect("MODE #b +R")
	n := login(t, s, "nova")
	n.send("JOIN #b")
	n.expect(" 473 nova #b ")
	bee.send("MODE #b -i")
	bee.expect("MODE #b -i")
	n.send("JOIN #b")
	n.expect(" 477 nova #b :You need to identify to a registered nick to join that channel")
}

func TestStatusMessages(t *testing.T) {
	s := testServer(t, nil)
	bee, ember, nova := login(t, s, "Bee"), login(t, s, "ember"), login(t, s, "nova")
	for _, c := range []*tc{bee, ember, nova} {
		c.send("JOIN #a")
		c.expect(" 366 " + c.nick + " #a ")
	}
	bee.send("MODE #a +o ember")
	nova.expect("MODE #a +o ember")
	bee.send("PRIVMSG @#a :ops only")
	ember.expect("PRIVMSG @#a :ops only")
	nova.quiet("ops only")
}

func TestWho(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #a")
	bee.expect(" 366 Bee #a ")
	ember.send("JOIN #a")
	ember.expect(" 366 ember #a ")
	ember.send("AWAY :out")
	ember.expect(" 306 ")
	bee.send("WHO #a")
	bee.expect(" 352 Bee #a ~bee 127.0.0.1 irc.test.local Bee H@ :0 Bee Real")
	bee.expect(" 352 Bee #a ~ember 127.0.0.1 irc.test.local ember G :0 ember Real")
	bee.expect(" 315 Bee #a :End of /WHO list.")
	x := login(t, s, "x")
	x.send("WHO *")
	x.expect(" 352 x * ~x ")
	x.quiet(" 352 x * ~bee ") // invisible and no shared channel
}

func TestWhox(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #a")
	bee.expect(" 366 Bee #a ")
	ember.send("JOIN #a")
	ember.expect(" 366 ember #a ")
	ember.send("AWAY :out")
	ember.expect(" 306 ")
	// %tcnfar,42: fields come back in WHOX order, whatever order was asked.
	bee.send("WHO #a %rafnct,42")
	bee.expect(" 354 Bee 42 #a Bee H@ 0 :Bee Real")
	bee.expect(" 354 Bee 42 #a ember G 0 :ember Real")
	bee.expect(" 315 Bee #a :End of /WHO list.")
	// No realname: the reply has no trailing part; a bad token reads 0.
	bee.send("WHO ember %tna,abcd")
	bee.expect(" 354 Bee 0 ember 0")
	bee.expect(" 315 Bee ember ")
	// Only you (and opers) see a real IP.
	bee.send("WHO ember %ni")
	bee.expect(" 354 Bee 255.255.255.255 ember")
	ember.send("WHO ember %ni")
	ember.expect(" 354 ember 127.0.0.1 ember")
	// o before the % still means opers only; a plain WHO is unchanged.
	bee.send("WHO #a o%n")
	bee.quiet(" 354 Bee ember")
	bee.send("WHO #a")
	bee.expect(" 352 Bee #a ~bee ")
	bee.send("VERSION")
	bee.expect(" WHOX ")
}

func TestPingTimeout(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.Limits.PingInterval.Duration = 150 * time.Millisecond
		c.Limits.PingTimeout.Duration = 150 * time.Millisecond
	})
	quiet := login(t, s, "sleepy")
	alive := login(t, s, "alive")
	alive.send("JOIN #a")
	alive.expect(" 366 alive #a ")
	quiet.send("JOIN #a")
	alive.expect("sleepy!~sleepy@127.0.0.1 JOIN #a")
	quiet.expect("PING :irc.test.local")
	alive.expect("PING :irc.test.local")
	alive.send("PONG :irc.test.local")
	quiet.expect("ERROR :Closing Link: 127.0.0.1 (Ping timeout: ")
	alive.expect(":sleepy!~sleepy@127.0.0.1 QUIT :Ping timeout: ")
}

func TestRegistrationTimeout(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Limits.RegisterTimeout.Duration = 100 * time.Millisecond })
	c := raw(t, s)
	c.send("NICK slow")
	c.expect("ERROR :Closing Link: 127.0.0.1 (Registration timed out)")
}

func TestLongLines(t *testing.T) {
	s := testServer(t, nil)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	// 509 bytes in is fine, but with Bee's prefix it is too long to relay
	// whole: it is cut on a character boundary.
	bee.send("PRIVMSG ember :" + strings.Repeat("é", 247))
	got := ember.expect("PRIVMSG ember :")
	if len(got) > 510 || !strings.HasSuffix(got, "é") {
		t.Errorf("line not cut cleanly: %d bytes, ends %q", len(got), got[len(got)-4:])
	}
	bee.send("PRIVMSG ember :" + strings.Repeat("x", 9000))
	bee.expect(" 417 Bee :Input line was too long")
	bee.send("PRIVMSG ember :" + strings.Repeat("x", 496)) // 511 bytes: one too many
	bee.expect(" 417 Bee :Input line was too long")
	bee.sync()
}

func TestTLS(t *testing.T) {
	s := testServer(t, nil)
	conn, err := tls.Dial("tcp", s.Addrs()[1].String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &tc{t: t, conn: conn, r: bufio.NewReader(conn), nick: "safe"}
	c.send("NICK safe")
	c.send("USER safe 0 * :Safe")
	c.expect(" 001 safe ")
	c.send("WHOIS safe")
	c.expect(" 671 safe safe :is using a secure connection")
	if cert := conn.ConnectionState().PeerCertificates[0]; cert.Subject.CommonName != "irc.test.local" {
		t.Errorf("certificate for %q", cert.Subject.CommonName)
	}
}

func TestShutdownTellsEveryone(t *testing.T) {
	off := false
	cfg := &config.Config{
		Server:   config.Server{Name: "irc.test.local", Network: "BeeNet", SID: "0BE", ResolveHosts: &off},
		Listen:   []config.Listen{{Address: "127.0.0.1:0"}},
		Cloak:    config.Cloak{Enabled: &off},
		Services: config.Services{Database: filepath.Join(t.TempDir(), "services.db")},
		GeoIP:    config.GeoIP{Enabled: &off},
	}
	cfg.SetDefaults()
	s, stopGate, err := newTestServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopGate)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx); close(done) }()
	c := login(t, s, "Bee")
	c.send("MOTD")
	c.expect(" 422 Bee :MOTD File is missing")
	cancel()
	c.expect("ERROR :Closing Link: 127.0.0.1 (Server shutting down)")
	<-done
}

func TestHostLookup(t *testing.T) {
	on := true
	s := testServerWith(t, func(c *config.Config) { c.Server.ResolveHosts = &on }, func(s *Server) {
		s.resolve = func(ctx context.Context, ip string) (string, bool) { return "bee.example.org", true }
	})
	c := raw(t, s)
	c.expect("NOTICE * :*** Looking up your hostname...")
	c.expect("NOTICE * :*** Found your hostname")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.expect(" 001 Bee ")
	c.send("WHOIS Bee")
	c.expect(" 311 Bee Bee ~bee bee.example.org * :Bee")
}

// expectNames checks a 353 line holds exactly want (NAMES order is not
// fixed, as in InspIRCd).
func expectNames(t *testing.T, line, want string) {
	t.Helper()
	_, got, _ := strings.Cut(line, " :")
	g, w := strings.Fields(got), strings.Fields(want)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, " ") != strings.Join(w, " ") {
		t.Fatalf("NAMES: got %q, want %q", got, want)
	}
}

func TestForwardBan(t *testing.T) {
	s := testServer(t, nil)
	bee, x := login(t, s, "Bee"), login(t, s, "x")
	bee.send("JOIN #a")
	bee.expect(" 366 Bee #a ")
	bee.send("MODE #a +b ~f:#jail:x")
	bee.expect("MODE #a +b ~f:#jail:x!*@*")
	bee.send("MODE #a +b ~f:#a:nobody")
	bee.expect("MODE #a +b ~f:#a:nobody!*@*")
	x.send("JOIN #a")
	x.expect(" 470 x #a #jail :[Link] #a has you banned, so you are automatically being transferred to #jail")
	x.expect(":x!~x@127.0.0.1 JOIN #jail")
	bee.quiet("JOIN #a")
	// An exception lets them in; a forward to the channel itself is a plain ban.
	bee.send("MODE #a +e x!*@*")
	bee.expect("MODE #a +e x!*@*")
	x.send("JOIN #a")
	x.expect(":x!~x@127.0.0.1 JOIN #a")
	nobody := login(t, s, "nobody")
	nobody.send("JOIN #a")
	nobody.expect(" 474 nobody #a :Cannot join channel (+b)")
}
