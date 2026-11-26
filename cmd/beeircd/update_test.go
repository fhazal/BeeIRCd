package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/release"
)

// fakeSystem plays systemd and the running server: a "program" is a file
// holding "PROGRAM <version>", and a restart runs whatever is installed.
type fakeSystem struct {
	running    string // version answering on the socket ("" = stopped)
	comesUp    bool   // whether a new version answers after a restart
	checkFails bool
	panel      bool // the Hive Panel service (beeircd-web) is running
	quit       string
	calls      []string

	// The door keeper: gate is what the running server's status says
	// ("" = a server from before the hand-over), progGate what
	// `beeircd gate-proto` prints for any program ("" = unknown command).
	gate, progGate string
	progSnap       string // what `beeircd handover-version` prints ("" = unknown command)
	handoverErr    bool   // the running server refuses the hand-over
	handoverWhy    string // the new server took nobody over, this is why
	pid            int    // the running server process
	took           *control.Handover
}

func progVersion(file string) string {
	b, _ := os.ReadFile(file)
	return strings.TrimPrefix(string(b), "PROGRAM ")
}

// releaseServer serves a signed release of version v (signed with sign).
// gate is the release file's door keeper protocol (none: an old release).
func releaseServer(t *testing.T, v string, sign ed25519.PrivateKey, gate ...string) *httptest.Server {
	t.Helper()
	prog := []byte("PROGRAM " + v)
	m := release.Manifest{Version: v, Date: "2026-11-20", Notes: "· Fixes a crash\n· Faster joins",
		Files: map[string]string{"beeircd-linux-amd64": release.Sum(prog)}}
	if len(gate) > 0 {
		m.Gate = gate[0]
	}
	man, _ := json.Marshal(m)
	files := map[string][]byte{"/release.json": man, "/release.json.sig": release.Sign(man, sign), "/beeircd-linux-amd64": prog}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testUpdater(t *testing.T, current, url string, fs *fakeSystem, answers string) (*updater, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "opt", "beeircd")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, []byte("PROGRAM "+current), 0o755)
	cfgPath := filepath.Join(dir, "beeircd.conf")
	conf := strings.Replace(goodConf, "URL", url, 1)
	os.WriteFile(cfgPath, []byte(conf), 0o640)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	u := &updater{
		out: out, c: &checker{w: out}, in: bufio.NewReader(strings.NewReader(answers)), tty: true,
		cfgPath: cfgPath, cfg: cfg, exe: exe, current: current, client: http.DefaultClient,
		asset: "beeircd-linux-amd64", isRoot: func() bool { return true }, rename: os.Rename, waitUp: 300 * time.Millisecond, pollStep: 10 * time.Millisecond,
	}
	u.run = func(name string, args ...string) (string, error) {
		fs.calls = append(fs.calls, filepath.Base(name)+" "+strings.Join(args, " "))
		switch {
		case len(args) == 1 && args[0] == "version":
			return "BeeIRCd " + progVersion(name) + " (test)", nil
		case len(args) > 0 && args[0] == "upgrade-config":
			var o bytes.Buffer
			if cmdUpgradeConfig(args[1:], &o, &o) != 0 {
				return o.String(), errors.New("exit 1")
			}
			return o.String(), nil
		case len(args) == 1 && args[0] == "handover-version":
			if fs.progSnap == "" {
				return "Unknown command \"handover-version\".", errors.New("exit 2")
			}
			return fs.progSnap + "\n", nil
		case len(args) == 1 && args[0] == "gate-proto":
			if fs.progGate == "" {
				return "Unknown command \"gate-proto\".", errors.New("exit 2")
			}
			return fs.progGate + "\n", nil
		case len(args) > 0 && args[0] == "check":
			if fs.checkFails {
				return "  ✓ config reads cleanly\n  ✗ [limits] flood_rate \"fast\" is not a number", errors.New("exit 1")
			}
			return "  ✓ safe to restart", nil
		case name == "systemctl" && len(args) == 2 && args[1] == "beeircd-web.service":
			if args[0] == "is-active" && !fs.panel {
				return "inactive", errors.New("exit 3")
			}
			if args[0] == "is-active" {
				return "active", nil
			}
			return "", nil
		case name == "systemctl":
			if v := progVersion(exe); fs.comesUp || v == current {
				fs.running = v
			} else {
				fs.running = ""
			}
			fs.pid++
			fs.took = nil
			return "", nil
		case name == "journalctl":
			return "Nov 20 14:12:31 beeircd[5201]: listen on :6697: address already in use", nil
		}
		return "", nil
	}
	u.ask = func(cmd, p string, wait time.Duration) (*control.Response, error) {
		switch cmd {
		case control.CmdQuitReason:
			fs.quit = p
			return &control.Response{}, nil
		case control.CmdStatus:
			if fs.running == "" {
				return nil, control.ErrNotRunning
			}
			return &control.Response{Status: &control.Status{Version: fs.running, Online: 48, Local: 47, PID: 4242 + fs.pid,
				Gate: fs.gate, Handover: fs.took}}, nil
		case control.CmdHandover:
			fs.calls = append(fs.calls, "handover")
			if fs.handoverErr {
				return nil, errors.New("a hand-over is already running")
			}
			// The door keeper starts the installed program.
			fs.running, fs.pid = progVersion(exe), fs.pid+1
			fs.took = &control.Handover{Kept: 46, Of: 47}
			if fs.handoverWhy != "" {
				fs.took = &control.Handover{Problem: fs.handoverWhy}
			}
			return &control.Response{}, nil
		}
		return nil, control.ErrNotRunning
	}
	return u, out
}

const goodConf = `
[server]
name = "irc.beenet.org"
network = "BeeNet"
sid = "0BE"

[[listen]]
address = ":6667"

[cloak]
secret = "0123456789abcdef-test"

[services]
database = "services.db"

[update]
url = "URL"
`

func signingKey(t *testing.T) ed25519.PrivateKey {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := release.TestKey
	release.TestKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { release.TestKey = old })
	return priv
}

func TestUpdateHappyPath(t *testing.T) {
	key := signingKey(t)
	srv := releaseServer(t, "1.0.1", key)
	fs := &fakeSystem{running: "1.0.0", comesUp: true}
	u, out := testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	text := out.String()
	for _, want := range []string{"BeeIRCd 1.0.0 → 1.0.1", "released 20 Nov 2026", "Fixes a crash", "the 47 people online are disconnected",
		"signature checked", "checksum matches", "1.0.1 reads your config cleanly", "✓ backup beeircd-", "1.0.0 kept as",
		"restarted · 1.0.1 answering", "--rollback"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if progVersion(u.exe) != "1.0.1" || progVersion(u.exe+".old") != "1.0.0" {
		t.Errorf("program %q, old %q", progVersion(u.exe), progVersion(u.exe+".old"))
	}
	if fs.quit != "Server updating to BeeIRCd 1.0.1, back in a few seconds" {
		t.Errorf("quit message %q", fs.quit)
	}
	if _, err := os.Stat(u.exe + ".new"); err == nil {
		t.Error(".new left behind")
	}

	// --rollback goes back, and once more goes forward again.
	u.current, u.in = "1.0.1", bufio.NewReader(strings.NewReader("y\n"))
	if code := u.rollback(false); code != 0 || progVersion(u.exe) != "1.0.0" || fs.running != "1.0.0" {
		t.Fatalf("rollback exit %d, program %s, running %s\n%s", code, progVersion(u.exe), fs.running, out)
	}
	if progVersion(u.exe+".old") != "1.0.1" {
		t.Error("the version rolled back from isn't kept")
	}
}

// --rollback asks the kept program about the config first: one that
// refuses it (a setting it doesn't know yet) must not replace the running one.
func TestRollbackRefusesConfigOldProgramRejects(t *testing.T) {
	fs := &fakeSystem{running: "1.0.1", comesUp: true, checkFails: true}
	u, out := testUpdater(t, "1.0.1", "http://127.0.0.1:1", fs, "y\n")
	os.WriteFile(u.exe+".old", []byte("PROGRAM 1.0.0"), 0o755)
	if code := u.rollback(true); code != 1 {
		t.Fatalf("rollback exit %d, want 1\n%s", code, out)
	}
	if progVersion(u.exe) != "1.0.1" || progVersion(u.exe+".old") != "1.0.0" || fs.running != "1.0.1" {
		t.Errorf("program %s, old %s, running %s: rollback changed something\n%s", progVersion(u.exe), progVersion(u.exe+".old"), fs.running, out)
	}
	for _, want := range []string{"1.0.0 refuses your config", "flood_rate", "Nothing changed."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A rename that fails half way through --rollback must stop there: going
// on would put the current program over the kept one and lose it.
func TestRollbackStopsWhenRenameFails(t *testing.T) {
	fs := &fakeSystem{running: "1.0.1", comesUp: true}
	u, out := testUpdater(t, "1.0.1", "http://127.0.0.1:1", fs, "y\n")
	os.WriteFile(u.exe+".old", []byte("PROGRAM 1.0.0"), 0o755)
	u.rename = func(from, to string) error {
		if from == u.exe+".old" {
			return errors.New("disk said no")
		}
		return os.Rename(from, to)
	}
	if code := u.rollback(true); code != 1 {
		t.Fatalf("rollback exit %d, want 1\n%s", code, out)
	}
	if progVersion(u.exe) != "1.0.1" || progVersion(u.exe+".old") != "1.0.0" || fs.running != "1.0.1" {
		t.Errorf("program %q, old %q, running %q: want both kept and nothing restarted\n%s", progVersion(u.exe), progVersion(u.exe+".old"), fs.running, out)
	}
	if _, err := os.Stat(u.exe + ".swap"); err == nil {
		t.Error(".swap left behind")
	}
	for _, want := range []string{"couldn't put 1.0.0 in place: disk said no", "Nothing changed."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestUpdateRollsBackWhenNewOneFails(t *testing.T) {
	key := signingKey(t)
	srv := releaseServer(t, "1.0.1", key)
	fs := &fakeSystem{running: "1.0.0", comesUp: false}
	u, out := testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	// An earlier update kept 0.9.0; the failed one must not lose it.
	os.WriteFile(u.exe+".old", []byte("PROGRAM 0.9.0"), 0o755)
	if code := u.update(false, false, false); code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	text := out.String()
	for _, want := range []string{"1.0.1 didn't answer within", "rolled back: 1.0.0 is running again (pid 4244)", "address already in use", "journalctl -u beeircd"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if progVersion(u.exe) != "1.0.0" || fs.running != "1.0.0" {
		t.Errorf("after rollback: program %s, running %s", progVersion(u.exe), fs.running)
	}
	if v := progVersion(u.exe + ".old"); v != "0.9.0" {
		t.Errorf("--rollback target after a failed update = %q, want 0.9.0 (kept from before)", v)
	}
	if _, err := os.Stat(u.exe + ".old.prev"); err == nil {
		t.Error(".old.prev left behind")
	}
}

// When the new version changes the config, the current config is kept
// first; if that copy can't be made, nothing may change.
func TestUpdateStopsWhenConfigCopyFails(t *testing.T) {
	key := signingKey(t)
	srv := releaseServer(t, "1.0.1", key)
	fs := &fakeSystem{running: "1.0.0", comesUp: true}
	u, out := testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	run := u.run
	u.run = func(name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "upgrade-config" {
			// A migration that changes something.
			b, _ := os.ReadFile(args[2])
			os.WriteFile(args[4], append(b, "\n# upgraded\n"...), 0o640)
			return "renamed a setting", nil
		}
		return run(name, args...)
	}
	confBefore, _ := os.ReadFile(u.cfgPath)
	// A folder where the copy goes makes the copy fail.
	os.Mkdir(u.cfgPath+".before-1.0.1", 0o755)
	if code := u.update(false, true, false); code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if progVersion(u.exe) != "1.0.0" || fs.running != "1.0.0" {
		t.Errorf("program %s, running %s: want nothing changed\n%s", progVersion(u.exe), fs.running, out)
	}
	if _, err := os.Stat(u.exe + ".old"); err == nil {
		t.Error("program was swapped")
	}
	if conf, _ := os.ReadFile(u.cfgPath); string(conf) != string(confBefore) {
		t.Errorf("config changed:\n%s", conf)
	}
	for _, want := range []string{"couldn't keep a copy of your config", "Nothing changed."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestUpdateRefusals(t *testing.T) {
	key := signingKey(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)

	// Signed with a key BeeIRCd doesn't trust: nothing happens.
	fake := releaseServer(t, "1.0.1", other)
	fs := &fakeSystem{running: "1.0.0"}
	u, out := testUpdater(t, "1.0.0", fake.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 1 || !strings.Contains(out.String(), "doesn't match BeeIRCd's key") || progVersion(u.exe) != "1.0.0" {
		t.Errorf("bad signature: exit %d, program %s\n%s", code, progVersion(u.exe), out)
	}

	// The new version refuses the config: nothing changes.
	srv := releaseServer(t, "1.0.1", key)
	fs = &fakeSystem{running: "1.0.0", checkFails: true}
	u, out = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 1 || !strings.Contains(out.String(), `1.0.1 refuses your config: [limits] flood_rate "fast" is not a number`) || progVersion(u.exe) != "1.0.0" {
		t.Errorf("config refused: exit %d\n%s", code, out)
	}
	for _, c := range fs.calls {
		if strings.HasPrefix(c, "systemctl") {
			t.Errorf("restarted although nothing changed: %v", fs.calls)
		}
	}

	// Already the newest; --check only looks; answering n changes nothing.
	u, out = testUpdater(t, "1.0.1", srv.URL, &fakeSystem{running: "1.0.1"}, "")
	if u.update(false, false, false); !strings.Contains(out.String(), "1.0.1 is the newest release") {
		t.Errorf("newest:\n%s", out)
	}
	u, out = testUpdater(t, "1.0.0", srv.URL, &fakeSystem{running: "1.0.0"}, "")
	if u.update(true, false, false); !strings.Contains(out.String(), "Install it with:  beeircd update") || progVersion(u.exe) != "1.0.0" {
		t.Errorf("--check:\n%s", out)
	}
	u, out = testUpdater(t, "1.0.0", srv.URL, &fakeSystem{running: "1.0.0"}, "n\n")
	if u.update(false, false, false); !strings.Contains(out.String(), "Nothing changed") || progVersion(u.exe) != "1.0.0" {
		t.Errorf("answered n:\n%s", out)
	}
	// A signed test version where releases are: only with --rc.
	rcSrv := releaseServer(t, "1.1.0-rc1", key)
	u, out = testUpdater(t, "1.0.0", rcSrv.URL, &fakeSystem{running: "1.0.0"}, "y\n")
	if code := u.update(false, false, false); code != 1 || !strings.Contains(out.String(), "1.1.0-rc1 is a test version") || progVersion(u.exe) != "1.0.0" {
		t.Errorf("test version without --rc: exit %d\n%s", code, out)
	}
	// No terminal and no --yes: refused instead of hanging.
	u, out = testUpdater(t, "1.0.0", srv.URL, &fakeSystem{running: "1.0.0"}, "")
	u.tty = false
	if code := u.update(false, false, false); code != 1 || !strings.Contains(out.String(), "add --yes") {
		t.Errorf("no tty:\n%s", out)
	}
}

func TestUpgradeConfigCopiesUnchanged(t *testing.T) {
	dir := t.TempDir()
	in, outFile := filepath.Join(dir, "a.conf"), filepath.Join(dir, "b.conf")
	os.WriteFile(in, []byte("# comment\n[server]\nname = \"x.y\"\n"), 0o640)
	var o bytes.Buffer
	if cmdUpgradeConfig([]string{"-in", in, "-out", outFile}, &o, &o) != 0 || o.Len() != 0 {
		t.Fatalf("output %q", o.String())
	}
	a, _ := os.ReadFile(in)
	b, _ := os.ReadFile(outFile)
	if !bytes.Equal(a, b) {
		t.Error("config changed with no migrations")
	}
}

// The Hive Panel (beeircd-web) runs the same program file: an update or a
// rollback restarts it too, or it keeps running the old version (found on
// the test pair at 1.0.0). A machine without the panel gets no restart.
func TestUpdateRestartsHivePanel(t *testing.T) {
	key := signingKey(t)
	srv := releaseServer(t, "1.0.1", key)
	restarts := func(fs *fakeSystem) int {
		n := 0
		for _, c := range fs.calls {
			if c == "systemctl restart beeircd-web.service" {
				n++
			}
		}
		return n
	}

	fs := &fakeSystem{running: "1.0.0", comesUp: true, panel: true}
	u, out := testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if restarts(fs) != 1 || !strings.Contains(out.String(), "Hive Panel restarted on 1.0.1") {
		t.Errorf("update: panel restarts %d, calls %v\n%s", restarts(fs), fs.calls, out)
	}
	u.current, u.in = "1.0.1", bufio.NewReader(strings.NewReader("y\n"))
	if code := u.rollback(false); code != 0 || restarts(fs) != 2 {
		t.Errorf("rollback exit %d, panel restarts %d\n%s", code, restarts(fs), out)
	}

	// A failed update goes back to the old program; the panel never left it.
	fs = &fakeSystem{running: "1.0.0", comesUp: false, panel: true}
	u, _ = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	u.update(false, false, false)
	if restarts(fs) != 0 {
		t.Errorf("failed update restarted the panel: %v", fs.calls)
	}

	fs = &fakeSystem{running: "1.0.0", comesUp: true}
	u, out = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 || restarts(fs) != 0 || strings.Contains(out.String(), "Hive Panel") {
		t.Errorf("no panel: exit %d, restarts %d\n%s", code, restarts(fs), out)
	}
}
