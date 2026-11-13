package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/server"
)

var secretShape = regexp.MustCompile(`^[A-Za-z0-9]{4}(-[A-Za-z0-9]{4}){4}$`)

func TestWebircAdd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beeircd.conf")
	os.WriteFile(path, []byte(strings.TrimSuffix(testConf, "\n")), 0o640) // no newline at the end
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
	defer func() { cancel(); <-done }()
	time.Sleep(100 * time.Millisecond)

	code, out := runCmd("webirc", "add", "-config", path, "beeroom", "127.0.0.1")
	if code != 0 || !strings.Contains(out, "[[webirc]] beeroom added") || !strings.Contains(out, "rehashed: people coming through beeroom") {
		t.Fatalf("add (%d):\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	secret := strings.TrimSpace(lines[len(lines)-1])
	if !secretShape.MatchString(secret) {
		t.Errorf("password %q is not 5 groups of 4", secret)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("config after add: %v", err)
	}
	if len(got.WebIRC) != 1 || got.WebIRC[0].Name != "beeroom" || got.WebIRC[0].Password != secret || got.WebIRC[0].Hosts[0] != "127.0.0.1" {
		t.Errorf("[[webirc]] = %+v", got.WebIRC)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640 kept", fi.Mode().Perm())
	}
	// The running server took it already: a second rehash finds nothing new.
	if code, out = runCmd("rehash", "-config", path); code != 0 || !strings.Contains(out, "nothing changed") {
		t.Errorf("rehash after add (%d):\n%s", code, out)
	}

	before, _ := os.ReadFile(path)
	if code, out = runCmd("webirc", "add", "-config", path, "BeeRoom", "10.0.0.5"); code != 1 || !strings.Contains(out, "already has a [[webirc]]") {
		t.Errorf("same name again (%d):\n%s", code, out)
	}
	if code, out = runCmd("webirc", "add", "-config", path, "kiwi", "chat.example.net"); code != 1 || !strings.Contains(out, "not added") || !strings.Contains(out, "not a name") {
		t.Errorf("host name instead of IP (%d):\n%s", code, out)
	}
	if code, out = runCmd("webirc", "add", "-config", path, "bad name", "10.0.0.5"); code != 1 || !strings.Contains(out, "may not have spaces") {
		t.Errorf("name with a space (%d):\n%s", code, out)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("a refused add changed beeircd.conf")
	}
	if _, err := os.Stat(filepath.Join(dir, ".beeircd.conf.webirc")); !os.IsNotExist(err) {
		t.Error("the temporary file was left behind")
	}

	code, out = runCmd("webirc", "add", "-q", "-config", path, "kiwi", "10.0.0.5, 10.0.1.0/24")
	if code != 0 || !secretShape.MatchString(strings.TrimSpace(out)) {
		t.Errorf("-q should print only the password (%d):\n%q", code, out)
	}
	if got, _ := config.Load(path); len(got.WebIRC) != 2 || strings.Join(got.WebIRC[1].Hosts, " ") != "10.0.0.5 10.0.1.0/24" {
		t.Errorf("second gateway: %+v", got.WebIRC)
	}

	if code, out = runCmd("webirc"); code != 2 || !strings.Contains(out, "Usage: beeircd webirc add") {
		t.Errorf("no subcommand (%d):\n%s", code, out)
	}
}

func TestWebircAddStopped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beeircd.conf")
	os.WriteFile(path, []byte(testConf), 0o600)
	code, out := runCmd("webirc", "add", "-config", path, "beeroom", "127.0.0.1")
	if code != 0 || !strings.Contains(out, "isn't running: it uses the gateway when it starts") {
		t.Errorf("add while stopped (%d):\n%s", code, out)
	}
}
