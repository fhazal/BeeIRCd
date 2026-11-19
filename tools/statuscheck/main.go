// Command statuscheck watches the BeeIRCd servers and writes a small JSON
// file for the status panel on beeircd.org, every 30 seconds.
//
// It keeps one real IRC connection (TLS, certificate checked) per server,
// pings it, reads the version and user count, and on the hub also asks
// LINKS to see whether the leaf is linked.
//
// Deploy on the website VPS:
//
//	go build -o /usr/local/bin/beeircd-status ./tools/statuscheck
//	cp tools/statuscheck/beeircd-status.service /etc/systemd/system/
//	chown www-data /var/www/beeircd.org    (the unit runs as www-data and
//	                                        must be able to write there)
//	systemctl daemon-reload && systemctl enable --now beeircd-status
//	journalctl -u beeircd-status           (state changes and errors only)
//
// History survives restarts in /var/lib/beeircd-status/state.json (made
// by the unit's StateDirectory). The page reads status.json, nothing else.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	port       = "6697"
	waitReply  = 10 * time.Second // how long a PONG may take
	minBackoff = 5 * time.Second
	maxBackoff = 60 * time.Second
	hour       = time.Hour
)

// target is one server to watch. Watch (hub only) is the server name that
// must show up in LINKS.
type target struct {
	Name, Role, Place, Nick, Watch string
}

var targets = []target{
	{Name: "irc.bajubesi.org", Role: "hub", Place: "Malaysia", Nick: "StatusMY", Watch: "irc.skinhead.com.my"},
	{Name: "irc.skinhead.com.my", Role: "leaf", Place: "Germany", Nick: "StatusDE"},
}

func main() {
	out := flag.String("out", "/var/www/beeircd.org/status.json", "status file for the website")
	statePath := flag.String("state", "/var/lib/beeircd-status/state.json", "history kept across restarts")
	every := flag.Duration("every", 30*time.Second, "time between checks")
	flag.Parse()
	log.SetFlags(0) // journald adds the time

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	old := loadState(*statePath)
	var ws []*watcher
	for _, t := range targets {
		ws = append(ws, newWatcher(t, *every, tlsDial, old[t.Name]))
	}
	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func() { defer wg.Done(); w.run(ctx) }()
	}

	delay := 5 * time.Second // first write once the first checks are in
	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-time.After(delay):
			delay = *every
			if err := writeAtomic(*out, snapshot(ws, time.Now(), *every)); err != nil {
				log.Printf("write %s: %v", *out, err)
			}
			if err := saveState(*statePath, ws); err != nil {
				log.Printf("write %s: %v", *statePath, err)
			}
		}
	}
	wg.Wait() // watchers say QUIT :bye
	saveState(*statePath, ws)
}

// tlsDial opens a real TLS connection and checks the certificate.
func tlsDial(ctx context.Context, t target) (net.Conn, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: waitReply},
		Config:    &tls.Config{ServerName: t.Name},
	}
	return d.DialContext(ctx, "tcp", net.JoinHostPort(t.Name, port))
}

// ---- history ----

// history counts checks per hour: hour start (unix) -> {up, total}.
type history map[int64][2]int

func (h history) add(now time.Time, up bool) {
	k := now.Truncate(hour).Unix()
	b := h[k]
	if up {
		b[0]++
	}
	b[1]++
	h[k] = b
	h.prune(now)
}

// oldest is the start of the oldest hour we still show.
func oldest(now time.Time) int64 {
	return now.Truncate(hour).Add(-23 * hour).Unix()
}

// prune drops buckets older than the 24 hours we show.
func (h history) prune(now time.Time) {
	for k := range h {
		if k < oldest(now) {
			delete(h, k)
		}
	}
}

// hours returns 24 fractions, oldest first, last = this hour; -1 = no data.
func (h history) hours(now time.Time) []float64 {
	res := make([]float64, 24)
	for i := range res {
		b := h[now.Truncate(hour).Add(time.Duration(i-23)*hour).Unix()]
		res[i] = -1
		if b[1] > 0 {
			res[i] = math.Round(float64(b[0])/float64(b[1])*1000) / 1000
		}
	}
	return res
}

// uptime is the percent of checks that were up in those 24 hours (0 when
// there are no checks yet).
func (h history) uptime(now time.Time) float64 {
	var up, total int
	for k, b := range h {
		if k >= oldest(now) {
			up += b[0]
			total += b[1]
		}
	}
	if total == 0 {
		return 0
	}
	return math.Round(float64(up)/float64(total)*1000) / 10
}

// status is up/down with the rule that one failed check is not enough.
type status struct {
	up    bool
	since time.Time
	fails int
}

// record notes one check and says whether up/down changed.
func (s *status) record(ok bool, now time.Time) bool {
	if ok {
		s.fails = 0
		if !s.up {
			s.up, s.since = true, now
			return true
		}
		return false
	}
	s.fails++
	if s.fails >= 2 && s.up {
		s.up, s.since = false, now
		return true
	}
	return false
}

// ---- one watched server ----

type dialFunc func(ctx context.Context, t target) (net.Conn, error)

type watcher struct {
	t     target
	every time.Duration
	dial  dialFunc

	mu      sync.Mutex
	st      status
	hist    history
	people  *int
	ms      *int
	version string
	linked  *bool // hub only: last LINKS answer
}

func newWatcher(t target, every time.Duration, dial dialFunc, old *saved) *watcher {
	w := &watcher{t: t, every: every, dial: dial, hist: history{}}
	w.st.since = time.Now()
	if old != nil {
		for k, v := range old.Hours {
			w.hist[k] = v
		}
		w.hist.prune(time.Now())
		w.version = old.Version
		w.st.up, w.st.since = old.Up, old.Since
	}
	return w
}

// run keeps a connection up until ctx ends, reconnecting with backoff.
// Ticks that pass while we are not connected count as failed checks.
func (w *watcher) run(ctx context.Context) {
	backoff := minBackoff
	for ctx.Err() == nil {
		s, err := w.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("%s: connect: %v", w.t.Name, err)
			w.record(false, nil, nil, nil)
		} else {
			backoff = minBackoff
			w.serve(ctx, s)
		}
		if !w.sleep(ctx, backoff) {
			return
		}
		if err != nil {
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// sleep waits d, counting a failed check at every tick. False if ctx ended.
func (w *watcher) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	tick := time.NewTicker(w.every)
	defer timer.Stop()
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-tick.C:
			w.record(false, nil, nil, nil)
		}
	}
}

// serve checks on every tick and answers PINGs in between. It returns when
// the connection is lost or a check fails, or after QUIT on shutdown.
func (w *watcher) serve(ctx context.Context, s *session) {
	defer s.close()
	tick := time.NewTicker(w.every)
	defer tick.Stop()
	if !w.check(s) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			s.send("QUIT :bye")
			return
		case line, ok := <-s.lines:
			if !ok {
				log.Printf("%s: connection lost", w.t.Name)
				w.record(false, nil, nil, nil)
				return
			}
			s.handle(line)
		case <-tick.C:
			if !w.check(s) {
				return
			}
		}
	}
}

// check sends PING (timed), LUSERS and, on the hub, LINKS. It is a success
// when our PONG comes back in time.
func (w *watcher) check(s *session) bool {
	tok := strconv.FormatInt(time.Now().UnixNano(), 36)
	start := time.Now()
	s.send("PING :" + tok)
	s.send("LUSERS")
	hub := w.t.Watch != ""
	if hub {
		s.send("LINKS")
	}
	var pong, luser, links bool
	var ms, people int
	linked := false
	timer := time.NewTimer(waitReply)
	defer timer.Stop()
wait:
	for !(pong && luser && (links || !hub)) {
		select {
		case <-timer.C:
			break wait
		case line, ok := <-s.lines:
			if !ok {
				break wait
			}
			cmd, p := s.handle(line)
			switch cmd {
			case "PONG":
				if len(p) > 0 && p[len(p)-1] == tok {
					pong, ms = true, int(time.Since(start).Milliseconds())
				}
			case "265":
				if n, ok := firstNumber(p[min(1, len(p)):]); ok {
					luser, people = true, max(n-1, 0) // minus ourselves
				}
			case "364":
				for _, a := range p {
					linked = linked || strings.EqualFold(a, w.t.Watch)
				}
			case "365":
				links = true
			}
		}
	}
	if !pong {
		log.Printf("%s: no PONG within %s", w.t.Name, waitReply)
		w.record(false, nil, nil, nil)
		return false
	}
	var pp *int
	if luser {
		pp = &people
	}
	var lk *bool
	if links {
		lk = &linked
	}
	w.record(true, pp, &ms, lk)
	return true
}

// record notes a check result. A failed check leaves the numbers alone
// until the status really flips (two in a row).
func (w *watcher) record(ok bool, people, ms *int, linked *bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.hist.add(now, ok)
	changed := w.st.record(ok, now)
	if ok {
		w.ms = ms
		if people != nil {
			w.people = people
		}
		if linked != nil {
			w.linked = linked
		}
	}
	if !w.st.up {
		w.people, w.ms = nil, nil
	}
	if changed {
		log.Printf("%s: now %s", w.t.Name, map[bool]string{true: "UP", false: "DOWN"}[w.st.up])
	}
}

func (w *watcher) setVersion(v string) {
	w.mu.Lock()
	w.version = v
	w.mu.Unlock()
}

// ---- IRC connection ----

type session struct {
	w     *watcher
	c     net.Conn
	nick  string
	lines chan string
	done  chan struct{}
	once  sync.Once
}

// connect dials, registers, and returns once the server said 001.
func (w *watcher) connect(ctx context.Context) (*session, error) {
	c, err := w.dial(ctx, w.t)
	if err != nil {
		return nil, err
	}
	s := &session{w: w, c: c, nick: w.t.Nick, lines: make(chan string, 256), done: make(chan struct{})}
	go s.read()
	s.send("NICK " + s.nick)
	s.send("USER status 0 * :beeircd.org status check")
	timer := time.NewTimer(waitReply)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.close()
			return nil, ctx.Err()
		case <-timer.C:
			s.close()
			return nil, errors.New("no welcome (001) in time")
		case line, ok := <-s.lines:
			if !ok {
				s.close()
				return nil, errors.New("closed during registration")
			}
			if cmd, _ := s.handle(line); cmd == "001" {
				s.send("MODE " + s.nick + " +iB") // errors do not matter
				return s, nil
			}
		}
	}
}

func (s *session) read() {
	defer close(s.lines)
	sc := bufio.NewScanner(s.c)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		select {
		case s.lines <- strings.TrimRight(sc.Text(), "\r"):
		case <-s.done:
			return
		}
	}
}

func (s *session) send(line string) {
	s.c.SetWriteDeadline(time.Now().Add(waitReply))
	fmt.Fprintf(s.c, "%s\r\n", line)
}

func (s *session) close() {
	s.once.Do(func() { close(s.done); s.c.Close() })
}

// handle does what every line needs (PING, nick taken, version) and
// returns the parsed command for the caller.
func (s *session) handle(line string) (string, []string) {
	cmd, p := parse(line)
	switch cmd {
	case "PING":
		if len(p) > 0 {
			s.send("PONG :" + p[len(p)-1])
		}
	case "001":
		if len(p) > 0 {
			s.nick = p[0]
		}
	case "004":
		if len(p) > 2 {
			s.w.setVersion(strings.TrimPrefix(p[2], "BeeIRCd-"))
		}
	case "433": // nick in use
		s.nick += "_"
		s.send("NICK " + s.nick)
	}
	return cmd, p
}

// parse splits an IRC line into command and params (trailing included).
func parse(line string) (string, []string) {
	if strings.HasPrefix(line, "@") {
		_, line, _ = strings.Cut(line, " ")
	}
	if strings.HasPrefix(line, ":") {
		_, line, _ = strings.Cut(line, " ")
	}
	head, trail, hasTrail := strings.Cut(line, " :")
	f := strings.Fields(head)
	if len(f) == 0 {
		return "", nil
	}
	p := f[1:]
	if hasTrail {
		p = append(p, trail)
	}
	return strings.ToUpper(f[0]), p
}

// firstNumber finds the first number param; failing that, the first number
// inside the last (free text) param.
func firstNumber(p []string) (int, bool) {
	for _, a := range p {
		if n, err := strconv.Atoi(a); err == nil {
			return n, true
		}
	}
	if len(p) == 0 {
		return 0, false
	}
	digits := func(r rune) bool { return r < '0' || r > '9' }
	for _, f := range strings.FieldsFunc(p[len(p)-1], digits) {
		if n, err := strconv.Atoi(f); err == nil {
			return n, true
		}
	}
	return 0, false
}

// ---- output ----

type serverOut struct {
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	Place    string    `json:"place"`
	Up       bool      `json:"up"`
	Since    string    `json:"since"`
	People   *int      `json:"people"`
	Ms       *int      `json:"ms"`
	Version  string    `json:"version"`
	Hours    []float64 `json:"hours"`
	Uptime24 float64   `json:"uptime24"`
}

type fileOut struct {
	Checked string      `json:"checked"`
	Every   int         `json:"every"`
	Linked  *bool       `json:"linked"`
	Servers []serverOut `json:"servers"`
}

func (w *watcher) view(now time.Time) serverOut {
	w.mu.Lock()
	defer w.mu.Unlock()
	return serverOut{
		Name: w.t.Name, Role: w.t.Role, Place: w.t.Place, Up: w.st.up,
		Since: w.st.since.UTC().Format(time.RFC3339), People: w.people, Ms: w.ms,
		Version: w.version, Hours: w.hist.hours(now), Uptime24: w.hist.uptime(now),
	}
}

// snapshot builds the JSON. linked: unknown (null) when the hub is down,
// false when the leaf is down, else what the hub's LINKS said.
func snapshot(ws []*watcher, now time.Time, every time.Duration) fileOut {
	res := fileOut{Checked: now.UTC().Format(time.RFC3339), Every: int(every.Seconds()), Servers: []serverOut{}}
	up := map[string]bool{}
	for _, w := range ws {
		v := w.view(now)
		res.Servers = append(res.Servers, v)
		up[w.t.Name] = v.Up
	}
	for _, w := range ws {
		if w.t.Watch == "" || !up[w.t.Name] {
			continue
		}
		if !up[w.t.Watch] {
			f := false
			res.Linked = &f
		} else {
			w.mu.Lock()
			res.Linked = w.linked
			w.mu.Unlock()
		}
	}
	return res
}

// writeAtomic writes JSON to a temp file next to path, then renames it.
func writeAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op after a good rename
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// ---- state file ----

type saved struct {
	Hours   history   `json:"hours"`
	Up      bool      `json:"up"`
	Since   time.Time `json:"since"`
	Version string    `json:"version"`
}

func loadState(path string) map[string]*saved {
	m := map[string]*saved{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("state %s unreadable, starting fresh: %v", path, err)
			m = map[string]*saved{}
		}
	}
	return m
}

func saveState(path string, ws []*watcher) error {
	m := map[string]*saved{}
	for _, w := range ws {
		w.mu.Lock()
		h := history{}
		for k, v := range w.hist {
			h[k] = v
		}
		m[w.t.Name] = &saved{Hours: h, Up: w.st.up, Since: w.st.since, Version: w.version}
		w.mu.Unlock()
	}
	return writeAtomic(path, m)
}
