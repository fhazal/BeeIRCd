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
	quit       string
	calls      []string
}

func progVersion(file string) string {
	b, _ := os.ReadFile(file)
	return strings.TrimPrefix(string(b), "PROGRAM ")
}

// releaseServer serves a signed release of version v (signed with sign).
func releaseServer(t *testing.T, v string, sign ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	prog := []byte("PROGRAM " + v)
	m := release.Manifest{Version: v, Date: "2026-11-20", Notes: "· Fixes a crash\n· Faster joins",
		Files: map[string]string{"beeircd-linux-amd64": release.Sum(prog)}}
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
	conf := strings.Replace(good7e, "URL", url, 1)
	os.WriteFile(cfgPath, []byte(conf), 0o640)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	u := &updater{
		out: out, c: &checker{w: out}, in: bufio.NewReader(strings.NewReader(answers)), tty: true,
		cfgPath: cfgPath, cfg: cfg, exe: exe, current: current, client: http.DefaultClient,
		asset: "beeircd-linux-amd64", isRoot: func() bool { return true }, waitUp: 300 * time.Millisecond, pollStep: 10 * time.Millisecond,
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
		case len(args) > 0 && args[0] == "check":
			if fs.checkFails {
				return "  ✓ config reads cleanly\n  ✗ [limits] flood_rate \"fast\" is not a number", errors.New("exit 1")
			}
			return "  ✓ safe to restart", nil
		case name == "systemctl":
			if v := progVersion(exe); fs.comesUp || v == current {
				fs.running = v
			} else {
				fs.running = ""
			}
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
			return &control.Response{Status: &control.Status{Version: fs.running, Online: 48, PID: 4242}}, nil
		}
		return nil, control.ErrNotRunning
	}
	return u, out
}

const good7e = `
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
	for _, want := range []string{"BeeIRCd 1.0.0 → 1.0.1", "released 20 Nov 2026", "Fixes a crash", "the 48 people online are disconnected",
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
	for _, want := range []string{"1.0.1 didn't answer within", "rolled back: 1.0.0 is running again (pid 4242)", "address already in use", "journalctl -u beeircd"} {
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
