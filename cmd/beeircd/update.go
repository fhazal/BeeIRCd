package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/release"
	"github.com/fhazal/beeircd/internal/version"
)

// updater carries out `beeircd update`. The
// fields that touch the machine are replaceable so tests run it end to end
// in a temp folder.
type updater struct {
	out      io.Writer
	c        *checker
	in       *bufio.Reader
	tty      bool
	cfgPath  string
	cfg      *config.Config
	exe      string // the installed program
	current  string // its version
	client   *http.Client
	asset    string
	run      func(name string, args ...string) (string, error)
	ask      func(cmd, path string, wait time.Duration) (*control.Response, error)
	isRoot   func() bool
	rename   func(from, to string) error
	waitUp   time.Duration
	pollStep time.Duration
}

func cmdUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultConfig, "config file")
	check := fs.Bool("check", false, "only look for a newer release")
	yes := fs.Bool("yes", false, "don't ask before updating (for scripts)")
	rc := fs.Bool("rc", false, "include test versions (like 1.1.0-rc1)")
	back := fs.Bool("rollback", false, "go back to the version kept before the last update")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if inContainer && !*check {
		// The image is the program: a new one comes from Docker.
		fmt.Fprintln(stdout, "  In Docker, update by pulling the new image (your /data stays):\n    "+dockerUpdate)
		return 1
	}
	u, err := newUpdater(*path, stdout)
	if err != nil {
		u.c.bad(err.Error())
		return 1
	}
	if *back {
		return u.rollback(*yes)
	}
	return u.update(*check, *yes, *rc)
}

func newUpdater(path string, stdout io.Writer) (*updater, error) {
	u := &updater{
		out: stdout, c: &checker{w: stdout, p: colourFor(stdout)}, in: bufio.NewReader(os.Stdin),
		tty: term.IsTerminal(int(os.Stdin.Fd())), cfgPath: path, current: version.Version,
		client: &http.Client{Timeout: 2 * time.Minute}, asset: release.AssetName(runtime.GOOS, runtime.GOARCH),
		run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		isRoot: func() bool { return os.Geteuid() == 0 }, rename: os.Rename, waitUp: 30 * time.Second, pollStep: 300 * time.Millisecond,
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return u, fmt.Errorf("can't find this program's file: %w", err)
	}
	u.exe = exe
	cfg, err := config.Load(path)
	if err != nil {
		return u, fmt.Errorf("config: %w", err)
	}
	u.cfg = cfg
	u.ask = func(cmd, p string, wait time.Duration) (*control.Response, error) {
		return control.Ask(cfg.Control.Socket, control.Request{Cmd: cmd, Path: p}, wait)
	}
	return u, nil
}

// baseURL is where to look: [update] url, or for --rc the newest release
// of any kind.
func (u *updater) baseURL(ctx context.Context, rc bool) (string, error) {
	if !rc {
		return or(u.cfg.Update.URL, release.DefaultURL), nil
	}
	if u.cfg.Update.RCURL != "" {
		return u.cfg.Update.RCURL, nil
	}
	if u.cfg.Update.URL != "" && u.cfg.Update.URL != release.DefaultURL {
		return "", errors.New("--rc needs [update] rc_url when [update] url is your own")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/fhazal/BeeIRCd/releases?per_page=10", nil)
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var list []struct {
		Tag   string `json:"tag_name"`
		Draft bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return "", fmt.Errorf("release list: %w", err)
	}
	for _, r := range list {
		if !r.Draft {
			return "https://github.com/fhazal/BeeIRCd/releases/download/" + r.Tag, nil
		}
	}
	return "", errors.New("no releases found")
}

func (u *updater) update(checkOnly, yes, rc bool) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	base, err := u.baseURL(ctx, rc)
	if err != nil {
		u.c.bad("can't look for releases: " + err.Error())
		return 1
	}
	m, err := release.Fetch(ctx, u.client, base)
	if err != nil {
		u.c.bad(err.Error())
		fmt.Fprintf(u.out, "  Nothing changed. BeeIRCd %s keeps running.\n", u.current)
		return 1
	}
	if !rc && release.TestVersion(m.Version) {
		u.c.bad(fmt.Sprintf("%s is a test version and you didn't ask for one (--rc): not installing it", m.Version))
		fmt.Fprintf(u.out, "  Nothing changed. BeeIRCd %s keeps running.\n", u.current)
		return 1
	}
	if !release.Newer(m.Version, u.current) {
		u.c.ok(fmt.Sprintf("BeeIRCd %s is the newest release", u.current))
		return 0
	}
	fmt.Fprintf(u.out, "\n  %s   released %s, signed by the BeeIRCd release key\n", u.c.p.title(fmt.Sprintf("BeeIRCd %s → %s", u.current, m.Version)), longDate(m.Date))
	if m.Notes != "" {
		fmt.Fprintln(u.out, "  What's new")
		for _, l := range strings.Split(m.Notes, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				fmt.Fprintln(u.out, "    "+l)
			}
		}
	}
	if checkOnly {
		fmt.Fprintln(u.out, "\n  Install it with:  beeircd update")
		return 0
	}
	if !u.isRoot() {
		u.c.bad("run it as root (sudo beeircd update): it replaces " + u.exe)
		return 1
	}
	var running *control.Status // nil: not running
	var plan switchPlan
	if st, err := u.ask(control.CmdStatus, "", 5*time.Second); err == nil && st.Status != nil {
		running = st.Status
		plan = planSwitch(running, m.Gate, 0, m.Version)
		fmt.Fprint(u.out, switchNote(plan, running, m.Version))
	} else {
		fmt.Fprintln(u.out, "\n  BeeIRCd isn't running: the program is updated and stays stopped.")
	}
	if !yes {
		if !u.tty {
			u.c.bad("no terminal to ask on: add --yes to update without questions")
			return 1
		}
		if ok, err := u.yesNo("Update now?", true); err != nil || !ok {
			fmt.Fprintln(u.out, "  Nothing changed.")
			return 0
		}
	}
	fmt.Fprintln(u.out)
	return u.install(ctx, base, m, running, plan)
}

// install does the checked swap; the old program and config stay next to
// the new ones until the new one answers.
func (u *updater) install(ctx context.Context, base string, m *release.Manifest, running *control.Status, plan switchPlan) int {
	u.c.ok("signature checked: this is a BeeIRCd release")
	prog, err := release.Download(ctx, u.client, base, m, u.asset)
	if err != nil {
		return u.stop(err.Error())
	}
	u.c.ok(fmt.Sprintf("downloaded %s (%s), checksum matches", u.asset, size(int64(len(prog)))))
	newExe, oldExe := u.exe+".new", u.exe+".old"
	defer os.Remove(newExe)
	if err := os.WriteFile(newExe, prog, 0o755); err != nil {
		return u.stop(err.Error())
	}
	if out, err := u.run(newExe, "version"); err != nil || !strings.Contains(out, m.Version) {
		return u.stop(fmt.Sprintf("the downloaded program doesn't run here (%s)", or(out, fmt.Sprint(err))))
	}
	if running != nil {
		// The program itself says whether it can take people over (the
		// release file said so before the download).
		was := plan.handover
		plan = planSwitch(running, u.newGateProto(newExe), u.newHandoverVersion(newExe), m.Version)
		if was && !plan.handover {
			u.c.warn("this update reconnects everyone once after all (" + plan.why + ")")
		}
	}
	// The new program upgrades a copy of the config and checks it.
	newCfg := u.cfgPath + ".new"
	defer os.Remove(newCfg)
	out, err := u.run(newExe, "upgrade-config", "-in", u.cfgPath, "-out", newCfg)
	if err != nil {
		return u.stop(fmt.Sprintf("%s can't read your config: %s", m.Version, out))
	}
	changes := nonEmptyLines(out)
	if out, err := u.run(newExe, "check", "-config", newCfg); err != nil {
		u.c.bad(fmt.Sprintf("%s refuses your config: %s", m.Version, checkRefusal(out)))
		fmt.Fprintln(u.out, "  Nothing changed. Fix the line, then run beeircd update again.")
		return 1
	}
	if len(changes) == 0 {
		u.c.ok(m.Version + " reads your config cleanly")
	} else {
		u.c.ok(m.Version + " updates your config: " + strings.Join(changes, "; "))
	}
	var bk bytes.Buffer
	if cmdBackup([]string{"-config", u.cfgPath}, &bk, &bk) != 0 {
		return u.stop("backup failed, not updating: " + strings.TrimSpace(bk.String()))
	}
	u.c.ok("backup " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(bk.String()), "✓")))
	// The config as it is now is kept before anything moves, so a failed
	// copy stops the update with nothing changed.
	cfgKept := ""
	if len(changes) > 0 {
		cfgKept = u.cfgPath + ".before-" + m.Version
		if err := copyFile(u.cfgPath, cfgKept); err != nil {
			return u.stop("couldn't keep a copy of your config, not updating: " + err.Error())
		}
	}
	// Swap: program, then config (when it changed). The version kept by
	// the last update waits aside, so a failed update leaves --rollback
	// exactly as it was.
	undo := func(why string, moves []move) int {
		if cfgKept != "" {
			os.Remove(cfgKept) // the config wasn't touched: no copy needed
		}
		return u.undoSwap(why, moves)
	}
	prevOld := oldExe + ".prev"
	os.Remove(prevOld)
	hadOld := false
	if err := u.rename(oldExe, prevOld); err == nil {
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return undo("couldn't move "+oldExe+" aside: "+err.Error(), nil)
	}
	var putPrevBack []move
	if hadOld {
		putPrevBack = []move{{prevOld, oldExe}}
	}
	if err := u.rename(u.exe, oldExe); err != nil {
		return undo("couldn't move "+u.current+" aside: "+err.Error(), putPrevBack)
	}
	putBack := append([]move{{oldExe, u.exe}}, putPrevBack...)
	if err := u.rename(newExe, u.exe); err != nil {
		return undo("couldn't put "+m.Version+" in place: "+err.Error(), putBack)
	}
	if cfgKept != "" {
		if err := u.rename(newCfg, u.cfgPath); err != nil {
			return undo("couldn't put the updated config in place: "+err.Error(), putBack)
		}
	}
	u.c.ok(fmt.Sprintf("program swapped; %s kept as %s", u.current, oldExe))
	if running == nil {
		os.Remove(prevOld)
		fmt.Fprintf(u.out, "\n  BeeIRCd %s is installed. Start it with:  beeircd start\n\n", m.Version)
		return 0
	}
	started := time.Now()
	if ok, handed := u.switchTo(m.Version, running, plan, "Server updating to BeeIRCd "+m.Version+", back in a few seconds", "switched to "+m.Version); ok {
		os.Remove(prevOld)
		if !handed {
			u.c.ok(fmt.Sprintf("restarted · %s answering after %s", m.Version, secs(time.Since(started))))
		}
		u.restartPanel(m.Version)
		fmt.Fprintf(u.out, "\n  BeeIRCd %s is running.   Go back with:  beeircd update --rollback\n\n", m.Version)
		return 0
	}
	// The new one didn't come up: put the old one back.
	u.c.bad(fmt.Sprintf("%s didn't answer within %s", m.Version, secs(u.waitUp)))
	logs, _ := u.run("journalctl", "-u", serviceName, "-n", "5", "--no-pager", "-o", "short")
	left := u.moveBack(append([]move{{u.exe, u.exe + ".failed"}}, putBack...))
	if cfgKept != "" {
		if err := u.rename(cfgKept, u.cfgPath); err != nil {
			u.c.bad("couldn't put your config back: " + err.Error())
			left = append(left, "mv "+cfgKept+" "+u.cfgPath)
		}
	}
	if left != nil {
		fmt.Fprintf(u.out, "  BeeIRCd wasn't restarted, so nothing runs on half put back files.\n  Do these by hand, then run:  systemctl restart %s\n", serviceName)
		for _, l := range left {
			fmt.Fprintln(u.out, "    "+l)
		}
		return 1
	}
	u.run("systemctl", "restart", serviceName)
	if u.waitFor(u.current) {
		st, _ := u.ask(control.CmdStatus, "", 5*time.Second)
		pid := 0
		if st != nil && st.Status != nil {
			pid = st.Status.PID
		}
		u.c.ok(fmt.Sprintf("rolled back: %s is running again (pid %d)", u.current, pid))
	} else {
		u.c.bad("rolled back to " + u.current + ", but it doesn't answer either: see beeircd status")
	}
	if logs != "" {
		fmt.Fprintln(u.out, "  Its last log lines:")
		for _, l := range nonEmptyLines(logs) {
			fmt.Fprintln(u.out, "    "+u.c.p.dim(l))
		}
	}
	fmt.Fprintf(u.out, "  Please report this with:  journalctl -u %s --since \"10 min ago\"\n\n", serviceName)
	return 1
}

// waitFor waits until the server answers with version v.
func (u *updater) waitFor(v string) bool {
	deadline := time.Now().Add(u.waitUp)
	for time.Now().Before(deadline) {
		if st, err := u.ask(control.CmdStatus, "", 2*time.Second); err == nil && st.Status != nil && st.Status.Version == v {
			return true
		}
		time.Sleep(u.pollStep)
	}
	return false
}

func (u *updater) stop(why string) int {
	u.c.bad(why)
	fmt.Fprintf(u.out, "  Nothing changed. BeeIRCd %s keeps running.\n", u.current)
	return 1
}

// move is one rename that undoes a step of an update.
type move struct{ from, to string }

// moveBack does the moves in order. It stops at the first one that fails,
// since going on could put one program over another, and returns the
// moves left as lines to type by hand.
func (u *updater) moveBack(moves []move) []string {
	for i, mv := range moves {
		if err := u.rename(mv.from, mv.to); err != nil {
			u.c.bad(fmt.Sprintf("couldn't move %s back to %s: %v", mv.from, mv.to, err))
			var left []string
			for _, l := range moves[i:] {
				left = append(left, "mv "+l.from+" "+l.to)
			}
			return left
		}
	}
	return nil
}

// undoSwap puts the program files back after a swap failed half way;
// the server hasn't been touched yet.
func (u *updater) undoSwap(why string, moves []move) int {
	u.c.bad(why)
	if left := u.moveBack(moves); left != nil {
		fmt.Fprintln(u.out, "  The server wasn't touched, but the program files are half swapped.\n  Put them back by hand before BeeIRCd restarts:")
		for _, l := range left {
			fmt.Fprintln(u.out, "    "+l)
		}
		return 1
	}
	fmt.Fprintf(u.out, "  Nothing changed. BeeIRCd %s keeps running.\n", u.current)
	return 1
}

// rollback swaps in the program kept by the last update.
func (u *updater) rollback(yes bool) int {
	oldExe := u.exe + ".old"
	if _, err := os.Stat(oldExe); err != nil {
		u.c.bad("there is no earlier version kept (" + oldExe + ")")
		return 1
	}
	if !u.isRoot() {
		u.c.bad("run it as root (sudo beeircd update --rollback)")
		return 1
	}
	out, _ := u.run(oldExe, "version")
	oldVer := "the earlier version"
	if f := strings.Fields(out); len(f) >= 2 {
		oldVer = f[1] // "BeeIRCd 1.0.0 (abc1234)"
	}
	// The kept program must read today's config (a setting added since it
	// came out stops it), or going back takes the server down.
	if out, err := u.run(oldExe, "check", "-config", u.cfgPath); err != nil {
		u.c.bad(fmt.Sprintf("%s refuses your config: %s", oldVer, checkRefusal(out)))
		fmt.Fprintln(u.out, "  Nothing changed. Fix or remove that line, then run beeircd update --rollback again.")
		return 1
	}
	fi, _ := os.Stat(oldExe)
	var running *control.Status // nil: not running
	var plan switchPlan
	if st, err := u.ask(control.CmdStatus, "", 5*time.Second); err == nil && st.Status != nil {
		running = st.Status
		plan = planSwitch(running, u.newGateProto(oldExe), u.newHandoverVersion(oldExe), oldVer)
	}
	fmt.Fprintf(u.out, "\n  Back from %s to %s (kept since %s).", u.current, oldVer, fi.ModTime().Format("2 Jan"))
	switch {
	case running != nil && plan.handover:
		fmt.Fprint(u.out, " The server switches without disconnecting anyone.")
	case running != nil:
		fmt.Fprintf(u.out, " The server restarts: everyone reconnects once\n  (%s).", plan.why)
	}
	fmt.Fprintln(u.out)
	if !yes {
		if !u.tty {
			u.c.bad("no terminal to ask on: add --yes")
			return 1
		}
		if ok, err := u.yesNo("Go back now?", false); err != nil || !ok {
			fmt.Fprintln(u.out, "  Nothing changed.")
			return 0
		}
	}
	tmp := u.exe + ".swap"
	if err := u.rename(u.exe, tmp); err != nil {
		u.c.bad(err.Error())
		return 1
	}
	if err := u.rename(oldExe, u.exe); err != nil {
		// Stop here: moving on would put the current program over the
		// kept one. Put the current program back first.
		u.c.bad("couldn't put " + oldVer + " in place: " + err.Error())
		if err := u.rename(tmp, u.exe); err != nil {
			u.c.bad("and couldn't put " + u.current + " back: " + err.Error())
			fmt.Fprintf(u.out, "  The server wasn't touched. Move %s back to %s by hand before it restarts.\n", tmp, u.exe)
			return 1
		}
		fmt.Fprintln(u.out, "  Nothing changed.")
		return 1
	}
	// So going forward again is one more --rollback.
	if err := u.rename(tmp, oldExe); err != nil {
		u.c.bad(oldVer + " is in place, but " + u.current + " couldn't be kept as " + oldExe + ": " + err.Error())
		fmt.Fprintf(u.out, "  The server wasn't touched. %s is now at %s.\n", u.current, tmp)
		return 1
	}
	if running == nil {
		u.c.ok("program swapped back to " + oldVer + "; start it with beeircd start")
		return 0
	}
	started := time.Now()
	ok, handed := u.switchTo(oldVer, running, plan, "Server going back to BeeIRCd "+oldVer+", back in a few seconds", "switched to "+oldVer)
	if !ok {
		u.c.bad(oldVer + " didn't answer within " + secs(u.waitUp) + ": see beeircd status")
		return 1
	}
	if !handed {
		u.c.ok(fmt.Sprintf("program swapped back · %s answering after %s", oldVer, secs(time.Since(started))))
	}
	u.restartPanel(oldVer)
	return 0
}

// restartPanel restarts the Hive Panel service when it runs: it is the same
// program file, so without this it keeps serving the version it started with.
func (u *updater) restartPanel(version string) {
	const svc = "beeircd-web.service"
	if out, _ := u.run("systemctl", "is-active", svc); strings.TrimSpace(out) != "active" {
		return
	}
	if out, err := u.run("systemctl", "restart", svc); err != nil {
		u.c.warn("the Hive Panel didn't restart (" + or(out, err.Error()) + "): run  systemctl restart beeircd-web")
		return
	}
	u.c.ok("Hive Panel restarted on " + version)
}

func (u *updater) yesNo(q string, def bool) (bool, error) {
	in := &installer{in: u.in, out: u.out, tty: u.tty}
	return in.yesNo(q, def)
}

// cmdUpgradeConfig is run by the NEW program during an update: it writes
// the upgraded config to -out and prints one line per change.
func cmdUpgradeConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("upgrade-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", defaultConfig, "config to read")
	out := fs.String("out", "", "where to write the upgraded copy")
	if err := fs.Parse(args); err != nil || *out == "" {
		return 2
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	text, changes := config.Upgrade(string(b))
	fi, _ := os.Stat(*in)
	mode := os.FileMode(0o640)
	if fi != nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(*out, []byte(text), mode); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if fi != nil {
		copyOwner(fi, *out)
	}
	for _, c := range changes {
		fmt.Fprintln(stdout, c)
	}
	return 0
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// checkRefusal is why `beeircd check` said no: its ✗ lines, or all of it.
func checkRefusal(out string) string {
	var why []string
	for _, l := range nonEmptyLines(out) {
		if strings.Contains(l, "✗") {
			why = append(why, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "✗")))
		}
	}
	return or(strings.Join(why, "; "), out)
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	fi, _ := os.Stat(from)
	if err := os.WriteFile(to, b, fi.Mode().Perm()); err != nil {
		return err
	}
	copyOwner(fi, to)
	return nil
}

func secs(d time.Duration) string {
	n := int(d.Round(time.Second) / time.Second)
	if n <= 1 {
		return "1 s"
	}
	return fmt.Sprintf("%d s", n)
}

// longDate turns "2026-11-20" into "20 Nov 2026".
func longDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("2 Jan 2006")
}

func people(n int) string {
	if n == 1 {
		return "the 1 person"
	}
	return fmt.Sprintf("the %d people", n)
}
