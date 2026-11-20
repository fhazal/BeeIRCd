//go:build linux

package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// syncBuf collects a process's log while the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type ircConn struct {
	t    *testing.T
	nick string
	conn net.Conn
	r    *bufio.Reader
}

func (c *ircConn) send(line string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s\r\n", line); err != nil {
		c.t.Fatalf("%s send %q: %v", c.nick, line, err)
	}
}

func (c *ircConn) expect(want string) string {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var seen []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("%s: waiting for %q: %v\nseen:\n%s", c.nick, want, err, strings.Join(seen, ""))
		}
		if strings.Contains(line, want) {
			return line
		}
		seen = append(seen, line)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// `beeircd run` as it runs in production: a gate process and a server
// process behind it. SIGUSR2 to the gate replaces the server process with
// a new one, twice, and a plain and a TLS client stay connected, in their
// channel, through both.
func TestRunHandsOverBetweenProcesses(t *testing.T) {
	dir := t.TempDir()
	plainAddr, tlsAddr := freePort(t), freePort(t)
	conf := fmt.Sprintf(`
[server]
name = "irc.test.local"
network = "BeeNet"
sid = "0BE"
resolve_hosts = false

[[listen]]
address = %q

[[listen]]
address = %q
tls = true

[tls]
cert = "cert.pem"
key = "key.pem"
self_signed = true

[cloak]
enabled = false
secret = "0123456789abcdef-test"

[limits]
flood_burst = 10000
flood_rate = 10000
session_limit = 100

[geoip]
enabled = false

[update]
check = false

[services]
database = "services.db"
`, plainAddr, tlsAddr)
	path := filepath.Join(dir, "beeircd.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs syncBuf
	cmd := exec.Command(os.Args[0], "-config", path)
	cmd.Env = append(os.Environ(), envAsBeeircd+"=1", "BEEIRCD_NO_PLACE_LOOKUP=1")
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("beeircd's log:\n%s", logs.String())
		}
	}()

	dial := func(nick string, secure bool) *ircConn {
		t.Helper()
		var conn net.Conn
		var err error
		for range 100 {
			if secure {
				conn, err = tls.Dial("tcp", tlsAddr, &tls.Config{InsecureSkipVerify: true})
			} else {
				conn, err = net.Dial("tcp", plainAddr)
			}
			if err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("%s can't connect: %v", nick, err)
		}
		t.Cleanup(func() { conn.Close() })
		c := &ircConn{t: t, nick: nick, conn: conn, r: bufio.NewReader(conn)}
		c.send("NICK " + nick)
		c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick)
		c.expect(" 001 " + nick + " ")
		return c
	}
	plain, secure := dial("Plain", false), dial("Secure", true)
	for _, c := range []*ircConn{plain, secure} {
		c.send("JOIN #hive")
		c.expect(" 366 " + c.nick + " #hive ")
	}
	plain.send("TOPIC #hive :same people, new process")
	secure.expect("TOPIC #hive :same people, new process")
	secure.send("AWAY :tea")
	secure.expect(" 306 Secure ")

	for round := 1; round <= 2; round++ {
		fmt.Fprintf(plain.conn, "PRIVMSG #hive :round %d, half a li", round)
		if err := cmd.Process.Signal(syscall.SIGUSR2); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(plain.conn, "ne\r\n")
		secure.expect(fmt.Sprintf(":Plain!~plain@127.0.0.1 PRIVMSG #hive :round %d, half a line", round))
		secure.send(fmt.Sprintf("PRIVMSG #hive :round %d over TLS", round))
		plain.expect(fmt.Sprintf(":Secure!~secure@127.0.0.1 PRIVMSG #hive :round %d over TLS", round))
		plain.send("WHOIS Secure")
		plain.expect(" 301 Plain Secure :tea")
		plain.expect(" 671 Plain Secure :is using a secure connection")
		plain.expect(" 318 Plain Secure ")
		plain.send("TOPIC #hive")
		plain.expect(" 332 Plain #hive :same people, new process")
		// The same ports take new people.
		extra := dial(fmt.Sprintf("Extra%d", round), round == 2)
		extra.send("JOIN #hive")
		extra.expect(" 366 " + extra.nick + " #hive ")
		plain.expect(" JOIN #hive")
		if want := "hand-over done: 2 clients stayed connected"; round == 1 && !strings.Contains(logs.String(), want) {
			t.Fatalf("round 1: the gate didn't log %q", want)
		}
	}
	log := logs.String()
	if n := strings.Count(log, " starting as irc.test.local"); n != 3 {
		t.Errorf("%d server processes started, want 3", n)
	}
	if !strings.Contains(log, "hand-over done: 3 clients stayed connected") {
		t.Errorf("the second hand-over should keep 3 clients")
	}
	if strings.Contains(log, "PANIC") || strings.Contains(log, "hand-over failed") {
		t.Errorf("something broke on the way")
	}

	// A normal stop still works after it.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	plain.expect("ERROR :Closing Link")
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			t.Errorf("beeircd exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("beeircd didn't stop on SIGTERM")
	}
}
