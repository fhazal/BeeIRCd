package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/gate"
	"github.com/fhazal/beeircd/internal/state"
)

// These tests hand a server's clients to a second server inside the test
// process, without the gate: the connections go along as they are. What
// the gate adds (sockets passed between processes, TLS and WebSocket
// clients behind pipes) is tested on Linux in handover_linux_test.go and
// in internal/gate.

// handed is a hand-over caught by a test.
type handed struct {
	snapshot []byte
	conns    map[string]net.Conn
}

// resume is what the next server gets: the same connections.
func (h *handed) resume(done func(kept int, problem string)) *resume {
	if done == nil {
		done = func(int, string) {}
	}
	return &resume{
		snapshot: h.snapshot,
		has:      func(id string) bool { return h.conns[id] != nil },
		conn: func(id string, info *gate.Info) (net.Conn, error) {
			c := h.conns[id]
			if c == nil {
				return nil, fmt.Errorf("no client %s", id)
			}
			delete(h.conns, id)
			return c, nil
		},
		close: func() {
			for id, c := range h.conns {
				handoverDrop(c, "*", "Server restarting")
				delete(h.conns, id)
			}
		},
		done: done,
	}
}

// handOver makes old hand its clients over and waits until it has
// stopped. before runs on old's loop when readers and writers have
// stopped, just before the snapshot.
func handOver(t *testing.T, old *Server, result chan error, before func()) *handed {
	t.Helper()
	got := make(chan *handed, 1)
	old.post(func() {
		old.hand.keepConns = true
		old.hand.beforeSnap = before
		old.hand.give = func(b []byte, conns map[string]net.Conn) error {
			got <- &handed{append([]byte(nil), b...), conns}
			return nil
		}
		old.startHandover()
	})
	var h *handed
	select {
	case h = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the old server never handed over")
	}
	select {
	case err := <-result:
		result <- err // for the server's own cleanup
		if !errors.Is(err, ErrHandover) {
			t.Fatalf("old server's Serve returned %v, want ErrHandover", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the old server didn't stop after handing over")
	}
	return h
}

// serveNext starts the next server on the old one's config file and runs
// it; the network is checked against a copy fed only with events, as for
// every test server.
func serveNext(t *testing.T, path string, r *resume) *Server {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newServer(cfg, log.New(io.Discard, "", 0), nil, r)
	if err != nil {
		t.Fatal(err)
	}
	runNext(t, s, func() {})
	return s
}

// runNext serves s until the test ends, with the end-of-test checks.
func runNext(t *testing.T, s *Server, stopGate func()) {
	t.Helper()
	replica := s.net.Clone()
	var replayErr error
	s.sinks = append(s.sinks, func(ev state.Event) {
		if err := replica.Apply(state.CopyEvent(ev)); err != nil && replayErr == nil {
			replayErr = fmt.Errorf("replaying %T: %v", ev, err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { s.Serve(ctx); close(stopped) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			checked := make(chan struct{})
			s.post(func() {
				defer close(checked)
				if replayErr != nil {
					t.Error(replayErr)
				} else if ok, where := state.Equal(s.net, replica); !ok {
					t.Errorf("after the hand-over the network built from events differs from the real one: %s", where)
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
			case <-time.After(5 * time.Second):
				t.Error("the next server did not stop")
			}
		})
	}
	stopFns[s] = stop
	t.Cleanup(func() {
		stop()
		delete(stopFns, s)
	})
}

// onLoop runs f on s's loop and waits for it.
func onLoop(t *testing.T, s *Server, f func()) {
	t.Helper()
	done := make(chan struct{})
	s.post(func() { defer close(done); f() })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop didn't run the test's job")
	}
}

// gone checks that the server closed c's connection, after an ERROR line.
func (c *tc) gone() {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	sawError := false
	for {
		line, err := c.r.ReadString('\n')
		if strings.HasPrefix(line, "ERROR :Closing Link") {
			sawError = true
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "aborted") {
				c.t.Fatalf("%s: still connected: %v", c.nick, err)
			}
			if !sawError {
				c.t.Errorf("%s was disconnected without an ERROR line", c.nick)
			}
			return
		}
	}
}

// The whole thing: people in channels, with their own settings, an oper,
// a logged-in account, someone in the middle of logging in and someone in
// the middle of a line. After the hand-over all of them go on as if
// nothing happened.
func TestHandoverKeepsEveryone(t *testing.T) {
	s, path, result := fileServer(t, "")
	b := boss(t, s)
	alice := registered(t, s, "Alice")
	alice.send("CAP REQ :echo-message server-time away-notify")
	alice.expect("CAP Alice ACK")
	bob := login(t, s, "Bob")
	dave := login(t, s, "Dave")

	alice.send("JOIN #Hive")
	alice.expect(" 366 Alice #Hive ")
	alice.send("MODE #Hive +mk honey")
	alice.expect("MODE #Hive +mk honey")
	alice.send("TOPIC #Hive :bees only")
	alice.expect("TOPIC #Hive :bees only")
	alice.send("MODE #Hive +b *!*@wasp.example")
	alice.expect("MODE #Hive +b *!*@wasp.example")
	bob.send("JOIN #hive honey")
	bob.expect(" 366 Bob #Hive ")
	dave.send("JOIN #hive honey")
	dave.expect(" 366 Dave #Hive ")
	alice.send("MODE #Hive +vv Bob Dave")
	alice.expect("MODE #Hive +vv Bob Dave")
	alice.send("MODE #Hive +H 50:1h")
	alice.expect("MODE #Hive +H ")
	bob.send("PRIVMSG #hive :for the record")
	alice.expect("PRIVMSG #Hive :for the record")
	bob.send("JOIN #side")
	bob.expect(" 366 Bob #side ")
	b.send("JOIN #side")
	b.expect(" 366 Boss #side ")

	bob.send("AWAY :gone fishing")
	bob.expect(" 306 Bob ")
	alice.expect("AWAY :gone fishing") // away-notify
	alice.send("MONITOR + Carol,Bob,nobody")
	alice.expect(" 730 Alice :Bob!")
	alice.expect(" 731 Alice :")
	dave.send("SILENCE +*!*@spam.example")
	dave.sync()
	alice.send("INVITE Boss #Hive")
	b.expect("INVITE Boss #Hive")

	// Carol is half-way through logging in: NICK is in, USER isn't.
	carol := raw(t, s)
	carol.nick = "Carol"
	carol.send("CAP LS 302")
	carol.expect("CAP * LS")
	carol.send("CAP REQ :server-time")
	carol.expect("CAP * ACK")
	carol.send("NICK Carol")
	// Erin sent AUTHENTICATE PLAIN and owes the server her password.
	erin := raw(t, s)
	erin.nick = "Erin"
	erin.send("CAP REQ :sasl")
	erin.expect("CAP * ACK")
	erin.send("NICK Erin")
	erin.send("USER erin 0 * :Erin")
	erin.send("AUTHENTICATE PLAIN")
	erin.expect("AUTHENTICATE +")

	for _, c := range []*tc{b, alice, bob, dave} {
		c.sync()
	}
	// Dave is in the middle of a line.
	fmt.Fprintf(dave.conn, "PRIVMSG #hive :hel")
	var oldUIDs map[string]string
	recorded := 0
	h := handOver(t, s, result, func() {
		recorded = s.hist.store.Lines("#hive")
		oldUIDs = map[string]string{}
		for _, u := range s.net.Users() {
			oldUIDs[u.Nick] = u.UID
		}
		// Output the old server never got to send goes along.
		s.clients[s.net.UserByNick("alice").UID].send(":irc.test.local NOTICE Alice :sent by the old server")
	})
	snap, err := decodeHandover(h.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Clients) != 6 || len(h.conns) != 6 {
		t.Fatalf("the hand-over holds %d clients and %d connections, want 6", len(snap.Clients), len(h.conns))
	}
	kept := -1
	n := serveNext(t, path, h.resume(func(k int, problem string) { kept = k }))
	if kept != 6 {
		t.Fatalf("the next server took over %d clients, want 6", kept)
	}
	// beeircd update reads the result from the status.
	var st *control.Status
	n.onLoop(func() { st = n.statusInfo() })
	if h := st.Handover; h == nil || h.Problem != "" || h.Kept != h.Of || h.Kept == 0 {
		t.Errorf("status after the hand-over: %+v", h)
	}

	alice.expect("NOTICE Alice :sent by the old server")
	// The rest of Dave's line arrives at the next server.
	fmt.Fprintf(dave.conn, "lo there\r\n")
	alice.expect(":Dave!~dave@127.0.0.1 PRIVMSG #Hive :hello there")
	bob.expect(":Dave!~dave@127.0.0.1 PRIVMSG #Hive :hello there")
	// Channel, modes, topic, ban list, members and their status.
	bob.send("PRIVMSG #hive :voiced people may talk")
	if line := alice.expect("PRIVMSG #Hive :voiced people may talk"); !strings.HasPrefix(line, "@time=") {
		t.Errorf("alice lost server-time: %q", line)
	}
	alice.send("PRIVMSG #hive :echo")
	alice.expect(":Alice!~alice@127.0.0.1 PRIVMSG #Hive :echo") // echo-message
	alice.send("MODE #hive")
	if line := alice.expect(" 324 Alice #Hive +Hkmnt "); !strings.HasSuffix(line, " honey") {
		t.Errorf("channel modes after the hand-over: %q", line)
	}
	alice.send("TOPIC #hive")
	alice.expect(" 332 Alice #Hive :bees only")
	alice.send("MODE #hive b")
	alice.expect(" 367 Alice #Hive *!*@wasp.example")
	alice.send("NAMES #hive")
	if line := alice.expect(" 353 Alice "); !strings.Contains(line, "@Alice") || !strings.Contains(line, "+Bob") || !strings.Contains(line, "+Dave") {
		t.Errorf("NAMES after the hand-over: %q", line)
	}
	// Away, account, oper, invite, silence.
	alice.send("WHOIS Bob")
	alice.expect(" 301 Alice Bob :gone fishing")
	alice.expect(" 318 Alice Bob ")
	bob.send("WHOIS Alice")
	bob.expect(" 330 Bob Alice Alice :is logged in as")
	bob.expect(" 318 Bob Alice ")
	alice.send("NS INFO Alice")
	alice.expect("Last seen     online now") // services know she is here, logged in
	b.send("JOIN #hive")                     // invited before the hand-over: no key needed
	b.expect(" 366 Boss #Hive ")
	b.send("OS STATS")
	b.expect(osLine("Boss", "")) // still an oper for OperServ
	b.send("WHOIS Boss")
	b.expect(" 313 Boss Boss ")
	b.expect(" 318 Boss Boss ")
	dave.send("SILENCE")
	dave.expect(" 271 Dave ")
	// MONITOR: Carol finishes logging in on the next server.
	carol.send("USER carol 0 * :Carol")
	carol.send("CAP END")
	carol.expect(" 001 Carol ")
	alice.expect(" 730 Alice :Carol!")
	carol.send("CAP LIST")
	if line := carol.expect("CAP Carol LIST :"); !strings.Contains(line, "server-time") { // asked for before the hand-over
		t.Errorf("carol lost her capabilities: %q", line)
	}
	// SASL goes on where it stopped (wrong password: 904, then in).
	erin.send("AUTHENTICATE " + "AGVyaW4Abm9wZQ==") // \0erin\0nope
	erin.expect(" 904 ")
	erin.send("CAP END")
	erin.expect(" 001 Erin ")

	// The next server's books match.
	if recorded == 0 {
		t.Error("test setup: the old server recorded no channel history")
	}
	onLoop(t, n, func() {
		// Recorded before the hand-over, plus the three lines said since.
		if got := n.hist.store.Lines("#hive"); got != recorded+3 {
			t.Errorf("channel history: %d lines, want the %d from before and 3 new ones", got, recorded)
		}
		if n.local != 6 || len(n.clients) != 6 {
			t.Errorf("next server: %d local, %d clients, want 6", n.local, len(n.clients))
		}
		if n.perIP["127.0.0.1"] != 6 {
			t.Errorf("connections per IP: %d, want 6", n.perIP["127.0.0.1"])
		}
		if n.throttle.unregAll != 0 {
			t.Errorf("%d clients still count as logging in", n.throttle.unregAll)
		}
		boss := n.net.UserByNick("boss")
		if c := n.clients[boss.UID]; c == nil || !n.opers[c] || !c.noLag.Load() || c.operFlags == "" {
			t.Errorf("the oper isn't one for the next server")
		}
		for _, nick := range []string{"NickServ", "ChanServ", "AdminServ"} {
			if u := n.net.UserByNick(nick); u == nil || !u.Service {
				t.Errorf("%s is missing", nick)
			}
		}
		for _, u := range n.net.Users() {
			if !u.Service && oldUIDs[u.Nick] != "" && oldUIDs[u.Nick] != u.UID {
				t.Errorf("%s changed UID for no reason: %s -> %s", u.Nick, oldUIDs[u.Nick], u.UID)
			}
		}
		if total, _, opers := n.net.PeopleCounts(); total != 6 || opers != 1 {
			t.Errorf("LUSERS counts: %d people, %d opers", total, opers)
		}
	})
	// New people can come in, and old ones leave the usual way.
	frank := login(t, n, "Frank")
	frank.send("JOIN #side")
	frank.expect(" 366 Frank #side ")
	bob.expect(":Frank!~frank@127.0.0.1 JOIN #side")
	bob.send("QUIT :bye")
	bob.gone()
	frank.expect(":Bob!~bob@127.0.0.1 QUIT :Quit: bye")
}

// A snapshot from another format version is not used: those clients are
// disconnected, cleanly, and the next server starts empty and works.
func TestHandoverOtherVersionDropsClients(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := login(t, s, "Alice")
	bob := login(t, s, "Bob")
	alice.send("JOIN #hive")
	alice.expect(" 366 Alice #hive ")
	onLoop(t, s, func() { s.hand.version = handoverVersion + 1 })
	h := handOver(t, s, result, nil)
	if _, err := decodeHandover(h.snapshot); !errors.Is(err, errHandoverVersion) {
		t.Fatalf("decode: %v, want the version error", err)
	}
	kept, problem := -1, ""
	n := serveNext(t, path, h.resume(func(k int, p string) { kept, problem = k, p }))
	if kept != 0 || !strings.Contains(problem, "another format version") {
		t.Fatalf("the gate was told %d kept, %q", kept, problem)
	}
	alice.gone()
	bob.gone()
	onLoop(t, n, func() {
		if len(n.clients) != 0 || n.local != 0 || n.net.Channel("#hive") != nil || n.net.UserByNick("alice") != nil {
			t.Errorf("the next server isn't empty: %d clients, %d local", len(n.clients), n.local)
		}
	})
	again := login(t, n, "Alice")
	again.send("JOIN #hive")
	again.expect(" 366 Alice #hive ")
}

// A snapshot of a server with another name or SID is not used either.
func TestHandoverOtherServerDropsClients(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := login(t, s, "Alice")
	h := handOver(t, s, result, nil)
	snap, err := decodeHandover(h.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snap.SID = "9ZZ"
	if h.snapshot, err = encodeHandover(snap, handoverVersion); err != nil {
		t.Fatal(err)
	}
	kept := -1
	n := serveNext(t, path, h.resume(func(k int, p string) { kept = k }))
	if kept != 0 {
		t.Fatalf("%d clients kept from another server's snapshot", kept)
	}
	alice.gone()
	login(t, n, "Alice")
}

// A hand-over that didn't arrive whole (the gate says so) starts empty.
func TestHandoverBrokenTransferDropsClients(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := login(t, s, "Alice")
	h := handOver(t, s, result, nil)
	r := h.resume(nil)
	r.err = errors.New("gate: hand-over: got 0 of 1 clients")
	n := serveNext(t, path, r)
	alice.gone()
	login(t, n, "Alice")
}

// When taking over breaks half-way the server refuses to start
// (ErrHandoverBroken: the gate starts a clean one) and nobody is left
// half-connected.
func TestHandoverBreaksHalfWay(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := login(t, s, "Alice")
	bob := login(t, s, "Bob")
	h := handOver(t, s, result, nil)
	r := h.resume(nil)
	take, n := r.conn, 0
	r.conn = func(id string, info *gate.Info) (net.Conn, error) {
		if n++; n == 2 {
			panic("test: broken while taking over")
		}
		return take(id, info)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newServer(cfg, log.New(io.Discard, "", 0), nil, r); !errors.Is(err, ErrHandoverBroken) {
		t.Fatalf("newServer: %v, want ErrHandoverBroken", err)
	}
	for _, c := range []*tc{alice, bob} {
		c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.Copy(io.Discard, c.conn); err != nil && !strings.Contains(err.Error(), "reset") && !strings.Contains(err.Error(), "aborted") {
			t.Errorf("%s is still connected: %v", c.nick, err)
		}
	}
	// The clean start that follows works.
	n2 := serveNext(t, path, nil)
	login(t, n2, "Alice")
}

// A client whose connection didn't come along quits for the others, the
// usual way.
func TestHandoverLostConnectionQuits(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := login(t, s, "Alice")
	bob := login(t, s, "Bob")
	for _, c := range []*tc{alice, bob} {
		c.send("JOIN #hive")
		c.expect(" 366 " + c.nick + " #hive ")
	}
	alice.expect(":Bob!~bob@127.0.0.1 JOIN #hive")
	var bobUID string
	h := handOver(t, s, result, func() { bobUID = s.net.UserByNick("bob").UID })
	r := h.resume(nil)
	take := r.conn
	r.conn = func(id string, info *gate.Info) (net.Conn, error) {
		if id == bobUID {
			return nil, errors.New("test: not a socket")
		}
		return take(id, info)
	}
	n := serveNext(t, path, r)
	alice.expect(":Bob!~bob@127.0.0.1 QUIT :Connection closed")
	onLoop(t, n, func() {
		if n.net.UserByNick("bob") != nil || n.local != 1 {
			t.Errorf("bob is still on the next server (%d local)", n.local)
		}
	})
}

// Lines sent just before the hand-over, a password check under way and a
// burst of lines the old server hadn't read: nothing is lost and nothing
// runs twice or out of order.
func TestHandoverLosesNoInput(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := registered(t, s, "Alice")
	alice.send("NS LOGOUT")
	alice.expect("MODE Alice -r")
	bob := login(t, s, "Bob")
	for _, c := range []*tc{alice, bob} {
		c.send("JOIN #hive")
		c.expect(" 366 " + c.nick + " #hive ")
	}
	// The password check runs off the loop; the lines behind it wait for
	// it, in the old server or (unread) in the next one.
	const lines = 400
	var burst strings.Builder
	burst.WriteString("NS IDENTIFY " + pw + "\r\n")
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&burst, "PING :n%d\r\n", i)
	}
	burst.WriteString("PRIVMSG #hive :half a li")
	if _, err := io.WriteString(alice.conn, burst.String()); err != nil {
		t.Fatal(err)
	}
	h := handOver(t, s, result, nil)
	serveNext(t, path, h.resume(nil))
	fmt.Fprintf(alice.conn, "ne\r\n")
	accepted := false
	for i := 0; i < lines; {
		alice.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := alice.r.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for PONG n%d: %v", i, err)
		}
		accepted = accepted || strings.Contains(line, "Password accepted")
		if !strings.Contains(line, " PONG ") {
			continue
		}
		if want := fmt.Sprintf("PONG irc.test.local :n%d\r\n", i); !strings.HasSuffix(line, want) {
			t.Fatalf("alice got %q, want %q", line, want)
		}
		i++
	}
	bob.expect(":Alice!~alice@127.0.0.1 PRIVMSG #hive :half a line")
	if !accepted {
		alice.expect(nsLine("Alice", "Password accepted - you are now recognized."))
	}
	bob.send("WHOIS Alice")
	bob.expect(" 330 Bob Alice Alice :is logged in as")
}

// A second hand-over, from the server that took over the first: state
// that was restored is handed on again.
func TestHandoverTwice(t *testing.T) {
	s, path, result := fileServer(t, "")
	alice := login(t, s, "Alice")
	bob := login(t, s, "Bob")
	alice.send("JOIN #hive")
	alice.expect(" 366 Alice #hive ")
	bob.send("JOIN #hive")
	bob.expect(" 366 Bob #hive ")
	bob.send("AWAY :brb")
	bob.expect(" 306 Bob ")
	h := handOver(t, s, result, nil)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newServer(cfg, log.New(io.Discard, "", 0), nil, h.resume(nil))
	if err != nil {
		t.Fatal(err)
	}
	result2 := make(chan error, 1)
	go func() { result2 <- second.Serve(context.Background()) }()
	alice.send("PRIVMSG #hive :on the second server")
	bob.expect("PRIVMSG #hive :on the second server")
	h2 := handOver(t, second, result2, nil)

	serveNext(t, path, h2.resume(nil))
	bob.send("PRIVMSG #hive :on the third server")
	alice.expect(":Bob!~bob@127.0.0.1 PRIVMSG #hive :on the third server")
	alice.send("WHOIS Bob")
	alice.expect(" 301 Alice Bob :brb")
}

// Server links are not handed over: the other server sees a split, the
// people here see its users leave, and when the next server has linked
// again both sides are whole. What the old server told its clients about
// the split and hadn't sent yet goes out from the next one.
func TestHandoverSplitsAndRelinks(t *testing.T) {
	hub, leaf := linkedPair(t)
	alice := login(t, hub.s, "alice")
	bob := login(t, leaf.s, "bob")
	carol := login(t, leaf.s, "carol")
	hub.seen(t, "bob")
	hub.seen(t, "carol")
	leaf.seen(t, "alice")
	alice.send("JOIN #x")
	alice.expect(" 366 alice #x ")
	waitLink(t, "the leaf knows #x", func() bool {
		known := false
		leaf.s.onLoop(func() { known = leaf.s.net.Channel("#x") != nil })
		return known
	})
	for _, c := range []*tc{bob, carol} {
		c.send("JOIN #x")
		c.expect(" 366 " + c.nick + " #x ")
	}
	alice.expect(":carol!~carol@127.0.0.1 JOIN #x")
	alice.send("TOPIC #x :one network")
	bob.expect("TOPIC #x :one network")
	carol.expect("TOPIC #x :one network")

	old := leaf.s
	got := make(chan *handed, 1)
	old.post(func() {
		old.hand.keepConns = true
		old.hand.give = func(b []byte, conns map[string]net.Conn) error {
			got <- &handed{append([]byte(nil), b...), conns}
			return nil
		}
		old.startHandover()
	})
	var h *handed
	select {
	case h = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("the leaf never handed over")
	}
	stopServer(old) // returns when its Serve has
	snap, err := decodeHandover(h.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range snap.Net.Users {
		if u.Nick == "alice" || u.Server != "leaf.test.local" {
			t.Errorf("the snapshot holds %s of %s: only this server's people belong in it", u.Nick, u.Server)
		}
	}
	// The hub saw a split.
	alice.expect(":bob!~bob@127.0.0.1 QUIT :")
	alice.expect(":carol!~carol@127.0.0.1 QUIT :")
	waitLink(t, "the hub forgets the leaf", func() bool { return len(hub.servers()) == 0 && hub.user("bob") == nil })

	// The next leaf: same config, same connections.
	r := h.resume(nil)
	prev := newTestServer
	newTestServer = func(cfg *config.Config) (*Server, func(), error) {
		s, err := newServer(cfg, log.New(io.Discard, "", 0), nil, r)
		return s, func() {}, err
	}
	leaf.s = testServerWith(t, func(c *config.Config) { *c = *old.cfg }, func(s *Server) { s.log = log.New(leaf.log, "", 0) })
	newTestServer = prev
	// The old leaf's goodbye to its own people about the hub's users.
	bob.expect(":alice!~alice@127.0.0.1 QUIT :")
	carol.expect(":alice!~alice@127.0.0.1 QUIT :")
	// Linked again, by itself, and whole.
	waitLink(t, "the link is back", func() bool { return len(hub.servers()) == 1 && hub.user("bob") != nil && hub.user("carol") != nil })
	leaf.seen(t, "alice")
	alice.expect(":bob!~bob@127.0.0.1 JOIN #x")
	bob.expect(":alice!~alice@127.0.0.1 JOIN #x")
	waitLink(t, "#x is whole on both sides", func() bool {
		n := 0
		for _, node := range []*linkNode{hub, leaf} {
			node.s.onLoop(func() {
				if ch := node.s.net.Channel("#x"); ch != nil && len(ch.Members) == 3 && ch.Topic == "one network" {
					n++
				}
			})
		}
		return n == 2
	})
	alice.send("PRIVMSG #x :hello again")
	bob.expect(":alice!~alice@127.0.0.1 PRIVMSG #x :hello again")
	carol.send("PRIVMSG #x :we never left")
	alice.expect(":carol!~carol@127.0.0.1 PRIVMSG #x :we never left")
	bob.expect(":carol!~carol@127.0.0.1 PRIVMSG #x :we never left")
	// Services run on the hub: the leaf's people reach them again.
	bob.send("PRIVMSG NickServ :HELP")
	bob.expect(":NickServ!")
}

// chatter makes everyone in cs talk in #busy at once and runs handover
// when a third of it is said. Every client must get every line of every
// other one, once and in order, whichever server process delivered it.
func chatter(t *testing.T, cs []*tc, lines int, handover func()) {
	t.Helper()
	for _, c := range cs {
		c.send("JOIN #busy")
		c.expect(" 366 " + c.nick + " #busy ")
	}
	for _, c := range cs[1:] {
		// Ops, so the channel's flood guard lets them all talk at once.
		cs[0].send("MODE #busy +o " + c.nick)
		c.expect("MODE #busy +o " + c.nick)
	}
	for _, c := range cs {
		c.sync()
	}
	const mark = " PRIVMSG #busy :"
	var said atomic.Int64
	third := make(chan struct{})
	errs := make(chan error, 2*len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Add(2)
		go func() { // talk
			defer wg.Done()
			for n := 0; n < lines; n++ {
				if _, err := fmt.Fprintf(c.conn, "PRIVMSG #busy :%d %d\r\n", i, n); err != nil {
					errs <- fmt.Errorf("%s can't send line %d: %v", c.nick, n, err)
					return
				}
				if said.Add(1) == int64(len(cs)*lines/3) {
					close(third)
				}
				time.Sleep(200 * time.Microsecond)
			}
		}()
		go func() { // listen
			defer wg.Done()
			next := make([]int, len(cs))
			for left := (len(cs) - 1) * lines; left > 0; {
				c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
				line, err := c.r.ReadString('\n')
				if err != nil {
					errs <- fmt.Errorf("%s: %d lines never came: %v", c.nick, left, err)
					return
				}
				at := strings.Index(line, mark)
				if at < 0 {
					continue
				}
				var from, n int
				if _, err := fmt.Sscanf(line[at+len(mark):], "%d %d", &from, &n); err != nil || from < 0 || from >= len(cs) {
					errs <- fmt.Errorf("%s got a broken line: %q", c.nick, line)
					return
				}
				if n != next[from] {
					errs <- fmt.Errorf("%s got line %d of %s, the next one is %d", c.nick, n, cs[from].nick, next[from])
					return
				}
				next[from]++
				left--
			}
		}()
	}
	select {
	case <-third:
	case <-time.After(15 * time.Second):
		t.Fatal("the talking never got going")
	}
	handover()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

const noTargetLimit = "\n[targetfloodprot]\nenabled = false\n"

// A hand-over while everyone is talking: no line is lost, doubled or out
// of order.
func TestHandoverUnderChatter(t *testing.T) {
	s, path, result := fileServer(t, noTargetLimit)
	cs := make([]*tc, 8)
	for i := range cs {
		cs[i] = login(t, s, fmt.Sprintf("bee%d", i))
	}
	chatter(t, cs, 150, func() {
		h := handOver(t, s, result, nil)
		serveNext(t, path, h.resume(nil))
	})
}

func testSnap() *handoverSnap {
	n := state.NewNetwork("0BE")
	u := &state.User{UID: n.NextUID(), Nick: "Alice", Ident: "a", Host: "h", Realname: "Al\xefce", Server: "irc.test.local", Modes: map[byte]bool{'i': true}, SignOn: time.Unix(1700000000, 0).UTC()}
	n.Apply(&state.UserAdd{User: u})
	n.Apply(&state.Join{UID: u.UID, Channel: "#hive", TS: 1700000001})
	return &handoverSnap{
		Server: "irc.test.local", SID: "0BE", Casemapping: "rfc1459", From: "test", Taken: time.Unix(1700000002, 0).UTC(),
		Net: n.Snapshot(),
		Clients: []clientSnap{{
			UID: u.UID, Registered: true, Host: "h", IP: "10.0.0.1", TLS: true, Port: 6697,
			Pipe: &gate.Info{Remote: "10.0.0.1:5000", Local: "10.0.0.2:6697", TLS: true, CertFP: "ab12", Pipe: true},
			Caps: []string{"away-notify", "server-time"}, Monitor: map[string]string{"bob": "Bob"},
			Accept: []string{"0BEAAAAAB"}, Silence: []string{"*!*@caf\xe9"}, OperFlags: "ak", Oper: true, NoLag: true,
			ConnectedAt: time.Unix(1700000000, 0).UTC(), LastRecv: time.Unix(1700000001, 0).UTC(), LastMsg: time.Unix(1700000001, 0).UTC(),
			FloodAhead: 3 * time.Second, In: []byte("PRIVMSG #hive :hal"), Out: []byte(":x NOTICE Alice :\x01hi\x01\r\n"),
			SASLMech: "PLAIN", SASLBuf: "QUJD", Multiline: &mlInSnap{ID: "b1", Target: "#hive", Verb: "PRIVMSG", Parts: []mlPartSnap{{"a", false}, {"b", true}}},
			DNSBL: &dnsblSnap{List: "bl.example", Reply: 2, Until: time.Unix(1700009999, 0).UTC()},
		}},
		MaxLocal: 7, MaxGlobal: 9,
		TimedBans:   []timedBanSnap{{"#hive", 'b', "~t:5:*!*@x", time.Unix(1700000300, 0).UTC()}},
		FloodUnsets: []floodUnsetSnap{{"#hive", 'm', time.Unix(1700000400, 0).UTC()}},
	}
}

// What is written is what is read.
func TestHandoverSnapshotRoundTrip(t *testing.T) {
	in := testSnap()
	b, err := encodeHandover(in, handoverVersion)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("BEEIRCD-HANDOVER 1 ")) {
		t.Fatalf("snapshot starts with %q", b[:min(len(b), 30)])
	}
	out, err := decodeHandover(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.Clients, out.Clients) {
		t.Fatalf("a client reads back differently:\n%+v\n%+v", in.Clients[0], out.Clients[0])
	}
	if !reflect.DeepEqual(in.TimedBans, out.TimedBans) || !reflect.DeepEqual(in.FloodUnsets, out.FloodUnsets) ||
		in.Server != out.Server || in.SID != out.SID || in.Casemapping != out.Casemapping || !in.Taken.Equal(out.Taken) {
		t.Fatalf("the server's part reads back differently:\n%+v\n%+v", in, out)
	}
	c := out.Clients[0]
	if c.Silence[0] != "*!*@caf\xe9" || out.Net.Users[0].Realname != "Al\xefce" {
		t.Fatalf("text that isn't UTF-8 changed: %q, %q", c.Silence[0], out.Net.Users[0].Realname)
	}
	if string(c.In) != "PRIVMSG #hive :hal" || string(c.Out) != ":x NOTICE Alice :\x01hi\x01\r\n" || c.FloodAhead != 3*time.Second ||
		c.Pipe == nil || c.Pipe.CertFP != "ab12" || len(c.Caps) != 2 || c.Monitor["bob"] != "Bob" || c.Multiline.Parts[1] != (mlPartSnap{"b", true}) ||
		c.DNSBL.List != "bl.example" || out.TimedBans[0].Mode != 'b' || out.MaxGlobal != 9 || len(out.Net.Channels) != 1 {
		t.Fatalf("fields lost: %+v", c)
	}
}

// Another version, and things that aren't a snapshot at all, are refused
// before anything is read from them.
func TestHandoverSnapshotVersion(t *testing.T) {
	for _, v := range []int{0, handoverVersion + 1, 99} {
		if v == handoverVersion {
			continue
		}
		b, err := encodeHandover(testSnap(), v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeHandover(b); !errors.Is(err, errHandoverVersion) {
			t.Errorf("version %d: %v, want the version error", v, err)
		}
	}
	good, _ := encodeHandover(testSnap(), handoverVersion)
	_, body, _ := bytes.Cut(good, []byte("\n"))
	empty, _ := encodeHandover(&handoverSnap{Server: "x"}, handoverVersion)
	flipped := append([]byte(nil), good...)
	for i := len(flipped) - len(body); i < len(flipped); i += 7 {
		flipped[i] ^= 0xff
	}
	for name, b := range map[string][]byte{
		"empty":          nil,
		"no header":      body,
		"other magic":    append([]byte(fmt.Sprintf("BEEIRCD-SNAPSHOT 1 %d\n", len(body))), body...),
		"no version":     append([]byte("BEEIRCD-HANDOVER\n"), body...),
		"word version":   append([]byte(fmt.Sprintf("BEEIRCD-HANDOVER one %d\n", len(body))), body...),
		"no length":      append([]byte("BEEIRCD-HANDOVER 1\n"), body...),
		"cut short":      good[:len(good)/2],
		"one byte short": good[:len(good)-1],
		"scrambled":      flipped,
		"not a gob":      []byte("BEEIRCD-HANDOVER 1 6\nhello\n"),
		"no network":     empty,
		"header, no end": []byte("BEEIRCD-HANDOVER 1 0"),
	} {
		if h, err := decodeHandover(b); err == nil {
			t.Errorf("%s: decoded to %+v, want an error", name, h)
		}
	}
}

// The reader can say what it took from the connection and didn't return:
// whole lines, the start of one, and whether it was dropping a line that
// is too long.
func TestLineReaderRest(t *testing.T) {
	long := strings.Repeat("x", readBufSize+500) // doesn't fit the read buffer
	for _, tc := range []struct {
		name, in string
		reads    int
		rest     string
		skip     bool
	}{
		{"nothing read", "", 0, "", false},
		{"whole lines waiting", "A\r\nB\r\nC\r\n", 1, "B\r\nC\r\n", false},
		{"half a line", "A\r\nPRIVMSG #a :he", 2, "PRIVMSG #a :he", false},
		{"half a long line", "A\r\n" + long, 2, long, false},
		{"a long line that may still fit", strings.Repeat("y", maxInput-100), 1, strings.Repeat("y", maxInput-100), false},
		{"inside a line that is too long", strings.Repeat("y", maxInput+2000), 1, "", true},
	} {
		lr := lineReader{r: bufio.NewReaderSize(&stuck{data: []byte(tc.in)}, readBufSize)}
		for i := 0; i < tc.reads; i++ {
			lr.read()
		}
		rest, skip := lr.rest()
		if skip != tc.skip || (!skip && string(rest) != tc.rest) {
			t.Errorf("%s: rest %q (skip %v), want %q (skip %v)", tc.name, rest, skip, tc.rest, tc.skip)
		}
		if skip {
			continue
		}
		// Reading on from the rest gives what one reader would have got.
		one := lineReader{r: bufio.NewReaderSize(strings.NewReader(tc.in+"llo\r\nD\r\n"), readBufSize)}
		two := lineReader{r: bufio.NewReaderSize(io.MultiReader(bytes.NewReader(rest), strings.NewReader("llo\r\nD\r\n")), readBufSize)}
		for i := 0; i < tc.reads && i < strings.Count(tc.in, "\n"); i++ {
			one.read()
		}
		for {
			a, errA := one.read()
			b, errB := two.read()
			if a != b || (errA == nil) != (errB == nil) {
				t.Errorf("%s: reading on gives %q (%v), one reader gives %q (%v)", tc.name, b, errB, a, errA)
			}
			if errA != nil {
				break
			}
		}
	}
}

// stuck gives its data and then times out, like a connection whose read
// deadline passed.
type stuck struct{ data []byte }

func (s *stuck) Read(p []byte) (int, error) {
	if len(s.data) == 0 {
		return 0, errTimeout{}
	}
	n := copy(p, s.data)
	s.data = s.data[n:]
	return n, nil
}

type errTimeout struct{}

func (errTimeout) Error() string { return "i/o timeout" }
