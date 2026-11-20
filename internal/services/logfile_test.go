package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withLogFile turns the log file on at path, with the clock fixed at now.
func withLogFile(t *testing.T, svc *Services, path string, now time.Time) {
	t.Helper()
	svc.now = func() time.Time { return now }
	svc.cfg.Services.LogFile = path
	svc.SyncLogFile()
	t.Cleanup(svc.CloseLogFile)
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Lines reach the file with no log channel at all, with the time and the
// service in front and the colours taken out.
func TestLogFileWithoutLogChannel(t *testing.T) {
	svc, h := newServices(t)
	if svc.logChannel() != "" {
		t.Fatalf("test needs no log channel, have %q", svc.logChannel())
	}
	path := filepath.Join(t.TempDir(), "services.log")
	now := time.Date(2026, 11, 20, 1, 31, 5, 0, time.Local)
	withLogFile(t, svc, path, now)

	opsv := h.net.UserByNick("OperServ")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	h.opers = map[string]string{boss.UID: "oa"}
	svc.Message(boss.UID, opsv, "RELOAD")
	noticeTo(h, "Boss")
	svc.logTo(svc.global, "\x02bold\x02 and \x0304,01red\x03 text")
	svc.CloseLogFile()

	got := readLog(t, path)
	want := []string{
		"2026-11-20 01:31:05 OperServ: ADMIN: Boss!~boss@leaf.example used RELOAD\n",
		"2026-11-20 01:31:05 Global: bold and red text\n",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("log file lacks %q:\n%s", w, got)
		}
	}
	if len(h.said) != 0 {
		t.Errorf("no log channel, but the bots said %q", h.said)
	}
}

// A rehash to a new path closes the old file and opens the new one; an
// empty path stops writing.
func TestLogFileRehash(t *testing.T) {
	svc, _ := newServices(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	withLogFile(t, svc, a, time.Now())
	svc.logTo(svc.global, "one")
	old := svc.logw
	svc.cfg.Services.LogFile = b
	svc.SyncLogFile()
	if !old.wait(3 * time.Second) {
		t.Fatal("the old writer did not stop")
	}
	svc.logTo(svc.global, "two")
	svc.cfg.Services.LogFile = ""
	cur := svc.logw
	svc.SyncLogFile()
	cur.wait(3 * time.Second)
	if svc.logFileOn() {
		t.Fatal("log file still on with an empty path")
	}
	svc.logTo(svc.global, "three")
	if ga, gb := readLog(t, a), readLog(t, b); !strings.HasSuffix(ga, "Global: one\n") || !strings.HasSuffix(gb, "Global: two\n") {
		t.Errorf("a = %q, b = %q", ga, gb)
	}
}

// Past the size cap the file becomes <file>.1, replacing an older one.
func TestLogFileRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.log")
	if err := os.WriteFile(path+".1", []byte("very old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := startLogWriter(path, 100, nil)
	for i := 0; i < 12; i++ {
		w.add(strings.Repeat("x", 9) + string(rune('a'+i))) // 11 bytes with the newline
	}
	w.close()
	if !w.wait(3 * time.Second) {
		t.Fatal("writer did not stop")
	}
	old, cur := readLog(t, path+".1"), readLog(t, path)
	if strings.Contains(old, "very old") || len(old) != 110 || !strings.HasSuffix(old, "xxxxxxxxxj\n") {
		t.Errorf(".1 = %q", old)
	}
	if cur != "xxxxxxxxxk\nxxxxxxxxxl\n" {
		t.Errorf("file = %q", cur)
	}
	if st, err := os.Stat(path); err != nil || (st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Errorf("stat %v, %v", st, err)
	}
}

// A full buffer drops lines and counts them; it never waits.
func TestLogFileFullBufferDrops(t *testing.T) {
	w := &logWriter{lines: make(chan string, logFileBuffer), done: make(chan struct{})} // no writer reads it
	done := make(chan struct{})
	go func() {
		for i := 0; i < logFileBuffer+5; i++ {
			w.add("line")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("add blocked on a full buffer")
	}
	if n := w.drops.Load(); n != 5 {
		t.Errorf("drops = %d, want 5", n)
	}
}

// An error is told once, not on every line.
func TestLogFileErrorToldOnce(t *testing.T) {
	var told atomic.Int32
	w := startLogWriter(filepath.Join(t.TempDir(), "missing", "services.log"), 100, func(err error) {
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error %v", err)
		}
		told.Add(1)
	})
	for i := 0; i < 5; i++ {
		w.add("line")
	}
	w.close()
	w.wait(3 * time.Second)
	if told.Load() != 1 || !w.failed.Load() {
		t.Errorf("told %d times, failed %v", told.Load(), w.failed.Load())
	}
}
