package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/server"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

const testConf = `[server]
name = "irc.test.local"
network = "BeeNet"
sid = "0BE"
resolve_hosts = false

[[listen]]
address = "127.0.0.1:0"

[[listen]]
address = "127.0.0.1:0"
tls = true

[tls]
cert = "cert.pem"
key = "key.pem"
self_signed = true

[cloak]
secret = "0123456789abcdef-test"

[services]
database = "services.db"
`

func runCmd(args ...string) (int, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestAdminCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beeircd.conf")
	os.WriteFile(path, []byte(testConf), 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	stopped := false
	stop := func() {
		if !stopped {
			cancel()
			<-done
			stopped = true
		}
	}
	defer stop()

	code, out := runCmd("status", "-config", path)
	if code != 0 || !strings.Contains(out, "● running") || !strings.Contains(out, "Services   NickServ") || strings.Contains(out, "\x1b[") {
		t.Errorf("status (%d):\n%s", code, out)
	}
	if code, out = runCmd("check", "-config", path); code != 0 || !strings.Contains(out, "✓ services database OK") || !strings.Contains(out, "✓ safe to restart") {
		t.Errorf("check (%d):\n%s", code, out)
	}
	if code, out = runCmd("backup", "-config", path); code != 0 || !strings.Contains(out, "✓ beeircd-") || !strings.Contains(out, "services database (live copy)") {
		t.Errorf("backup (%d):\n%s", code, out)
	}
	mine := filepath.Join(t.TempDir(), "mine.tar.gz")
	if code, out = runCmd("backup", "-config", path, mine); code != 0 || !strings.Contains(out, "mine.tar.gz") {
		t.Errorf("named backup (%d):\n%s", code, out)
	}
	if fi, err := os.Stat(mine); err != nil || fi.Size() == 0 {
		t.Errorf("named backup not moved there: %v", err)
	}
	if code, out = runCmd("backup", "-config", path, mine); code != 1 || !strings.Contains(out, "exists already") {
		t.Errorf("overwriting a backup (%d):\n%s", code, out)
	}
	if code, out = runCmd("rehash", "-config", path); code != 0 || !strings.Contains(out, "nothing changed") {
		t.Errorf("rehash (%d):\n%s", code, out)
	}

	stop()
	if code, out = runCmd("status", "-config", path); code != 3 || !strings.Contains(out, "● stopped") {
		t.Errorf("stopped status (%d):\n%s", code, out)
	}
	if code, out = runCmd("check", "-config", path); code != 0 || !strings.Contains(out, "✓ services database OK") {
		t.Errorf("offline check (%d):\n%s", code, out)
	}
	if code, out = runCmd("backup", "-config", path, filepath.Join(dir, "offline.tar.gz")); code != 0 || !strings.Contains(out, "offline.tar.gz") {
		t.Errorf("offline backup (%d):\n%s", code, out)
	}
	if code, out = runCmd("rehash", "-config", path); code != 1 || !strings.Contains(out, "not running") {
		t.Errorf("rehash while stopped (%d):\n%s", code, out)
	}
	os.WriteFile(path, []byte(testConf+"\n[limits]\nbogus = 1\n"), 0o600)
	if code, out = runCmd("check", "-config", path); code != 1 || !strings.Contains(out, "✗ config:") || !strings.Contains(out, "✗ not safe to restart") {
		t.Errorf("broken config check (%d):\n%s", code, out)
	}
}

func TestStatusScreen(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)
	dir := t.TempDir()
	st := &control.Status{
		Version: "1.0.0", Started: now.Add(-76 * time.Hour), PID: 4121, Server: "irc.beenet.org", Network: "BeeNet",
		Ports:  []control.Port{{Address: ":6667"}, {Address: ":6697", TLS: true}},
		Online: 48, Peak: 61, Channels: 12, Opers: 4, Services: []string{"NickServ", "ChanServ"},
		MemoryMB: 38, LagAvgMS: 0.1, Lag99MS: 2, DBBytes: 2200000, HistoryBytes: 1500000, HistoryChannels: 9,
		Cert:      &control.Cert{Source: "own files", NotAfter: now.Add(90 * 24 * time.Hour)},
		BackupDir: dir,
	}
	out := statusScreen(st, paint{}, now, false)
	for _, want := range []string{
		"BeeIRCd 1.0.0   ● running  for 3 days 4 h   pid 4121",
		"Ports      6667 plain · 6697 TLS",
		"People     48 online (peak 61) · 12 channels · 4 opers",
		"Load       38 MB memory · answers in 0.1 ms (99% under 2 ms)",
		"Data       services.db 2.1 MB · history 1.4 MB in 9 channels",
		"Cert       own files, valid until 10 Feb 2027",
		"Backup     none yet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Safety") {
		t.Error("Safety shows with nothing to say")
	}
	st.Safety = control.Safety{Throttled: 214, Zlined: 3}
	st.Cert.NotAfter = now.Add(6 * 24 * time.Hour)
	os.WriteFile(filepath.Join(dir, control.BackupName(now.Add(-3*time.Hour))), []byte("x"), 0o600)
	os.Chtimes(filepath.Join(dir, control.BackupName(now.Add(-3*time.Hour))), now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	out = statusScreen(st, paint{on: true}, now, true)
	for _, want := range []string{"Safety     today 214 connections throttled · 3 IPs Z-lined", "6 days left", "Backup     today 07:00", "\x1b[33m"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWords(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{20 * time.Second, "under a minute"}, {90 * time.Second, "1 min"}, {2*time.Hour + 5*time.Minute, "2 h 5 min"}, {50 * time.Hour, "2 days 2 h"}, {41 * 24 * time.Hour, "41 days"}} {
		if got := since(c.d); got != c.want {
			t.Errorf("since(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	if size(2200000) != "2.1 MB" || size(800) != "800 bytes" || ms(0.06) != "0.1 ms" {
		t.Error("size/ms words")
	}
}

func TestCheckCertForOtherName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beeircd.conf")
	os.WriteFile(path, []byte(testConf), 0o600)
	// A real setup: the certificate is for the DNS name, not the server name.
	if err := tlsutil.MakeSelfSigned(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), "test.beeircd.org"); err != nil {
		t.Fatal(err)
	}
	code, out := runCmd("check", "-config", path)
	if code != 0 || !strings.Contains(out, "! certificate is for test.beeircd.org, not the server name irc.test.local") || !strings.Contains(out, "✓ safe to restart") {
		t.Errorf("check (%d):\n%s", code, out)
	}
}
