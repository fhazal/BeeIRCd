// Command beeload puts a crowd of bots on an IRC server to see how it
// copes: they connect, join channels, talk, change nicks and go away, and
// beeload prints how long the server takes to answer (its lag) as it goes.
//
//	beeload -addr 192.168.50.76:26667 -n 3000
//
// A few bots sit in -watch (default #loadtest) and talk slowly there, so a
// person in that channel can see the crowd without drowning in it. Stop
// with Ctrl+C: every bot quits.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	addr     = flag.String("addr", "127.0.0.1:6667", "server address")
	n        = flag.Int("n", 1000, "number of bots")
	ramp     = flag.Int("ramp", 100, "bots connecting per second")
	chans    = flag.Int("chans", 50, "busy channels (#load0 ...), each bot joins 3")
	watch    = flag.String("watch", "#loadtest", "quiet channel to watch the crowd in")
	watchers = flag.Int("watchers", 200, "bots that also sit in -watch")
	talk     = flag.Duration("talk", 30*time.Second, "how often each bot says something in a busy channel")
	watchMsg = flag.Duration("watchtalk", 2*time.Second, "how often one of the watch bots speaks in -watch")
	duration = flag.Duration("duration", 0, "stop after this long (0 = until Ctrl+C)")
	prefix   = flag.String("prefix", "bee", "bot nick prefix")
)

var (
	connected, registered, sent, received, failed atomic.Int64
	lagMu                                         sync.Mutex
	lags                                          []time.Duration
)

type bot struct {
	id   int
	conn net.Conn
	wmu  sync.Mutex
	w    *bufio.Writer
	stop chan struct{}
}

func (b *bot) send(lines ...string) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	for _, l := range lines {
		b.w.WriteString(l + "\r\n")
	}
	if b.w.Flush() == nil {
		sent.Add(int64(len(lines)))
	}
}

func (b *bot) run(wg *sync.WaitGroup) {
	defer wg.Done()
	var err error
	for try := 0; try < 20; try++ {
		if b.conn, err = net.DialTimeout("tcp", *addr, 10*time.Second); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		failed.Add(1)
		return
	}
	connected.Add(1)
	defer b.conn.Close()
	b.w = bufio.NewWriter(b.conn)
	nick := fmt.Sprintf("%s%04d", *prefix, b.id)
	b.send("NICK "+nick, "USER "+*prefix+" 0 * :beeload bot "+strconv.Itoa(b.id))
	ready := make(chan struct{})
	go b.read(ready)
	select {
	case <-ready:
	case <-b.stop:
		return
	case <-time.After(60 * time.Second):
		failed.Add(1)
		return
	}
	registered.Add(1)
	var join []string
	for k := 0; k < 3; k++ {
		join = append(join, fmt.Sprintf("#load%d", (b.id*7+k*13)%*chans))
	}
	if b.id < *watchers {
		join = append(join, *watch)
	}
	b.send("JOIN " + strings.Join(join, ","))
	talkEvery := time.NewTicker(*talk + time.Duration(rand.Int64N(int64(*talk))))
	defer talkEvery.Stop()
	for {
		select {
		case <-b.stop:
			b.send("QUIT :beeload done")
			time.Sleep(200 * time.Millisecond)
			return
		case <-talkEvery.C:
			ch := join[rand.IntN(3)]
			b.send(fmt.Sprintf("PRIVMSG %s :buzz %d from bot %d", ch, rand.IntN(1000), b.id))
			switch rand.IntN(20) {
			case 0:
				b.send("AWAY :out gathering pollen", "AWAY")
			case 1:
				b.send(fmt.Sprintf("NICK %s%04d_", *prefix, b.id))
			case 2:
				b.send("NICK " + nick)
			case 3:
				b.send("PART "+ch+" :brb", "JOIN "+ch)
			}
		}
	}
}

func (b *bot) read(ready chan struct{}) {
	r := bufio.NewReaderSize(b.conn, 32<<10)
	once := sync.Once{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		received.Add(1)
		switch {
		case strings.HasPrefix(line, "PING"):
			b.send("PONG" + strings.TrimRight(line[4:], "\r\n"))
		case strings.Contains(line, " 001 "):
			once.Do(func() { close(ready) })
		case strings.Contains(line, " PONG ") && strings.Contains(line, ":lag"):
			i := strings.LastIndex(line, ":lag")
			if ns, err := strconv.ParseInt(strings.TrimSpace(line[i+4:]), 10, 64); err == nil {
				lagMu.Lock()
				lags = append(lags, time.Since(time.Unix(0, ns)))
				lagMu.Unlock()
			}
		}
	}
}

func main() {
	flag.Parse()
	bots := make([]*bot, *n)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()
	go func() { // ramp up
		tick := time.NewTicker(time.Second / time.Duration(max(*ramp, 1)))
		defer tick.Stop()
		for i := range bots {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			bots[i] = &bot{id: i, stop: stop}
			wg.Add(1)
			go bots[i].run(&wg)
		}
	}()
	go func() { // a watch bot speaks now and then
		t := time.NewTicker(*watchMsg)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				i := rand.IntN(max(min(*watchers, *n), 1))
				if b := bots[i]; b != nil && b.w != nil {
					b.send(fmt.Sprintf("PRIVMSG %s :hello from bot %d of %d (%d online)", *watch, i, *n, registered.Load()))
				}
			}
		}
	}()
	go func() { // lag probes from 50 random bots every 2 seconds
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				for k := 0; k < 50; k++ {
					if b := bots[rand.IntN(*n)]; b != nil && b.w != nil {
						b.send("PING :lag" + strconv.FormatInt(time.Now().UnixNano(), 10))
					}
				}
			}
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	var timeout <-chan time.Time
	if *duration > 0 {
		timeout = time.After(*duration)
	}
	report := time.NewTicker(5 * time.Second)
	var lastSent, lastRecv int64
	for {
		select {
		case <-report.C:
			lagMu.Lock()
			l := lags
			lags = nil
			lagMu.Unlock()
			s, r := sent.Load(), received.Load()
			fmt.Printf("%6s  online %5d/%d  failed %d  sent %6d/s  received %7d/s  lag %s\n",
				time.Since(start).Round(time.Second), registered.Load(), *n, failed.Load(),
				(s-lastSent)/5, (r-lastRecv)/5, lagText(l))
			lastSent, lastRecv = s, r
		case <-sig:
			finish(stop, &wg)
			return
		case <-timeout:
			finish(stop, &wg)
			return
		}
	}
}

func lagText(l []time.Duration) string {
	if len(l) == 0 {
		return "-"
	}
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	p := func(q float64) string {
		return fmt.Sprintf("%.1fms", float64(l[int(q*float64(len(l)-1))])/1e6)
	}
	return "median " + p(0.5) + ", 99% " + p(0.99) + ", max " + p(1)
}

func finish(stop chan struct{}, wg *sync.WaitGroup) {
	fmt.Println("stopping: every bot quits")
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	}
	fmt.Printf("done: %d sent, %d received\n", sent.Load(), received.Load())
}
