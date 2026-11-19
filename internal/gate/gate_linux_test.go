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
// directory, so a restarted one knows it is the second.
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
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("addr.%d", n)), []byte(res.Addrs[0]), 0o600)
	conn, err := c.Accept()
	if err != nil {
		return 2
	}
	fmt.Fprintf(conn, "child %d\r\n", n)
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
	case n == 0:
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
