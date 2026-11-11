package server

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/store"
)

// TestLoad5000 is the Stage 1.5.2 gate: 5,000 clients connect, register,
// join registered and unregistered channels, change nicks, talk and set
// topics and modes, and 99% of the loop's work must finish within 5 ms.
//
// It is slow, so it runs only with BEEIRCD_LOAD=1:
//
//	BEEIRCD_LOAD=1 go test -run Load5000 -v ./internal/server
func TestLoad5000(t *testing.T) {
	if os.Getenv("BEEIRCD_LOAD") == "" {
		t.Skip("set BEEIRCD_LOAD=1 to run the 5,000-client load test")
	}
	const (
		clients    = 5000
		channels   = 100 // #load0 .. #load99; the first 20 are registered
		registered = 20
		perClient  = 3
		messages   = 3
	)
	skipReplay = true
	defer func() { skipReplay = false }()
	s := testServerWith(t, func(cfg *config.Config) {
		cfg.Limits.MaxUsers = clients + 100
		cfg.Limits.SessionLimit = clients + 100
		cfg.Limits.MaxChannels = 20
		cfg.Services.LogChannel = "#services" // every services command posts a line
		cfg.Services.LogConnects = true
	}, func(s *Server) {
		// 200 accounts with access on the registered channels and a few
		// AKICKs, so JOIN and NICK have services data to look at.
		now := time.Now()
		var accts []*store.Account
		for i := 0; i < 200; i++ {
			a, err := s.store.CreateAccount(fmt.Sprintf("acct%d", i), "x", "", now)
			if err != nil {
				t.Fatal(err)
			}
			accts = append(accts, a)
		}
		for i := 0; i < registered; i++ {
			ch, err := s.store.CreateChannel(fmt.Sprintf("#load%d", i), accts[i], "", now)
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 50; j++ {
				s.store.SetLevel(ch, accts[(i+j+1)%len(accts)], store.LevelAOP, "test", "", now)
			}
			for j := 0; j < 10; j++ {
				s.store.AddAkick(ch, fmt.Sprintf("*!*@nobody%d.example", j), "no", "test", now)
			}
		}
		// 500 network bans that match nobody (globs, CIDR, a regex), so
		// connects and the per-command SHUN check have a list to scan.
		for i := 0; i < 100; i++ {
			for _, b := range []store.NetBan{
				{Kind: store.BanAkill, Mask: fmt.Sprintf("*@bad%d.example", i)},
				{Kind: store.BanAkill, Mask: fmt.Sprintf("*@198.51.%d.0/24", i)},
				{Kind: store.BanZline, Mask: fmt.Sprintf("203.0.%d.*", i)},
				{Kind: store.BanShun, Mask: fmt.Sprintf("spam%d@*", i)},
				{Kind: store.BanSNLine, Mask: fmt.Sprintf("/^botnet%d .*$/", i)},
			} {
				b.Reason, b.SetBy, b.SetAt = "load", "test", now
				if err := s.store.AddNetBan(b); err != nil {
					t.Fatal(err)
				}
			}
		}
		s.svc.ReloadBans()
		if n := s.svc.BanCounts(); n[store.BanAkill] != 200 || n[store.BanShun] != 100 {
			t.Fatalf("bans not loaded: %v", n)
		}
	})
	addr := s.Addrs()[0].String()

	type lc struct {
		conn net.Conn
		w    *bufio.Writer
		mu   sync.Mutex
		got  chan string // lines we wait for
	}
	conns := make([]*lc, clients)
	var wg sync.WaitGroup
	send := func(c *lc, lines ...string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, l := range lines {
			c.w.WriteString(l + "\r\n")
		}
		c.w.Flush()
	}
	// reader drains everything (so no send queue fills up) and reports the
	// lines a phase waits for.
	reader := func(c *lc) {
		r := bufio.NewReaderSize(c.conn, 64<<10)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(c.got)
				return
			}
			if strings.Contains(line, " 001 ") || strings.Contains(line, " 366 ") || strings.Contains(line, "PONG") {
				select {
				case c.got <- line:
				default:
				}
			}
			if strings.HasPrefix(line, "PING") {
				send(c, "PONG"+line[4:])
			}
		}
	}
	waitFor := func(c *lc, want string, n int) error {
		deadline := time.After(60 * time.Second)
		for n > 0 {
			select {
			case line, ok := <-c.got:
				if !ok {
					return fmt.Errorf("connection closed")
				}
				if strings.Contains(line, want) {
					n--
				}
			case <-deadline:
				return fmt.Errorf("timed out waiting for %q", want)
			}
		}
		return nil
	}
	parallel := func(name string, work func(i int, c *lc) error) {
		t.Helper()
		start := time.Now()
		errs := make(chan error, clients)
		sem := make(chan struct{}, 256)
		for i := range conns {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := work(i, conns[i]); err != nil {
					errs <- fmt.Errorf("client %d: %v", i, err)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%-10s %d clients in %s", name, clients, time.Since(start).Round(time.Millisecond))
	}

	parallel("connect", func(i int, _ *lc) error {
		// Windows refuses connects while the listen queue is full: retry.
		var conn net.Conn
		var err error
		for try := 0; try < 50; try++ {
			if conn, err = net.Dial("tcp", addr); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			return err
		}
		c := &lc{conn: conn, w: bufio.NewWriter(conn), got: make(chan string, 64)}
		conns[i] = c
		go reader(c)
		nick := fmt.Sprintf("load%d", i)
		if i < 200 {
			nick = fmt.Sprintf("acct%d", i) // registered nicks: NickServ asks them to identify
		}
		send(c, "NICK "+nick, "USER u 0 * :load test")
		return waitFor(c, " 001 ", 1)
	})
	t.Cleanup(func() {
		for _, c := range conns {
			if c != nil {
				c.conn.Close()
			}
		}
	})

	// From here on the loop is measured.
	resetLoop(s)
	parallel("join", func(i int, c *lc) error {
		var names []string
		for k := 0; k < perClient; k++ {
			names = append(names, fmt.Sprintf("#load%d", (i*7+k*31)%channels))
		}
		send(c, "JOIN "+strings.Join(names, ","))
		return waitFor(c, " 366 ", perClient)
	})
	parallel("talk", func(i int, c *lc) error {
		ch := fmt.Sprintf("#load%d", (i*7)%channels)
		for k := 0; k < messages; k++ {
			send(c, fmt.Sprintf("PRIVMSG %s :message %d from %d, a line about the length people type", ch, k, i))
		}
		send(c, "PING :talk")
		return waitFor(c, "PONG", 1)
	})
	parallel("nick", func(i int, c *lc) error {
		send(c, fmt.Sprintf("NICK renamed%d", i), "PING :nick")
		return waitFor(c, "PONG", 1)
	})
	parallel("modes", func(i int, c *lc) error {
		ch := fmt.Sprintf("#load%d", (i*7)%channels)
		send(c, "TOPIC "+ch+" :topic from "+fmt.Sprint(i), "MODE "+ch+" +b *!*@spam"+fmt.Sprint(i%50)+".example",
			"AWAY :lunch", "AWAY", "PING :modes")
		return waitFor(c, "PONG", 1)
	})
	parallel("part", func(i int, c *lc) error {
		send(c, fmt.Sprintf("PART #load%d :bye", (i*7)%channels), "PING :part")
		return waitFor(c, "PONG", 1)
	})

	st := loopNow(s)
	t.Logf("loop: %s", st.summary())
	t.Logf("loop: 50%% under %s, 90%% under %s, 99.9%% under %s",
		ms(st.percentile(0.5)), ms(st.percentile(0.9)), ms(st.percentile(0.999)))
	if p := st.percentile(0.99); p > 5*time.Millisecond {
		t.Errorf("99%% of loop work took up to %s; the gate is 5 ms", ms(p))
	}
	if st.average() > time.Millisecond {
		t.Errorf("average loop work %s; expected well under 1 ms", ms(st.average()))
	}
}

func resetLoop(s *Server) {
	done := make(chan struct{})
	s.post(func() { s.loop = loopStats{}; close(done) })
	<-done
}

func loopNow(s *Server) loopStats {
	out := make(chan loopStats)
	s.post(func() { out <- s.loop })
	return <-out
}
