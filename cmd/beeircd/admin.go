package main

// The admin commands: status, check, backup, rehash, start, stop, restart
// (step 7b). They read beeircd.conf to find the control socket, and talk
// to the running server through it.

import (
	"github.com/fhazal/beeircd/internal/acmecert"

	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/history"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
	"github.com/fhazal/beeircd/internal/version"
)

// serviceName is the systemd unit the installer sets up.
const serviceName = "beeircd"

// paint colours a few words for a terminal: the status dot and rows that
// need you. Off when the output isn't a terminal or NO_COLOR is set.
type paint struct{ on bool }

func colourFor(w io.Writer) paint {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return paint{}
	}
	fi, err := f.Stat()
	return paint{on: err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

func (p paint) wrap(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p paint) ok(s string) string    { return p.wrap("32", s) }
func (p paint) warn(s string) string  { return p.wrap("33", s) }
func (p paint) bad(s string) string   { return p.wrap("31", s) }
func (p paint) dim(s string) string   { return p.wrap("2", s) }
func (p paint) title(s string) string { return p.wrap("1", s) }

// adminFlags reads -config for an admin command; rest are the other words.
func adminFlags(name string, args []string, stderr io.Writer) (path string, rest []string, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	p := fs.String("config", defaultConfig, "config file")
	if err := fs.Parse(args); err != nil {
		return "", nil, false
	}
	return *p, fs.Args(), true
}

func ask(cfg *config.Config, cmd, path string, wait time.Duration) (*control.Response, error) {
	return control.Ask(cfg.Control.Socket, control.Request{Cmd: cmd, Path: path}, wait)
}

// ---- status ----

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	path, _, ok := adminFlags("status", args, stderr)
	if !ok {
		return 2
	}
	p := colourFor(stdout)
	cfg, cfgErr := config.Load(path)
	if cfgErr == nil {
		resp, err := ask(cfg, control.CmdStatus, "", 5*time.Second)
		if err == nil && resp.Status != nil {
			fmt.Fprint(stdout, statusScreen(resp.Status, p, time.Now(), backupTimerOff()))
			return 0
		}
	}
	fmt.Fprint(stdout, stoppedScreen(p, cfgErr))
	return 3 // like systemctl status for a stopped service
}

func statusScreen(st *control.Status, p paint, now time.Time, timerOff bool) string {
	var b strings.Builder
	row := func(name, text string) { fmt.Fprintf(&b, "  %-10s %s\n", name, text) }
	fmt.Fprintf(&b, "\n  %s   %s  for %s   %s\n", p.title("BeeIRCd "+st.Version), p.ok("● running"), since(now.Sub(st.Started)), p.dim("pid "+strconv.Itoa(st.PID)))
	row("Server", st.Server+"  · "+st.Network)
	var ports []string
	for _, pt := range st.Ports {
		kind := "plain"
		if pt.TLS {
			kind = "TLS"
		}
		if pt.Proxy {
			kind += " (proxy)"
		}
		ports = append(ports, portOf(pt.Address)+" "+kind)
	}
	row("Ports", strings.Join(ports, " · "))
	row("People", fmt.Sprintf("%d online %s · %s · %s", st.Online, p.dim(fmt.Sprintf("(peak %d)", st.Peak)), plural(st.Channels, "channel"), plural(st.Opers, "oper")))
	row("Services", strings.Join(st.Services, "  ")+"   "+p.ok("all up"))
	row("Load", fmt.Sprintf("%.0f MB memory · answers in %s %s", st.MemoryMB, ms(st.LagAvgMS), p.dim("(99% under "+ms(st.Lag99MS)+")")))
	data := "services.db " + size(st.DBBytes)
	if st.HistoryChannels > 0 {
		data += " · history " + size(st.HistoryBytes) + " in " + plural(st.HistoryChannels, "channel")
	}
	row("Data", data)
	if c := st.Cert; c != nil {
		left := c.NotAfter.Sub(now)
		text := c.Source + ", valid until " + c.NotAfter.Format("2 Jan 2006")
		switch {
		case strings.Contains(c.Source, "stand-in") && c.RenewFailures > 0:
			b.WriteString(p.warn(fmt.Sprintf("  %-10s Let's Encrypt has no certificate yet (self-signed stand-in) · failed %s: %s", "Cert", times(c.RenewFailures), c.RenewError)) + "\n")
		case strings.Contains(c.Source, "stand-in"):
			b.WriteString(p.warn(fmt.Sprintf("  %-10s Let's Encrypt: asking for the first certificate (self-signed stand-in until then)", "Cert")) + "\n")
		case c.RenewFailures > 0:
			b.WriteString(p.warn(fmt.Sprintf("  %-10s valid until %s · renewal failed %s: %s", "Cert", c.NotAfter.Format("2 Jan 2006"), times(c.RenewFailures), c.RenewError)) + "\n")
		case (c.RenewAt.IsZero() && left < 14*24*time.Hour) || (!c.RenewAt.IsZero() && now.After(c.RenewAt.Add(24*time.Hour))):
			b.WriteString(p.warn(fmt.Sprintf("  %-10s %s · %s left", "Cert", text, since(left))) + "\n")
		case !c.RenewAt.IsZero():
			row("Cert", text+" "+p.dim("(renews "+c.RenewAt.Format("2 Jan")+")"))
		default:
			row("Cert", text)
		}
	}
	keep := filepath.Clean(st.BackupDir)
	last, any := control.LastBackup(st.BackupDir)
	timer := ""
	if timerOff {
		timer = " · the daily timer is off (systemctl enable " + serviceName + "-backup.timer)"
	}
	switch {
	case !any:
		b.WriteString(p.warn(fmt.Sprintf("  %-10s none yet%s", "Backup", or(timer, " · beeircd backup makes one"))) + "\n")
	case now.Sub(last) > 48*time.Hour:
		b.WriteString(p.warn(fmt.Sprintf("  %-10s last one %s%s", "Backup", ago(last, now), timer)) + "\n")
	default:
		row("Backup", ago(last, now)+" "+p.dim(fmt.Sprintf("· %d kept in %s", len(control.Backups(keep)), keep)))
	}
	for i, l := range linksRow(st, p, now) {
		name := "Links"
		if i > 0 {
			name = ""
		}
		row(name, l)
	}
	if u := st.Update; u != nil {
		b.WriteString(p.warn(fmt.Sprintf("  %-10s %s is out (%s) · beeircd update", "Update", u.Version, shortDate(u.Date))) + "\n")
	}
	sf := st.Safety
	var safe []string
	if sf.Throttled > 0 {
		safe = append(safe, plural(sf.Throttled, "connection")+" throttled")
	}
	if sf.Zlined > 0 {
		safe = append(safe, plural(sf.Zlined, "IP")+" Z-lined")
	}
	if sf.DNSBLHits > 0 {
		safe = append(safe, plural(sf.DNSBLHits, "DNSBL hit"))
	}
	if len(safe) > 0 {
		row("Safety", "today "+strings.Join(safe, " · "))
	}
	return b.String()
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// stoppedScreen is status when nothing answers.
func stoppedScreen(p paint, cfgErr error) string {
	var b strings.Builder
	line := fmt.Sprintf("\n  %s   %s", p.title("BeeIRCd "+version.Version), p.bad("● stopped"))
	if since, why := systemdStopped(); since != "" {
		line += "  since " + since + " " + p.dim("("+why+")")
	}
	b.WriteString(line + "\n")
	if logs := lastLogLines(5); len(logs) > 0 {
		b.WriteString("\n  Last lines of the log:\n")
		for _, l := range logs {
			b.WriteString("  " + l + "\n")
		}
	}
	b.WriteString("\n")
	start := "beeircd start"
	switch {
	case inContainer:
		start = "docker compose up -d"
	case !systemdHere():
		start = "your service manager (OpenRC: rc-service beeircd start)"
	}
	if cfgErr != nil {
		b.WriteString("  " + p.bad("✗") + " " + cfgErr.Error() + "\n")
		b.WriteString("  Fix beeircd.conf, then:  beeircd check  and start it with " + start + "\n")
	} else {
		b.WriteString("  Start it with:  " + start + "\n")
	}
	return b.String()
}

// ---- check ----

type checker struct {
	w    io.Writer
	p    paint
	fail bool
}

func (c *checker) ok(text string, extra ...string) {
	fmt.Fprintf(c.w, "  %s %s%s\n", c.p.ok("✓"), text, dimExtra(c.p, extra))
}
func (c *checker) warn(text string) { fmt.Fprintf(c.w, "  %s %s\n", c.p.warn("!"), c.p.warn(text)) }
func (c *checker) bad(text string) {
	c.fail = true
	fmt.Fprintf(c.w, "  %s %s\n", c.p.bad("✗"), text)
}

func dimExtra(p paint, extra []string) string {
	if len(extra) == 0 || extra[0] == "" {
		return ""
	}
	return "  " + p.dim(extra[0])
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	path, _, ok := adminFlags("check", args, stderr)
	if !ok {
		return 2
	}
	c := &checker{w: stdout, p: colourFor(stdout)}
	runChecks(c, path, time.Now())
	if c.fail {
		c.bad("not safe to restart: fix the lines above first")
		return 1
	}
	c.ok("safe to restart")
	return 0
}

// runChecks is check without the last line, for check and restart.
func runChecks(c *checker, path string, now time.Time) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		c.bad("config: " + err.Error())
		return nil
	}
	c.ok("config reads cleanly")
	if cfg.NeedsTLS() {
		host := cfg.TLS.Domains(cfg.Server.Name)[0]
		leaf, err := tlsutil.Describe(cfg.TLS.Cert, cfg.TLS.Key, host, now)
		switch {
		case err != nil && cfg.TLS.ACME && errors.Is(err, os.ErrNotExist):
			c.ok("certificate: asked from Let's Encrypt at start (port 80 of " + host + " must reach this server" + acmeVia(cfg.TLS.ACMEHTTP) + ")")
		case err != nil && cfg.TLS.SelfSigned && errors.Is(err, os.ErrNotExist):
			c.ok("certificate: a self-signed one is made at start")
		case err == nil && cfg.TLS.ACME && acmecert.SelfSigned(leaf):
			c.warn("certificate: still the self-signed stand-in; Let's Encrypt hasn't given one yet (see beeircd status)")
		case err == nil && cfg.TLS.ACME && !now.After(acmecert.RenewAt(leaf).Add(24*time.Hour)):
			c.ok("certificate matches "+host+", valid until "+leaf.NotAfter.Format("2 Jan 2006"), "(Let's Encrypt, renews "+acmecert.RenewAt(leaf).Format("2 Jan")+")")
		case errors.Is(err, tlsutil.ErrOtherName):
			c.warn("certificate is for " + strings.Join(leaf.DNSNames, ", ") + ", not the server name " + cfg.Server.Name +
				" (fine when clients connect by that name), valid until " + leaf.NotAfter.Format("2 Jan 2006"))
		case err != nil:
			c.bad(err.Error())
		case leaf.NotAfter.Sub(now) < 14*24*time.Hour:
			c.warn("certificate for " + host + " runs out on " + leaf.NotAfter.Format("2 Jan 2006"))
		default:
			c.ok("certificate matches "+host+", valid until "+leaf.NotAfter.Format("2 Jan 2006"), "")
		}
	}
	checkLinks(c, cfg)
	running := false
	if resp, err := ask(cfg, control.CmdCheck, "", 30*time.Second); err == nil && resp.Check != nil {
		running = true
		ck := resp.Check
		if ck.DBError != "" {
			c.bad("services database: " + ck.DBError)
		} else {
			c.ok("services database OK", fmt.Sprintf("(%d nicks, %d channels, %d memos)", ck.Nicks, ck.Channels, ck.Memos))
		}
	} else {
		sum, err := store.CheckFile(cfg.Services.Database)
		switch {
		case errors.Is(err, os.ErrNotExist):
			c.ok("services database: none yet, made at the first start")
		case errors.Is(err, store.ErrInUse):
			c.bad("services database is in use, but the server doesn't answer on " + cfg.Control.Socket)
		case err != nil:
			c.bad("services database: " + err.Error())
		default:
			c.ok("services database OK", fmt.Sprintf("(%d nicks, %d channels, %d memos)", sum.Nicks, sum.Channels, sum.Memos))
		}
	}
	if h := cfg.History; h.Secret != "" {
		hs := history.New(0, irc.Fold)
		chans, _, err := hs.Load(h.File, h.Secret, now)
		switch {
		case errors.Is(err, history.ErrWrongSecret):
			c.warn("channel history: " + filepath.Base(h.File) + " was saved with another secret, so it starts empty")
		case err != nil:
			c.warn("channel history: " + err.Error())
		default:
			c.ok("channel history opens with its secret", fmt.Sprintf("(%s)", plural(chans, "channel")))
		}
	}
	if !running {
		for _, l := range cfg.Listen {
			ln, err := net.Listen("tcp", l.Address)
			if err != nil {
				c.bad("port " + l.Address + " is in use by another program")
				continue
			}
			ln.Close()
		}
	}
	return cfg
}

// ---- backup ----

func cmdBackup(args []string, stdout, stderr io.Writer) int {
	path, rest, ok := adminFlags("backup", args, stderr)
	if !ok {
		return 2
	}
	c := &checker{w: stdout, p: colourFor(stdout)}
	cfg, err := config.Load(path)
	if err != nil {
		c.bad("config: " + err.Error())
		return 1
	}
	dst := ""
	if len(rest) > 0 {
		if dst, err = filepath.Abs(rest[0]); err != nil {
			c.bad(err.Error())
			return 1
		}
	}
	if dst != "" {
		if _, err := os.Stat(dst); err == nil {
			c.bad("backup: " + dst + " exists already")
			return 1
		}
	}
	var b *control.Backup
	named := ""
	if dst != "" {
		named = "named"
	}
	resp, err := ask(cfg, control.CmdBackup, named, 10*time.Minute)
	switch {
	case err == nil:
		b = resp.Backup
		if dst != "" {
			// The server writes only inside its backup folder; move it here.
			if merr := moveFile(b.File, dst); merr != nil {
				c.bad("backup kept at " + b.File + ": can't move it to " + dst + ": " + merr.Error())
				return 1
			}
			b.File = dst
		}
	case errors.Is(err, control.ErrNotRunning):
		b, err = offlineBackup(cfg, dst, time.Now())
	}
	if err != nil {
		c.bad("backup: " + err.Error())
		return 1
	}
	c.ok(filepath.Base(b.File), size(b.Bytes)+" · "+filepath.Dir(b.File))
	fmt.Fprintf(stdout, "    %s\n", strings.Join(b.Parts, ", "))
	return 0
}

// offlineBackup packs the files directly while no server runs.
func offlineBackup(cfg *config.Config, dst string, now time.Time) (*control.Backup, error) {
	dated := dst == ""
	if dated {
		if err := os.MkdirAll(cfg.Backup.Dir, 0o700); err != nil {
			return nil, err
		}
		dst = control.FreeBackupName(cfg.Backup.Dir, now)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("%s exists already", dst)
	}
	files := []control.File{
		{Name: "beeircd.conf", Path: cfg.Path, Part: "config"},
		{Name: "services.db", Path: cfg.Services.Database, Part: "services database"},
		{Name: "services.db-wal", Path: cfg.Services.Database + "-wal", Part: "services database"},
		{Name: "history.db", Path: cfg.History.File, Part: "channel history"},
	}
	if cfg.TLS.Cert != "" {
		files = append(files,
			control.File{Name: "tls/" + filepath.Base(cfg.TLS.Cert), Path: cfg.TLS.Cert, Part: "certificate"},
			control.File{Name: "tls/" + filepath.Base(cfg.TLS.Key), Path: cfg.TLS.Key, Part: "certificate"})
	}
	n, parts, err := control.WriteArchive(dst, files)
	if err != nil {
		return nil, err
	}
	if dated {
		control.Prune(cfg.Backup.Dir, cfg.Backup.Keep)
	}
	return &control.Backup{File: dst, Bytes: n, Parts: parts}, nil
}

// ---- rehash ----

func cmdRehash(args []string, stdout, stderr io.Writer) int {
	path, _, ok := adminFlags("rehash", args, stderr)
	if !ok {
		return 2
	}
	c := &checker{w: stdout, p: colourFor(stdout)}
	cfg, err := config.Load(path)
	if err != nil {
		c.bad("config: " + err.Error())
		c.bad("not re-read: the running server keeps its settings")
		return 1
	}
	resp, err := ask(cfg, control.CmdRehash, "", 30*time.Second)
	if err != nil {
		c.bad(err.Error())
		return 1
	}
	r := resp.Rehash
	c.ok("beeircd.conf read again")
	if len(r.Applied) == 0 && len(r.NeedsRestart) == 0 {
		fmt.Fprintln(stdout, "    nothing changed")
	}
	if len(r.Applied) > 0 {
		fmt.Fprintln(stdout, "    applied:  "+strings.Join(r.Applied, " · "))
	}
	if len(r.NeedsRestart) > 0 {
		fmt.Fprintln(stdout, "    "+c.p.warn("waits for a restart:  "+strings.Join(r.NeedsRestart, " · ")))
	}
	return 0
}

// ---- start, stop, restart (systemd) ----

func systemctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// systemdHere is haveSystemd, swapped out by tests.
var systemdHere = haveSystemd

func haveSystemd() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func cmdService(verb string, args []string, stdout, stderr io.Writer) int {
	path, _, ok := adminFlags(verb, args, stderr)
	if !ok {
		return 2
	}
	c := &checker{w: stdout, p: colourFor(stdout)}
	if inContainer {
		c.bad("in Docker, use: docker compose " + verb + " beeircd")
		return 1
	}
	if !haveSystemd() {
		c.bad("systemd isn't on this machine: run `beeircd run -config " + path + "` under your own service manager")
		return 1
	}
	if verb == "restart" || verb == "start" {
		probe := &checker{w: io.Discard}
		if cfg := runChecks(probe, path, time.Now()); probe.fail || cfg == nil {
			runChecks(c, path, time.Now()) // show what's wrong
			if verb == "restart" {
				c.bad("not restarted: the running server keeps going. Fix the lines above first.")
			} else {
				c.bad("not started. Fix the lines above first.")
			}
			return 1
		}
	}
	if out, err := systemctl(verb, serviceName); err != nil {
		c.bad("systemctl " + verb + " " + serviceName + ": " + or(out, err.Error()))
		return 1
	}
	if verb == "stop" {
		c.ok("BeeIRCd stopped")
		return 0
	}
	cfg, _ := config.Load(path)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := ask(cfg, control.CmdStatus, "", 2*time.Second); err == nil && resp.Status != nil {
			c.ok(fmt.Sprintf("BeeIRCd is running (pid %d)", resp.Status.PID))
			return 0
		}
		time.Sleep(300 * time.Millisecond)
	}
	c.bad("BeeIRCd didn't answer within 20 seconds; see beeircd status")
	return 1
}

// backupTimerOff reports a backup timer that exists but isn't enabled.
func backupTimerOff() bool {
	if !haveSystemd() {
		return false
	}
	out, _ := systemctl("is-enabled", serviceName+"-backup.timer")
	return out == "disabled"
}

// systemdStopped says since when the service is down and why, if systemd
// knows.
func systemdStopped() (since, why string) {
	if !haveSystemd() {
		return "", ""
	}
	out, err := systemctl("show", serviceName, "-p", "InactiveEnterTimestamp", "-p", "ExecMainStatus", "-p", "Result")
	if err != nil {
		return "", ""
	}
	v := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		k, val, _ := strings.Cut(l, "=")
		v[k] = val
	}
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", v["InactiveEnterTimestamp"])
	if err != nil {
		return "", ""
	}
	since = t.Format("15:04") + " " + dayWord(t, time.Now())
	switch {
	case v["Result"] == "success":
		why = "it was stopped"
	case v["ExecMainStatus"] != "" && v["ExecMainStatus"] != "0":
		why = "it exited with code " + v["ExecMainStatus"]
	default:
		why = "it stopped: " + v["Result"]
	}
	return since, why
}

// lastLogLines are the service's last log lines, "15:04:05  text".
func lastLogLines(n int) []string {
	if _, err := exec.LookPath("journalctl"); err != nil {
		return nil
	}
	out, err := exec.Command("journalctl", "-u", serviceName, "-n", strconv.Itoa(n), "--no-pager", "-o", "cat").Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" {
			continue
		}
		// The server logs "2006/01/02 15:04:05 text".
		if len(l) > 20 && l[4] == '/' && l[10] == ' ' {
			l = l[11:19] + "  " + l[20:]
		}
		lines = append(lines, l)
	}
	return lines
}

// ---- words ----

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return strconv.Itoa(n) + " times"
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return addr
}

// since writes a duration the way people say it: 3 days 4 h, 2 h 5 min.
func since(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	h := int(d/time.Hour) % 24
	m := int(d/time.Minute) % 60
	switch {
	case days >= 7:
		return plural(days, "day")
	case days > 0 && h > 0:
		return fmt.Sprintf("%s %d h", plural(days, "day"), h)
	case days > 0:
		return plural(days, "day")
	case h > 0 && m > 0:
		return fmt.Sprintf("%d h %d min", h, m)
	case h > 0:
		return fmt.Sprintf("%d h", h)
	}
	if m == 0 {
		return "under a minute"
	}
	return fmt.Sprintf("%d min", m)
}

func dayWord(t, now time.Time) string {
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today"
	case now.Sub(t) < 48*time.Hour && d2-d1 == 1:
		return "yesterday"
	}
	return t.Format("2 Jan")
}

// ago writes a backup time: today 04:00, yesterday 04:00, 3 days ago.
func ago(t, now time.Time) string {
	w := dayWord(t, now)
	if w == "today" || w == "yesterday" {
		return w + " " + t.Format("15:04")
	}
	return plural(int(now.Sub(t)/(24*time.Hour)), "day") + " ago"
}

func ms(v float64) string {
	switch {
	case v < 0.05:
		return "0.1 ms" // the smallest step shown; "0.0 ms" reads like broken
	case v < 1:
		return fmt.Sprintf("%.1f ms", v)
	case v < 1000:
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f s", v/1000)
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// acmeVia names the acme_http address when it isn't plain port 80.
func acmeVia(addr string) string {
	if addr == "" || addr == ":80" {
		return ""
	}
	return " on " + addr
}

// shortDate turns a release date "2026-11-20" into "20 Nov".
func shortDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("2 Jan")
}

// moveFile moves src to dst (a new file, 0600), copying when they are on
// different file systems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	in.Close()
	return os.Remove(src)
}
