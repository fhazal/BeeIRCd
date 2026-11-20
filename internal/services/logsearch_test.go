package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logSearchReply runs posted work until nick got a notice with want, then
// returns all of nick's notices.
func logSearchReply(t *testing.T, h *fakeHost, nick, want string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, n := range h.notices {
			if strings.HasPrefix(n, nick+": ") && strings.Contains(n, want) {
				return noticeTo(h, nick)
			}
		}
		select {
		case f := <-h.posts:
			f()
		case <-deadline:
			t.Fatalf("no notice to %s with %q; got %q", nick, want, h.notices)
		}
	}
}

func TestOperServLogSearch(t *testing.T) {
	svc, h := newServices(t)
	opsv := h.net.UserByNick("OperServ")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	small := helpUser(t, h, "1LFAAAAAC", "Small")
	h.opers = map[string]string{boss.UID: "oa", small.UID: "og"}

	svc.Message(boss.UID, opsv, "LOGSEARCH drop")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Services don't log to a file; set [services] log_file first.") {
		t.Fatalf("LOGSEARCH with no log file: %q", got)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "services.log")
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)
	ago := func(d time.Duration) string { return now.Add(-d).Format(logTimeFormat) }
	day := 24 * time.Hour
	write := func(name string, lines ...string) {
		if err := os.WriteFile(name, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(path+".1",
		ago(9*day)+" NickServ: ADMIN: old drop",
		ago(2*day)+" NickServ: COMMAND: drop A1",
	)
	write(path,
		ago(day)+" NickServ: COMMAND: drop B1",
		ago(day-time.Hour)+" ChanServ: COMMAND: drop B2",
		ago(time.Minute)+" Global: nothing here",
		"not a log line drop",
		ago(0)+" NickServ: COMMAND: DROP B3",
	)
	// LOGSEARCH reads [services] log_file; the lines services write while
	// the test runs (LOGSEARCH's own) go to another file, so they don't
	// show up in the results.
	svc.now = func() time.Time { return now }
	svc.cfg.Services.LogFile = path
	svc.logw = startLogWriter(filepath.Join(dir, "own.log"), 10<<20, nil)
	t.Cleanup(svc.CloseLogFile)

	svc.Message(small.UID, opsv, "LOGSEARCH drop")
	if got := noticeTo(h, "Small"); !strings.Contains(got, "You need oper flag a") {
		t.Fatalf("LOGSEARCH without flag a: %q", got)
	}

	cases := []struct {
		cmd  string
		want []string // the result lines' ends, in order
		last string
	}{
		// No wildcards: matched anywhere, either case; .1 first, 7 days back.
		{"LOGSEARCH drop", []string{"drop A1", "drop B1", "drop B2", "DROP B3"}, "Showing 4 of 4 matches."},
		{"LOGSEARCH +10d drop", []string{"old drop", "drop A1", "drop B1", "drop B2", "DROP B3"}, "Showing 5 of 5 matches."},
		{"LOGSEARCH +1d drop", []string{"drop B1", "drop B2", "DROP B3"}, "Showing 3 of 3 matches."},
		// The newest are kept, still oldest first.
		{"LOGSEARCH +2l drop", []string{"drop B2", "DROP B3"}, "Showing 2 of 4 matches."},
		{"LOGSEARCH +10d +1L drop", []string{"DROP B3"}, "Showing 1 of 5 matches."},
		// Wildcards work, and the pattern is always wrapped in * (Anope).
		{"LOGSEARCH *ChanServ: *B?", []string{"drop B2"}, "Showing 1 of 1 matches."},
		{"LOGSEARCH drop b*", []string{"drop B1", "drop B2", "DROP B3"}, "Showing 3 of 3 matches."},
	}
	for _, tc := range cases {
		svc.Message(boss.UID, opsv, tc.cmd)
		got := strings.Split(logSearchReply(t, h, "Boss", tc.last), "\n")
		if len(got) != len(tc.want)+1 || got[len(got)-1] != tc.last {
			t.Errorf("%s: %q", tc.cmd, got)
			continue
		}
		for i, w := range tc.want {
			if !strings.HasSuffix(got[i], w) || !strings.HasPrefix(got[i], "2026-11-") {
				t.Errorf("%s: line %d = %q, want ...%q", tc.cmd, i, got[i], w)
			}
		}
	}

	svc.Message(boss.UID, opsv, "LOGSEARCH +7d")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Syntax: LOGSEARCH") {
		t.Errorf("LOGSEARCH without a pattern: %q", got)
	}

	// READONLY lets it through: it only reads.
	svc.Message(boss.UID, opsv, "SET READONLY ON")
	noticeTo(h, "Boss")
	svc.Message(boss.UID, opsv, "LOGSEARCH B3")
	if got := logSearchReply(t, h, "Boss", "matches."); !strings.Contains(got, "Showing 1 of 1 matches.") {
		t.Errorf("LOGSEARCH in read-only mode: %q", got)
	}
}
