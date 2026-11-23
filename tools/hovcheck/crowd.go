package main

import (
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// crowd is the bots and what they found out.
type crowd struct {
	plain, tlsAddr          string
	n                       int
	admin, adminPass, nsPas string
	spread                  bool

	logMu sync.Mutex
	log   *os.File

	mu       sync.Mutex // one command at a time
	bots     []*bot
	ready    bool
	base     map[string]string // facts from before any switch
	quitting atomic.Bool
	lagMax   [2]atomic.Int64 // longest PING answer since the last check (ns): plain bot, TLS bot
}

// bot is one connection. The bots never reconnect.
type bot struct {
	c      *crowd
	i      int
	nick   string
	tls    bool
	caps   bool // asked for server-time + message-tags
	admin  bool // SASL PLAIN, then /oper
	conn   net.Conn
	wmu    sync.Mutex
	mu     sync.Mutex
	lines  []string
	got    map[string]bool // hov: tokens received
	tagged map[string]bool // ... with a time= tag
	gone   string          // why the connection ended; "" while up
	events []string        // ERROR, QUIT or KILL seen, forced nick change
	seen   int             // events already reported
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	c := &crowd{}
	fs.StringVar(&c.plain, "plain", "127.0.0.1:6667", "plain port")
	fs.StringVar(&c.tlsAddr, "tls", "127.0.0.1:6697", "TLS port")
	fs.IntVar(&c.n, "n", 50, "bots, the last one is the admin")
	fs.StringVar(&c.admin, "admin", "Bee", "the admin's nick, account and oper name")
	fs.StringVar(&c.adminPass, "pass", "", "the admin's /oper password")
	fs.StringVar(&c.nsPas, "nspass", "", "the admin's NickServ password (SASL PLAIN)")
	fs.BoolVar(&c.spread, "spread", true, "each bot connects from its own 127.0.1.x address (loopback targets only)")
	ctl := fs.String("ctl", "127.0.0.1:8098", "where to listen for hovcheck ask")
	logPath := fs.String("log", "hovbots.log", "every line every bot received")
	fs.Parse(args)
	if c.n < 12 {
		fmt.Fprintln(os.Stderr, "-n must be at least 12")
		return 2
	}
	var err error
	if c.log, err = os.Create(*logPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	handle := func(path string, f func(r *http.Request) string) {
		http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			c.mu.Lock()
			out := f(r)
			c.mu.Unlock()
			c.logf("=== %s\n%s", r.URL, out)
			fmt.Fprint(w, out)
		})
	}
	handle("/ready", func(*http.Request) string { return c.setup() })
	handle("/check", func(r *http.Request) string { return c.check(r.URL.Query().Get("label")) })
	handle("/restart", func(*http.Request) string { return c.restart() })
	handle("/quit", func(*http.Request) string {
		out := c.quit()
		go func() { time.Sleep(time.Second); os.Exit(0) }()
		return out
	})
	if err := http.ListenAndServe(*ctl, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func (c *crowd) logf(f string, a ...any) {
	c.logMu.Lock()
	fmt.Fprintf(c.log, time.Now().Format("15:04:05.000 ")+f+"\n", a...)
	c.logMu.Unlock()
}

// report collects OK / FAIL / INFO lines.
type report struct {
	b          strings.Builder
	pass, fail int
}

func (r *report) ok(cond bool, f string, a ...any) bool {
	if cond {
		r.pass++
		fmt.Fprintf(&r.b, "OK   "+f+"\n", a...)
	} else {
		r.fail++
		fmt.Fprintf(&r.b, "FAIL "+f+"\n", a...)
	}
	return cond
}

func (r *report) info(f string, a ...any) { fmt.Fprintf(&r.b, "INFO "+f+"\n", a...) }

func (c *crowd) group(i int) string { return fmt.Sprintf("#hov-g%d", i/10) }

// ---- one bot ----

func (b *bot) send(lines ...string) {
	b.wmu.Lock()
	defer b.wmu.Unlock()
	if b.conn == nil {
		return
	}
	b.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	for _, l := range lines {
		if _, err := b.conn.Write([]byte(l + "\r\n")); err != nil {
			b.c.logf("%s write: %v", b.nick, err)
			return
		}
	}
}

func (b *bot) up() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gone == ""
}

func (b *bot) read() {
	r := newLineReader(b.conn)
	for {
		line, err := r.next()
		if err != nil {
			if !b.c.quitting.Load() {
				b.mu.Lock()
				last := ""
				if len(b.lines) > 0 {
					last = b.lines[len(b.lines)-1]
				}
				b.gone = fmt.Sprintf("%s connection ended: %v (last line: %q)", time.Now().Format("15:04:05.000"), err, last)
				b.mu.Unlock()
				b.c.logf("%s GONE: %s", b.nick, b.gone)
			}
			return
		}
		b.c.logf("%s < %s", b.nick, line)
		m := parse(line)
		var event string
		switch m.cmd {
		case "PING":
			b.send("PONG :" + m.last())
		case "PONG":
			if t := m.last(); strings.HasPrefix(t, "lag") {
				var ns int64
				fmt.Sscan(t[3:], &ns)
				d := time.Since(time.Unix(0, ns)).Nanoseconds()
				k := map[bool]int{false: 0, true: 1}[b.tls]
				for {
					old := b.c.lagMax[k].Load()
					if d <= old || b.c.lagMax[k].CompareAndSwap(old, d) {
						break
					}
				}
			}
		case "PRIVMSG":
			if t := m.last(); strings.HasPrefix(t, "hov:") {
				b.mu.Lock()
				b.got[t] = true
				if strings.Contains(m.tags, "time=") {
					b.tagged[t] = true
				}
				b.mu.Unlock()
			}
		case "ERROR", "QUIT", "KILL":
			event = line
		case "NICK":
			if nickOf(m.prefix) == b.nick {
				event = "forced nick change: " + line
			}
		}
		b.mu.Lock()
		b.lines = append(b.lines, line)
		if event != "" {
			b.events = append(b.events, event)
		}
		b.mu.Unlock()
	}
}

func (b *bot) mark() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}

// waitFor waits for a line from index from on that matches; it returns the
// line (or "") and the index after it.
func (b *bot) waitFor(from int, match func(msg) bool, d time.Duration) (string, int) {
	end := time.Now().Add(d)
	for {
		b.mu.Lock()
		for ; from < len(b.lines); from++ {
			if match(parse(b.lines[from])) {
				l := b.lines[from]
				b.mu.Unlock()
				return l, from + 1
			}
		}
		b.mu.Unlock()
		if time.Now().After(end) {
			return "", from
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// query sends line and returns what came back up to and including the
// line where done is true (nil on time-out).
func (b *bot) query(line string, done func(msg) bool) []msg {
	from := b.mark()
	b.send(line)
	end, _ := b.waitFor(from, done, 10*time.Second)
	if end == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []msg
	for _, l := range b.lines[from:] {
		m := parse(l)
		out = append(out, m)
		if done(m) {
			break
		}
	}
	return out
}

func is(cmd string) func(msg) bool { return func(m msg) bool { return m.cmd == cmd } }

func (b *bot) connect() error {
	addr := b.c.plain
	if b.tls {
		addr = b.c.tlsAddr
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	if host, _, _ := net.SplitHostPort(addr); b.c.spread && net.ParseIP(host).IsLoopback() {
		d.LocalAddr = &net.TCPAddr{IP: net.IPv4(127, 0, 1, byte(b.i+1))}
	}
	var err error
	if b.tls {
		b.conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	} else {
		b.conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	go b.read()
	from := 0
	switch {
	case b.admin:
		b.send("CAP REQ :sasl")
	case b.caps:
		b.send("CAP REQ :server-time message-tags")
	}
	b.send("NICK "+b.nick, "USER hov 0 * :hovcheck bot")
	if b.admin {
		var l string
		if l, from = b.waitFor(from, is("CAP"), 10*time.Second); !strings.Contains(l, "ACK") {
			return fmt.Errorf("CAP REQ sasl: %q", l)
		}
		b.send("AUTHENTICATE PLAIN")
		if l, from = b.waitFor(from, is("AUTHENTICATE"), 10*time.Second); l == "" {
			return fmt.Errorf("no AUTHENTICATE +")
		}
		b.send("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte(b.c.admin+"\x00"+b.c.admin+"\x00"+b.c.nsPas)))
		if l, from = b.waitFor(from, func(m msg) bool { return m.cmd == "903" || m.cmd == "904" || m.cmd == "902" }, 10*time.Second); parse(l).cmd != "903" {
			return fmt.Errorf("SASL PLAIN as %s: %q", b.c.admin, l)
		}
	}
	if b.admin || b.caps {
		b.send("CAP END")
	}
	if l, _ := b.waitFor(from, is("001"), 30*time.Second); l == "" {
		b.mu.Lock()
		defer b.mu.Unlock()
		return fmt.Errorf("no 001 in 30 s (last: %q)", strings.Join(lastN(b.lines, 3), " | "))
	}
	return nil
}

func lastN(l []string, n int) []string {
	if len(l) > n {
		return l[len(l)-n:]
	}
	return l
}

// ---- commands ----

func (c *crowd) setup() string {
	var r report
	if c.ready {
		r.ok(false, "ready was already done")
		return r.b.String()
	}
	start := time.Now()
	for i := 0; i < c.n; i++ {
		b := &bot{c: c, i: i, nick: fmt.Sprintf("hb%02d", i), tls: i%2 == 1, caps: i >= 4 && i <= 7,
			got: map[string]bool{}, tagged: map[string]bool{}}
		if i == c.n-1 {
			b.nick, b.tls, b.admin = c.admin, true, true
		}
		c.bots = append(c.bots, b)
	}
	var wg sync.WaitGroup
	errs := make([]error, c.n)
	for i, b := range c.bots {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = b.connect() }()
		time.Sleep(40 * time.Millisecond)
	}
	wg.Wait()
	connected, tlsN := 0, 0
	for i, e := range errs {
		if e != nil {
			r.ok(false, "%s connects: %v", c.bots[i].nick, e)
		} else {
			connected++
			if c.bots[i].tls {
				tlsN++
			}
		}
	}
	r.ok(connected == c.n, "%d of %d bots connected (%d TLS, %d plain; %s by SASL PLAIN)", connected, c.n, tlsN, connected-tlsN, c.admin)
	if connected != c.n {
		return r.b.String()
	}
	adm := c.bots[c.n-1]
	from := adm.mark()
	adm.send("OPER " + c.admin + " " + c.adminPass)
	l, _ := adm.waitFor(from, func(m msg) bool { return m.cmd == "381" || m.cmd == "491" || m.cmd == "464" }, 10*time.Second)
	r.ok(parse(l).cmd == "381", "%s /oper (381)", c.admin)

	// Channels: #hov for all, #hov-gN for each ten; the first one in ops.
	joined := func(b *bot, chans ...string) bool {
		from := b.mark()
		b.send("JOIN " + strings.Join(chans, ","))
		for _, ch := range chans {
			if l, _ := b.waitFor(from, func(m msg) bool { return m.cmd == "366" && strings.EqualFold(m.param(1), ch) }, 10*time.Second); l == "" {
				return false
			}
		}
		return true
	}
	bad := 0
	for i, b := range c.bots {
		if i == 0 || i%10 == 0 {
			chans := []string{c.group(i)}
			if i == 0 {
				chans = []string{"#hov", c.group(i)}
			}
			if !joined(b, chans...) {
				bad++
			}
		}
	}
	for i, b := range c.bots {
		if i == 0 {
			continue
		}
		chans := []string{"#hov"}
		if i%10 != 0 {
			chans = append(chans, c.group(i))
		}
		if !joined(b, chans...) {
			bad++
		}
	}
	r.ok(bad == 0, "every bot joined #hov and its group channel (%d failed)", bad)
	b0 := c.bots[0]
	b0.send("TOPIC #hov :hand-over test topic, keep me", "MODE #hov +sl 200", "MODE #hov +v hb01")
	for i := 0; i < c.n; i += 10 {
		c.bots[i].send(fmt.Sprintf("TOPIC %s :group %d", c.group(i), i/10))
	}
	c.bots[8].send("AWAY :hov away test")
	c.bots[9].send("MONITOR + hb10")

	// Two NickServ accounts (30 s online first, as in Anope).
	if w := 31*time.Second - time.Since(start); w > 0 {
		time.Sleep(w)
	}
	for _, i := range []int{2, 3} {
		b := c.bots[i]
		from := b.mark()
		b.send(fmt.Sprintf("PRIVMSG NickServ :REGISTER hov-ns-pass-%02d %s@example.org", i, b.nick))
		l, _ := b.waitFor(from, func(m msg) bool {
			return (m.cmd == "NOTICE" || m.cmd == "PRIVMSG") && strings.Contains(m.last(), "registered")
		}, 10*time.Second)
		r.ok(l != "", "%s NickServ REGISTER", b.nick)
	}
	time.Sleep(time.Second)

	c.base = c.facts()
	keys := sortedKeys(c.base)
	for _, k := range keys {
		r.info("%-22s %s", k, c.base[k])
	}
	r.ok(strings.Count(c.base["names #hov"], " ")+1 == c.n, "#hov has all %d bots", c.n)
	r.ok(strings.Contains(c.base["names #hov"], "@hb00") && strings.Contains(c.base["names #hov"], "+hb01"), "#hov has @hb00 and +hb01")
	r.ok(strings.Contains(c.base["topic #hov"], "hand-over test topic"), "#hov topic set")
	r.ok(strings.Contains(c.base["modes #hov"], "s") && strings.Contains(c.base["modes #hov"], "200"), "#hov modes +s +l 200")
	r.ok(strings.HasPrefix(c.base["whois hb02"], "account=hb02"), "hb02 logged in to its account")
	r.ok(strings.HasPrefix(c.base["whois hb03"], "account=hb03"), "hb03 logged in to its account")
	r.ok(strings.HasPrefix(c.base["whois "+c.admin], "account="+c.admin+" oper=yes"), "%s logged in (SASL) and oper", c.admin)
	r.ok(strings.Contains(c.base["whois hb08"], "away=hov away test"), "hb08 away")
	r.ok(strings.Contains(strings.ToLower(c.base["monitor hb09"]), "hb10"), "hb09 monitors hb10")
	r.ok(strings.Contains(c.base["caps hb04"], "server-time"), "hb04 has server-time")
	c.chat(&r, "ready")
	c.ready = true
	go c.lagProbe()
	fmt.Fprintf(&r.b, "SUMMARY ready: %d OK, %d FAIL\n", r.pass, r.fail)
	return r.b.String()
}

// facts is what must not change across a switch.
func (c *crowd) facts() map[string]string {
	f := map[string]string{}
	chans := []struct {
		name string
		by   *bot
	}{{"#hov", c.bots[0]}}
	for i := 0; i < c.n; i += 10 {
		chans = append(chans, struct {
			name string
			by   *bot
		}{c.group(i), c.bots[i]})
	}
	for _, ch := range chans {
		var names []string
		for _, m := range ch.by.query("NAMES "+ch.name, is("366")) {
			if m.cmd == "353" {
				names = append(names, strings.Fields(m.last())...)
			}
		}
		sort.Strings(names)
		f["names "+ch.name] = strings.Join(names, " ")
		for _, m := range ch.by.query("MODE "+ch.name, func(m msg) bool { return m.cmd == "324" || m.cmd == "403" || m.cmd == "442" }) {
			if m.cmd == "324" {
				f["modes "+ch.name] = strings.Join(m.params[2:], " ")
			}
		}
		var topic []string
		for _, m := range ch.by.query("TOPIC "+ch.name, func(m msg) bool { return m.cmd == "333" || m.cmd == "331" || m.cmd == "403" }) {
			switch m.cmd {
			case "332":
				topic = append(topic, m.last())
			case "333":
				topic = append(topic, "by "+strings.Join(m.params[2:], " at "))
			case "331":
				topic = append(topic, "(none)")
			}
		}
		f["topic "+ch.name] = strings.Join(topic, " ")
	}
	for _, nick := range []string{"hb02", "hb03", "hb08", c.admin} {
		acct, oper, away := "-", "no", "-"
		for _, m := range c.bots[0].query("WHOIS "+nick, is("318")) {
			switch m.cmd {
			case "330":
				acct = m.param(2)
			case "313":
				oper = "yes"
			case "301":
				away = m.last()
			}
		}
		f["whois "+nick] = fmt.Sprintf("account=%s oper=%s away=%s", acct, oper, away)
	}
	var mon []string
	for _, m := range c.bots[9].query("MONITOR L", is("733")) {
		if m.cmd == "732" {
			mon = append(mon, m.last())
		}
	}
	f["monitor hb09"] = strings.Join(mon, ",")
	for i := 4; i <= 7; i++ {
		for _, m := range c.bots[i].query("CAP LIST", is("CAP")) {
			if m.cmd == "CAP" {
				caps := strings.Fields(m.last())
				sort.Strings(caps)
				f[fmt.Sprintf("caps hb%02d", i)] = strings.Join(caps, " ")
			}
		}
	}
	return f
}

func sortedKeys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// chat: every bot says one line in its group channel and one to the next
// bot; hb00 says one in #hov. Every line must reach everyone it should.
func (c *crowd) chat(r *report, label string) {
	tok := func(from int, kind string) string { return fmt.Sprintf("hov:%s:%d:%s", label, from, kind) }
	want := map[*bot][]string{}
	for i, b := range c.bots {
		b.send("PRIVMSG "+c.group(i)+" :"+tok(i, "g"), "PRIVMSG "+c.bots[(i+1)%c.n].nick+" :"+tok(i, "p"))
		for j, o := range c.bots {
			if j != i && c.group(j) == c.group(i) {
				want[o] = append(want[o], tok(i, "g"))
			}
		}
		want[c.bots[(i+1)%c.n]] = append(want[c.bots[(i+1)%c.n]], tok(i, "p"))
	}
	c.bots[0].send("PRIVMSG #hov :" + tok(0, "a"))
	for _, o := range c.bots[1:] {
		want[o] = append(want[o], tok(0, "a"))
	}
	total := 0
	for _, w := range want {
		total += len(w)
	}
	missing := func() (miss []string, untagged int) {
		for _, b := range c.bots {
			b.mu.Lock()
			for _, t := range want[b] {
				if !b.got[t] {
					miss = append(miss, b.nick+"<-"+t)
				} else if b.caps && !b.tagged[t] {
					untagged++
				}
			}
			b.mu.Unlock()
		}
		return
	}
	end := time.Now().Add(20 * time.Second)
	var miss []string
	var untagged int
	for {
		miss, untagged = missing()
		if len(miss) == 0 || time.Now().After(end) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.ok(len(miss) == 0, "chat %s: %d of %d messages arrived (group channels, private, #hov)", label, total-len(miss), total)
	if len(miss) > 0 {
		r.info("missing: %s", strings.Join(lastN(miss, 20), " "))
	}
	r.ok(untagged == 0, "chat %s: server-time bots got time= on every message (%d without)", label, untagged)
}

func (c *crowd) lagProbe() {
	// Once a second, under the server's flood_rate (default 2 lines/s):
	// faster and the server's flood slowdown would hold these bots' lines
	// back, and that wait would show up as hand-over lag. The two bots
	// just before the oper bot (one TLS, one plain) send them: not hb00,
	// which asks the checks' NAMES/MODE/TOPIC/WHOIS, so the probe can't
	// delay those answers.
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		if c.quitting.Load() {
			return
		}
		for _, b := range c.bots[c.n-3 : c.n-1] {
			b.send(fmt.Sprintf("PING :lag%d", time.Now().UnixNano()))
		}
	}
}

func (c *crowd) check(label string) string {
	var r report
	if !c.ready {
		r.ok(false, "ready first")
		return r.b.String()
	}
	if label == "" {
		label = "check"
	}
	label = regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(label, "_")
	up := 0
	for _, b := range c.bots {
		b.mu.Lock()
		if b.gone == "" {
			up++
		} else {
			r.info("%s lost: %s", b.nick, b.gone)
		}
		ev := b.events[b.seen:]
		b.seen = len(b.events)
		b.mu.Unlock()
		for _, e := range ev {
			r.ok(false, "%s saw: %s", b.nick, e)
		}
	}
	r.ok(up == c.n, "%s: %d of %d bots still connected (none reconnected: bots never do)", label, up, c.n)
	// Everyone answers.
	answered := 0
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, b := range c.bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			from := b.mark()
			b.send("PING :alive-" + label)
			if l, _ := b.waitFor(from, func(m msg) bool { return m.cmd == "PONG" && m.last() == "alive-"+label }, 15*time.Second); l != "" {
				mu.Lock()
				answered++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	r.ok(answered == c.n, "%s: %d of %d bots get answers (PING/PONG)", label, answered, c.n)
	c.chat(&r, label)
	now := c.facts()
	for _, k := range sortedKeys(c.base) {
		r.ok(now[k] == c.base[k], "%s: %s unchanged", label, k)
		if now[k] != c.base[k] {
			r.info("before: %s", c.base[k])
			r.info("now:    %s", now[k])
		}
	}
	r.info("longest PING answer since the last check: plain %s, TLS %s",
		time.Duration(c.lagMax[0].Swap(0)).Round(time.Millisecond), time.Duration(c.lagMax[1].Swap(0)).Round(time.Millisecond))
	fmt.Fprintf(&r.b, "SUMMARY %s: %d OK, %d FAIL\n", label, r.pass, r.fail)
	return r.b.String()
}

// restart is /as restart by the admin bot, with the confirm code.
func (c *crowd) restart() string {
	var r report
	if !c.ready {
		r.ok(false, "ready first")
		return r.b.String()
	}
	adm := c.bots[c.n-1]
	from := adm.mark()
	adm.send("PRIVMSG AdminServ :RESTART")
	re := regexp.MustCompile(`(?i)/as restart (\S+)`)
	l, from := adm.waitFor(from, func(m msg) bool { return (m.cmd == "NOTICE" || m.cmd == "PRIVMSG") && re.MatchString(m.last()) }, 10*time.Second)
	if !r.ok(l != "", "AdminServ RESTART asks for a code") {
		return r.b.String()
	}
	adm.mu.Lock()
	for _, x := range adm.lines[max(0, from-3):from] {
		if p := parse(x); p.cmd == "NOTICE" || p.cmd == "PRIVMSG" {
			r.info("AdminServ: %s", p.last())
		}
	}
	adm.mu.Unlock()
	code := re.FindStringSubmatch(parse(l).last())[1]
	adm.send("PRIVMSG AdminServ :RESTART " + code + " hovcheck restart")
	l, _ = adm.waitFor(from, func(m msg) bool {
		return (m.cmd == "NOTICE" || m.cmd == "PRIVMSG") && strings.Contains(m.last(), "Going now")
	}, 10*time.Second)
	r.ok(l != "", "AdminServ RESTART %s: Going now.", code)
	return r.b.String()
}

func (c *crowd) quit() string {
	var r report
	c.quitting.Store(true)
	total := 0
	for _, b := range c.bots {
		b.mu.Lock()
		total += len(b.events)
		if b.gone != "" {
			r.info("%s lost: %s", b.nick, b.gone)
		}
		b.mu.Unlock()
	}
	for _, b := range c.bots {
		b.send("QUIT :hovcheck done")
	}
	r.ok(total == 0, "over the whole run the bots saw %d ERROR/QUIT/KILL/forced nick lines", total)
	time.Sleep(500 * time.Millisecond)
	return r.b.String()
}
