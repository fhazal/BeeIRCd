package main

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHistoryBuckets(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 30, 0, 0, time.UTC)
	h := history{}
	h.add(now, true)
	h.add(now, false)                           // this hour: 1 of 2
	h.add(now.Add(-2*hour), true)               // two hours ago: 1 of 1
	h.add(now.Add(-30*hour), false)             // too old, dropped
	h.add(now.Add(-23*hour+time.Minute), false) // oldest shown hour

	got := h.hours(now)
	if len(got) != 24 {
		t.Fatalf("want 24 buckets, got %d", len(got))
	}
	if got[23] != 0.5 || got[21] != 1 || got[0] != 0 || got[22] != -1 {
		t.Fatalf("buckets wrong: %v", got)
	}
	h.prune(now) // adds prune against their own time, so do it at "now"
	if len(h) != 3 {
		t.Fatalf("old bucket not dropped: %v", h)
	}
	if u := h.uptime(now); u != 50 { // 2 up of 4
		t.Fatalf("uptime = %v, want 50", u)
	}
	// An hour later the oldest bucket falls out.
	later := now.Add(hour)
	h.prune(later)
	if len(h) != 2 {
		t.Fatalf("prune kept %d buckets, want 2", len(h))
	}
	if (history{}).uptime(now) != 0 {
		t.Fatal("empty history should give 0")
	}
}

func TestTwoFailuresBeforeDown(t *testing.T) {
	now := time.Now()
	s := status{}
	if !s.record(true, now) || !s.up {
		t.Fatal("first success should flip to up")
	}
	if s.record(false, now) || !s.up {
		t.Fatal("one failure must not flip to down")
	}
	if s.record(true, now) || s.fails != 0 {
		t.Fatal("a success must reset the failure count")
	}
	s.record(false, now)
	if !s.record(false, now.Add(time.Minute)) || s.up {
		t.Fatal("two failures in a row should flip to down")
	}
	if !s.since.Equal(now.Add(time.Minute)) {
		t.Fatal("since should be the time of the change")
	}
}

func TestParse(t *testing.T) {
	cmd, p := parse(":srv 265 Bot 12 30 :Current local users 12, max 30")
	if cmd != "265" || len(p) != 4 || p[0] != "Bot" {
		t.Fatalf("parse: %q %q", cmd, p)
	}
	if n, ok := firstNumber(p[1:]); !ok || n != 12 {
		t.Fatalf("firstNumber = %d %v", n, ok)
	}
	if n, ok := firstNumber([]string{"Current local users 7, max 9"}); !ok || n != 7 {
		t.Fatalf("free-text number = %d %v", n, ok)
	}
	if cmd, p = parse("PING :abc"); cmd != "PING" || p[0] != "abc" {
		t.Fatalf("ping: %q %q", cmd, p)
	}
}

// fakeIRC accepts one client and answers like a small server. It reports
// each line it got on the returned channel.
func fakeIRC(t *testing.T) (net.Listener, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 100)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		sc := bufio.NewScanner(c)
		var nick string
		reply := func(s string) { c.Write([]byte(s + "\r\n")) }
		for sc.Scan() {
			line := sc.Text()
			got <- line
			f := strings.Fields(line)
			switch f[0] {
			case "NICK":
				nick = f[1]
				reply(":fake 001 " + nick + " :Welcome")
				reply(":fake 004 " + nick + " fake 1.3.0-rc8 iow ntk")
			case "PING":
				reply(":fake PONG fake :" + strings.TrimPrefix(f[1], ":"))
			case "LUSERS":
				reply(":fake 265 " + nick + " 5 9 :Current local users 5, max 9")
			case "LINKS":
				reply(":fake 364 " + nick + " fake fake :0 hub")
				reply(":fake 364 " + nick + " leaf.test fake :1 leaf")
				reply(":fake 365 " + nick + " * :End of LINKS")
			case "QUIT":
				return
			}
		}
	}()
	return ln, got
}

func TestWatcherAgainstFakeServer(t *testing.T) {
	ln, got := fakeIRC(t)
	defer ln.Close()
	dial := func(ctx context.Context, _ target) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}
	tg := target{Name: "fake", Role: "hub", Place: "Nowhere", Nick: "StatusX", Watch: "leaf.test"}
	w := newWatcher(tg, 100*time.Millisecond, dial, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()

	var v serverOut
	deadline := time.Now().Add(5 * time.Second)
	for {
		v = w.view(time.Now())
		w.mu.Lock()
		linked := w.linked
		w.mu.Unlock()
		if v.Up && v.People != nil && v.Ms != nil && linked != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher never got a full result: %+v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if *v.People != 4 { // 5 local users minus ourselves
		t.Errorf("people = %d, want 4", *v.People)
	}
	if v.Version != "1.3.0-rc8" {
		t.Errorf("version = %q", v.Version)
	}
	w.mu.Lock()
	if !*w.linked {
		t.Error("hub should have seen the leaf in LINKS")
	}
	w.mu.Unlock()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not stop")
	}
	var sawQuit, sawUser, sawMode bool
	for timeout := time.After(2 * time.Second); !sawQuit; {
		select {
		case l := <-got:
			sawQuit = sawQuit || strings.HasPrefix(l, "QUIT")
			sawUser = sawUser || strings.HasPrefix(l, "USER status")
			sawMode = sawMode || l == "MODE StatusX +iB"
		case <-timeout:
			sawQuit = true // report below via the failed flag
			t.Error("QUIT never arrived")
		}
	}
	if !sawUser || !sawMode {
		t.Errorf("user=%v mode=%v", sawUser, sawMode)
	}
}

func TestSnapshotLinked(t *testing.T) {
	now := time.Now()
	yes := true
	hub := &watcher{t: target{Name: "hub", Watch: "leaf"}, hist: history{}, linked: &yes}
	leaf := &watcher{t: target{Name: "leaf"}, hist: history{}}
	ws := []*watcher{hub, leaf}
	if s := snapshot(ws, now, time.Second); s.Linked != nil {
		t.Error("hub down: linked should be null")
	}
	hub.st.up = true
	if s := snapshot(ws, now, time.Second); s.Linked == nil || *s.Linked {
		t.Error("leaf down: linked should be false")
	}
	leaf.st.up = true
	if s := snapshot(ws, now, time.Second); s.Linked == nil || !*s.Linked {
		t.Error("both up: linked should follow LINKS (true)")
	}
}
