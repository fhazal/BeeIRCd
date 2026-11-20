//go:build linux

package gate

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

type portConf = config.Listen

// The test binary is also the fake server process: run starts it again
// with BEEIRCD_GATE set, and TestMain sends it to fakeChild.
func TestMain(m *testing.M) {
	if os.Getenv(envKey) != "" {
		os.Exit(fakeChild())
	}
	os.Exit(m.Run())
}

// fakeChild opens one port through the gate, greets one client and then
// exits as BEEIRCD_GATE_TEST_MODE says. It counts its runs in the test's
// directory, so a restarted one knows it is the second. The hand-over
// modes: "hand" and "ask" hand the client to the next run, "hang" never
// answers the request, "crash" makes the run that takes over die.
func fakeChild() int {
	dir, mode := os.Getenv("BEEIRCD_GATE_TEST_DIR"), os.Getenv("BEEIRCD_GATE_TEST_MODE")
	runs, _ := os.ReadFile(filepath.Join(dir, "runs"))
	n := len(runs)
	os.WriteFile(filepath.Join(dir, "runs"), append(runs, 'x'), 0o600)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	c, err := ChildFromEnv()
	if err != nil || c == nil {
		fmt.Fprintln(os.Stderr, "fake child:", err)
		return 2
	}
	res, err := c.Sync(Spec{Ports: []portConf{{Address: "127.0.0.1:0"}}})
	if err != nil || res.Errors[0] != "" {
		fmt.Fprintln(os.Stderr, "fake child sync:", err, res)
		return 2
	}
	var conn net.Conn
	if h := c.Handover(); h != nil {
		// This run takes over the last one's client.
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("took.%d", n)), []byte(fmt.Sprint(h.Len(), h.Err)), 0o600)
		if mode == "crash" {
			return 9
		}
		conn, err = h.Conn("the client", &Info{Remote: "192.0.2.7:1234", Local: "192.0.2.1:6697", TLS: true, Pipe: true})
		if err != nil || h.Err != nil {
			fmt.Fprintln(os.Stderr, "fake child take over:", err, h.Err)
			c.Resumed(0, fmt.Sprint(err, h.Err))
			return 2
		}
		fmt.Fprintf(conn, "child %d has: %s from %s\r\n", n, h.Snapshot, conn.RemoteAddr())
		c.Resumed(1, "")
	} else {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("addr.%d", n)), []byte(res.Addrs[0]), 0o600)
		if conn, err = c.Accept(); err != nil {
			return 2
		}
		fmt.Fprintf(conn, "child %d\r\n", n)
		handing := n == 0 && (mode == "hand" || mode == "ask" || mode == "crash" || mode == "hang")
		if handing && mode == "ask" {
			bufio.NewReader(conn).ReadString('\n') // the client says go
			if err := c.RequestHandover(); err != nil {
				fmt.Fprintln(os.Stderr, "fake child ask:", err)
				return 2
			}
		}
		if handing {
			if _, err := c.Accept(); !errors.Is(err, ErrHandover) {
				fmt.Fprintln(os.Stderr, "fake child: Accept gave", err)
				return 2
			}
			if mode == "hang" {
				select {} // never hands over: the gate has to kill it
			}
			if err := c.Give([]byte(fmt.Sprintf("snapshot %d", n)), map[string]net.Conn{"the client": conn}); err != nil {
				fmt.Fprintln(os.Stderr, "fake child give:", err)
				return 2
			}
			conn.Close()
			return ExitHandover
		}
	}
	if mode == "term" {
		<-term
		fmt.Fprintf(conn, "ERROR :Closing link (Server shutting down)\r\n")
	} else {
		bufio.NewReader(conn).ReadString('\n')
	}
	c.Sync(Spec{}) // the gate closes the port, as the server does
	conn.Close()
	switch {
	case mode == "term":
		return 0
	case n == 0 && mode == "":
		return ExitRestart
	default:
		return 7
	}
}

// testFront is a plain-port Front. With pipe set it hands clients on as a
// non-TCP conn, so the gate copies their bytes through a socket pair.
type testFront struct {
	deliver Deliver
	pipe    bool
	mu      sync.Mutex
	lns     []net.Listener
}

type wrapped struct{ net.Conn }

func newTestFront(pipe bool) func(Deliver) Front {
	return func(d Deliver) Front { return &testFront{deliver: d, pipe: pipe} }
}

func (f *testFront) Apply(s Spec) Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ln := range f.lns {
		ln.Close()
	}
	f.lns = nil
	res := Result{Addrs: make([]string, len(s.Ports)), Errors: make([]string, len(s.Ports))}
	for i, p := range s.Ports {
		ln, err := net.Listen("tcp", p.Address)
		if err != nil {
			res.Errors[i] = err.Error()
			continue
		}
		f.lns = append(f.lns, ln)
		res.Addrs[i] = ln.Addr().String()
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				info := Info{Remote: conn.RemoteAddr().String(), Local: conn.LocalAddr().String(), TLS: f.pipe, CertFP: "ab12"}
				if f.pipe {
					conn = wrapped{conn}
				}
				if !f.deliver(conn, info) {
					conn.Close()
				}
			}
		}()
	}
	return res
}

func (f *testFront) Close() { f.Apply(Spec{}) }

func loopback(t *testing.T, pipe bool) (*Child, string) {
	t.Helper()
	child, stop, err := Loopback(newTestFront(pipe))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Close(); stop() })
	res, err := child.Sync(Spec{Ports: []portConf{{Address: "127.0.0.1:0"}}})
	if err != nil || res.Errors[0] != "" {
		t.Fatalf("sync: %v %v", err, res)
	}
	return child, res.Addrs[0]
}

// accept dials addr and returns both ends: the client and what the
// server process got from the gate.
func accept(t *testing.T, child *Child, addr string) (net.Conn, net.Conn) {
	t.Helper()
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	type got struct {
		c   net.Conn
		err error
	}
	ch := make(chan got, 1)
	go func() { c, err := child.Accept(); ch <- got{c, err} }()
	select {
	case g := <-ch:
		if g.err != nil {
			t.Fatal(g.err)
		}
		t.Cleanup(func() { g.c.Close() })
		return client, g.c
	case <-time.After(5 * time.Second):
		t.Fatal("no client from the gate")
	}
	return nil, nil
}

func roundTrip(t *testing.T, client, server net.Conn) {
	t.Helper()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	server.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(client, "NICK bee\r\n")
	if line, err := bufio.NewReader(server).ReadString('\n'); err != nil || line != "NICK bee\r\n" {
		t.Fatalf("server read %q, %v", line, err)
	}
	fmt.Fprintf(server, ":srv 001 bee :hi\r\n")
	if line, err := bufio.NewReader(client).ReadString('\n'); err != nil || line != ":srv 001 bee :hi\r\n" {
		t.Fatalf("client read %q, %v", line, err)
	}
}

// A plain port hands over the client's own socket: no copying in between.
func TestPlainClientGetsItsOwnSocket(t *testing.T) {
	child, addr := loopback(t, false)
	client, server := accept(t, child, addr)
	if _, ok := server.(*net.TCPConn); !ok {
		t.Fatalf("server got %T, want the client's *net.TCPConn", server)
	}
	if server.RemoteAddr().String() != client.LocalAddr().String() {
		t.Fatalf("remote %s, want %s", server.RemoteAddr(), client.LocalAddr())
	}
	roundTrip(t, client, server)
}

// A piped client keeps its addresses and what the gate learned (Info).
func TestPipedClientInfo(t *testing.T) {
	child, addr := loopback(t, true)
	client, server := accept(t, child, addr)
	gc, ok := server.(*Conn)
	if !ok {
		t.Fatalf("server got %T, want *gate.Conn", server)
	}
	if !gc.Info.Pipe || !gc.Info.TLS || gc.Info.CertFP != "ab12" {
		t.Fatalf("info %+v", gc.Info)
	}
	ra, ok := server.RemoteAddr().(*net.TCPAddr)
	if !ok || ra.String() != client.LocalAddr().String() {
		t.Fatalf("remote %v (%T), want %s", server.RemoteAddr(), server.RemoteAddr(), client.LocalAddr())
	}
	if la, ok := server.LocalAddr().(*net.TCPAddr); !ok || la.String() != addr {
		t.Fatalf("local %v, want %s", server.LocalAddr(), addr)
	}
	roundTrip(t, client, server)
	// A lot of output, more than the pipe and socket buffers hold.
	big := strings.Repeat("x", 510) + "\r\n"
	go func() {
		for range 2000 {
			io.WriteString(server, big)
		}
		server.(*Conn).CloseWrite()
	}()
	n, err := io.Copy(io.Discard, client)
	if err != nil || n != int64(2000*len(big)) {
		t.Fatalf("client got %d bytes, %v", n, err)
	}
}

// A clean close by the client reads as io.EOF; a dropped connection as
// ErrReset, so the server can say "Connection closed" or "Read error".
func TestPipedCloseAndReset(t *testing.T) {
	child, addr := loopback(t, true)
	client, server := accept(t, child, addr)
	client.(*net.TCPConn).CloseWrite()
	server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := server.Read(make([]byte, 16)); err != io.EOF {
		t.Fatalf("clean close: %v, want io.EOF", err)
	}
	client, server = accept(t, child, addr)
	client.(*net.TCPConn).SetLinger(0)
	client.Close() // RST
	server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := server.Read(make([]byte, 16)); !errors.Is(err, ErrReset) {
		t.Fatalf("reset: %v, want ErrReset", err)
	}
}

// Sync reports a port that can't open with net.Listen's own text.
func TestSyncPortError(t *testing.T) {
	child, addr := loopback(t, false)
	res, err := child.Sync(Spec{Ports: []portConf{{Address: addr}, {Address: addr}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors[0] != "" || !strings.Contains(res.Errors[1], "address already in use") {
		t.Fatalf("errors %q", res.Errors)
	}
}

// When the gate goes away the server process hears it.
func TestChildSeesGateGone(t *testing.T) {
	child, stop, err := Loopback(newTestFront(false))
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	go child.Accept()
	stop()
	select {
	case <-child.Gone():
	case <-time.After(5 * time.Second):
		t.Fatal("Gone not closed")
	}
}

func runGate(t *testing.T, mode string) (string, chan int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BEEIRCD_GATE_TEST_DIR", dir)
	t.Setenv("BEEIRCD_GATE_TEST_MODE", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code := make(chan int, 1)
	go func() { code <- run(log.New(io.Discard, "", 0), exe, nil, newTestFront(true)) }()
	return dir, code
}

func childAddr(t *testing.T, dir string, n int) string {
	t.Helper()
	for range 100 {
		if b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("addr.%d", n))); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server process %d never opened its port", n)
	return ""
}

func greet(t *testing.T, addr string, want string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	if line, err := r.ReadString('\n'); err != nil || line != want+"\r\n" {
		t.Fatalf("got %q, %v; want %q", line, err, want)
	}
	return c, r
}

// ExitRestart starts the server again; any other exit ends the gate with
// the same code.
func TestGateRestartsChild(t *testing.T) {
	dir, code := runGate(t, "")
	c, _ := greet(t, childAddr(t, dir, 0), "child 0")
	fmt.Fprintf(c, "bye\r\n")
	c2, _ := greet(t, childAddr(t, dir, 1), "child 1")
	fmt.Fprintf(c2, "bye\r\n")
	select {
	case got := <-code:
		if got != 7 {
			t.Fatalf("gate exit %d, want 7", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("gate didn't exit")
	}
	c.Close()
	c2.Close()
}

// SIGTERM goes on to the server process; the gate keeps copying until the
// server said goodbye, then exits with its code.
func TestGateStopsOnSIGTERM(t *testing.T) {
	dir, code := runGate(t, "term")
	addr := childAddr(t, dir, 0)
	c, r := greet(t, addr, "child 0")
	defer c.Close()
	syscall.Kill(os.Getpid(), syscall.SIGTERM) // caught by run's signal.Notify
	if line, err := r.ReadString('\n'); err != nil || !strings.HasPrefix(line, "ERROR :Closing link") {
		t.Fatalf("got %q, %v; want the goodbye", line, err)
	}
	select {
	case got := <-code:
		if got != 0 {
			t.Fatalf("gate exit %d, want 0", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("gate didn't exit")
	}
	if _, err := r.ReadString('\n'); err != io.EOF {
		t.Fatalf("after goodbye: %v, want EOF", err)
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("port still open after the gate stopped")
	}
}

func gateExit(t *testing.T, code chan int, want int) {
	t.Helper()
	select {
	case got := <-code:
		if got != want {
			t.Fatalf("gate exit %d, want %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("gate didn't exit")
	}
}

func shortWaits(t *testing.T, hand time.Duration) {
	t.Helper()
	oldHand, oldHold, oldSync := handWait, holdWait, syncWait
	handWait, holdWait, syncWait = hand, 5*time.Second, 5*time.Second
	t.Cleanup(func() { handWait, holdWait, syncWait = oldHand, oldHold, oldSync })
}

// SIGUSR2 replaces the server process and the client stays connected: the
// next process gets the snapshot, the client's socket and what the last
// one knew about it.
func TestGateHandsClientToNextProcess(t *testing.T) {
	for _, mode := range []string{"hand", "ask"} {
		t.Run(mode, func(t *testing.T) {
			dir, code := runGate(t, mode)
			c, r := greet(t, childAddr(t, dir, 0), "child 0")
			defer c.Close()
			if mode == "ask" {
				fmt.Fprintf(c, "go\r\n") // the server process asks the gate itself
			} else {
				syscall.Kill(os.Getpid(), syscall.SIGUSR2) // caught by run's signal.Notify
			}
			if line, err := r.ReadString('\n'); err != nil || line != "child 1 has: snapshot 0 from 192.0.2.7:1234\r\n" {
				t.Fatalf("after the hand-over the client read %q, %v", line, err)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "took.1")); string(b) != "1 <nil>" {
				t.Fatalf("the next process took %q, want 1 client and no error", b)
			}
			fmt.Fprintf(c, "bye\r\n")
			gateExit(t, code, 7)
			if runs, _ := os.ReadFile(filepath.Join(dir, "runs")); string(runs) != "xx" {
				t.Fatalf("%d server processes ran, want 2", len(runs))
			}
		})
	}
}

// A server that doesn't hand over in time is killed (it never runs next
// to the new one); its clients drop and the next process starts empty.
func TestGateHandoverTimeout(t *testing.T) {
	shortWaits(t, 500*time.Millisecond)
	dir, code := runGate(t, "hang")
	c, r := greet(t, childAddr(t, dir, 0), "child 0")
	defer c.Close()
	syscall.Kill(os.Getpid(), syscall.SIGUSR2)
	if line, err := r.ReadString('\n'); err != io.EOF {
		t.Fatalf("the old server's client read %q, %v; want EOF", line, err)
	}
	c2, _ := greet(t, childAddr(t, dir, 1), "child 1")
	defer c2.Close()
	if _, err := os.Stat(filepath.Join(dir, "took.1")); err == nil {
		t.Fatal("the next process was given a hand-over that never happened")
	}
	fmt.Fprintf(c2, "bye\r\n")
	gateExit(t, code, 7)
}

// A new server that dies while taking over is started once more, empty;
// the clients are gone, as after a restart.
func TestGateHandoverNewServerDies(t *testing.T) {
	dir, code := runGate(t, "crash")
	c, r := greet(t, childAddr(t, dir, 0), "child 0")
	defer c.Close()
	syscall.Kill(os.Getpid(), syscall.SIGUSR2)
	if line, err := r.ReadString('\n'); err != io.EOF {
		t.Fatalf("the client read %q, %v; want EOF", line, err)
	}
	c2, _ := greet(t, childAddr(t, dir, 2), "child 2")
	defer c2.Close()
	if _, err := os.Stat(filepath.Join(dir, "took.2")); err == nil {
		t.Fatal("the restarted process was given the hand-over again")
	}
	fmt.Fprintf(c2, "bye\r\n")
	gateExit(t, code, 7)
	if runs, _ := os.ReadFile(filepath.Join(dir, "runs")); string(runs) != "xxx" {
		t.Fatalf("%d server processes ran, want 3", len(runs))
	}
}

// oldServer plays the server that hands over: it waits for the gate's
// request, then gives its one client away (after wait is closed).
func oldServer(child *Child, server net.Conn, asked chan<- struct{}, wait <-chan struct{}) chan error {
	gave := make(chan error, 1)
	go func() {
		if _, err := child.Accept(); !errors.Is(err, ErrHandover) {
			gave <- fmt.Errorf("Accept: %v, want ErrHandover", err)
			return
		}
		close(asked)
		<-wait
		err := child.Give([]byte("the snapshot"), map[string]net.Conn{"one": server})
		server.Close()
		gave <- err
	}()
	return gave
}

// Inside one process: the next Child gets the snapshot and the client, a
// plain one as its own socket and a piped one with its pipe; a client
// that connects during the hand-over waits and reaches the next server.
func TestLoopHandover(t *testing.T) {
	for _, pipe := range []bool{false, true} {
		t.Run(fmt.Sprintf("pipe=%v", pipe), func(t *testing.T) {
			shortWaits(t, 5*time.Second)
			lp, err := NewLoop(newTestFront(pipe))
			if err != nil {
				t.Fatal(err)
			}
			defer lp.Close()
			child := lp.Child()
			res, err := child.Sync(Spec{Ports: []portConf{{Address: "127.0.0.1:0"}}})
			if err != nil || res.Errors[0] != "" {
				t.Fatalf("sync: %v %v", err, res)
			}
			client, server := accept(t, child, res.Addrs[0])
			roundTrip(t, client, server)
			var info *Info
			if gc, ok := server.(*Conn); ok {
				info = &gc.Info
			}
			if (info != nil) != pipe {
				t.Fatalf("server side is a %T", server)
			}

			asked, wait := make(chan struct{}), make(chan struct{})
			gave := oldServer(child, server, asked, wait)
			type next struct {
				child *Child
				err   error
			}
			done := make(chan next, 1)
			go func() { c, err := lp.Handover(5 * time.Second); done <- next{c, err} }()
			<-asked
			// Someone connects while no server takes clients.
			late, err := net.Dial("tcp", res.Addrs[0])
			if err != nil {
				t.Fatal(err)
			}
			defer late.Close()
			fmt.Fprintf(late, "NICK late\r\n")
			fmt.Fprintf(client, "half a ") // and the old client keeps typing
			close(wait)
			if err := <-gave; err != nil {
				t.Fatalf("Give: %v", err)
			}
			nx := <-done
			if nx.err != nil {
				t.Fatalf("Handover: %v", nx.err)
			}
			defer nx.child.Close()
			h := nx.child.Handover()
			if h == nil || h.Err != nil || string(h.Snapshot) != "the snapshot" || h.Len() != 1 || !h.Has("one") {
				t.Fatalf("hand-over: %+v", h)
			}
			if nx.child.Handover() != nil {
				t.Fatal("the hand-over can be taken twice")
			}
			if _, err := h.Conn("two", info); err == nil {
				t.Fatal("got a client nobody handed over")
			}
			server2, err := h.Conn("one", info)
			if err != nil {
				t.Fatal(err)
			}
			defer server2.Close()
			if _, isTCP := server2.(*net.TCPConn); isTCP == pipe {
				t.Fatalf("the next server got a %T", server2)
			}
			if pipe && server2.RemoteAddr().String() != client.LocalAddr().String() {
				t.Fatalf("remote address %s, want the client's %s", server2.RemoteAddr(), client.LocalAddr())
			}
			if err := nx.child.Resumed(1, ""); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(client, "line\r\n")
			server2.SetDeadline(time.Now().Add(5 * time.Second))
			if line, err := bufio.NewReader(server2).ReadString('\n'); err != nil || line != "half a line\r\n" {
				t.Fatalf("the next server read %q, %v", line, err)
			}
			roundTrip(t, client, server2)

			got := make(chan net.Conn, 1)
			go func() { c, _ := nx.child.Accept(); got <- c }()
			select {
			case lc := <-got:
				if lc == nil {
					t.Fatal("the late client didn't arrive")
				}
				defer lc.Close()
				lc.SetDeadline(time.Now().Add(5 * time.Second))
				if line, err := bufio.NewReader(lc).ReadString('\n'); err != nil || line != "NICK late\r\n" {
					t.Fatalf("late client: %q, %v", line, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the client that connected during the hand-over never reached the next server")
			}
		})
	}
}

// A hand-over nobody takes is closed with a goodbye line.
func TestHandoverCloseSaysGoodbye(t *testing.T) {
	for _, pipe := range []bool{false, true} {
		lp, err := NewLoop(newTestFront(pipe))
		if err != nil {
			t.Fatal(err)
		}
		child := lp.Child()
		res, err := child.Sync(Spec{Ports: []portConf{{Address: "127.0.0.1:0"}}})
		if err != nil || res.Errors[0] != "" {
			t.Fatalf("sync: %v %v", err, res)
		}
		client, server := accept(t, child, res.Addrs[0])
		asked, wait := make(chan struct{}), make(chan struct{})
		close(wait)
		gave := oldServer(child, server, asked, wait)
		next, err := lp.Handover(5 * time.Second)
		if err != nil || <-gave != nil {
			t.Fatalf("hand-over: %v", err)
		}
		next.Handover().Close() // the new server can't use the snapshot
		client.SetDeadline(time.Now().Add(5 * time.Second))
		all, err := io.ReadAll(client)
		if err != nil || string(all) != goodbye {
			t.Fatalf("pipe=%v: the client read %q, %v; want the goodbye and EOF", pipe, all, err)
		}
		next.Close()
		lp.Close()
	}
}

// A server that never answers the request: Handover gives up.
func TestLoopHandoverTimeout(t *testing.T) {
	lp, err := NewLoop(newTestFront(false))
	if err != nil {
		t.Fatal(err)
	}
	defer lp.Close()
	defer lp.Child().Close()
	if _, err := lp.Handover(200 * time.Millisecond); err == nil {
		t.Fatal("Handover returned without a server handing over")
	}
}

// Fewer clients arrive than the gate announced: the hand-over is marked
// broken and holds nobody.
func TestBrokenHandoverTransfer(t *testing.T) {
	shortWaits(t, time.Second)
	syncWait = 300 * time.Millisecond
	l, child, err := loopPair()
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	defer child.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	srv, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	f, err := srv.(*net.TCPConn).File()
	srv.Close()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(f.Fd()))
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := snapFile([]byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	go l.sendOld(&payload{snap: -1, ids: []string{"one"}, fds: []int{fd}})
	h := child.load(snap, 2)
	if h.Err == nil || h.Len() != 0 {
		t.Fatalf("hand-over: err %v, %d clients; want an error and nobody", h.Err, h.Len())
	}
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if all, err := io.ReadAll(client); err != nil || string(all) != goodbye {
		t.Fatalf("the client read %q, %v; want the goodbye and EOF", all, err)
	}
}
