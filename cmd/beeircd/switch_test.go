package main

import (
	"bufio"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

func TestPlanSwitch(t *testing.T) {
	for _, c := range []struct {
		name, gate, os, next string
		snap, nextSnap       int // the running server's and the new program's hand-over format
		handover             bool
		why                  string
	}{
		{"hand-over", "2", "linux", "2", 1, 1, true, ""},
		{"old server", "", "linux", "2", 0, 1, false, "the server running now can't hand people over yet"},
		{"windows", "off", "windows", "off", 1, 1, false, "the door keeper only runs on Linux"},
		{"no-gate", "off", "linux", "2", 1, 1, false, "the server runs without the door keeper, -no-gate"},
		{"proto v1", "1", "linux", "2", 1, 1, false, "the door keeper running now can't hand people over"},
		{"old new program", "2", "linux", "", 1, 1, false, "BeeIRCd 1.3.0 can't take people over"},
		{"gate changed", "2", "linux", "3", 1, 1, false, "the door keeper itself changed"},
		{"new program without gate", "2", "linux", "off", 1, 1, false, "the door keeper itself changed"},
		{"format changed", "2", "linux", "2", 1, 2, false, "the hand-over format changed"},
		{"server too old to say its format", "2", "linux", "2", 0, 1, true, ""},
		{"format of a server too old to say changed", "2", "linux", "2", 0, 2, false, "the hand-over format changed"},
		{"new format not known yet", "2", "linux", "2", 1, 0, true, ""},
	} {
		p := planSwitch(&control.Status{Gate: c.gate, OS: c.os, HandoverVersion: c.snap}, c.next, c.nextSnap, "1.3.0")
		if p.handover != c.handover || p.why != c.why {
			t.Errorf("%s: got %+v, want handover=%v why=%q", c.name, p, c.handover, c.why)
		}
	}
}

func TestSwitchNoteAndStayed(t *testing.T) {
	st := &control.Status{Online: 98, Local: 97}
	got := switchNote(switchPlan{handover: true}, st, "1.3.0")
	want := "\n  The server switches to 1.3.0 without disconnecting anyone: the door keeper\n  hands the 97 people on this server over to it.\n"
	if got != want {
		t.Errorf("hand-over note:\n%q\nwant\n%q", got, want)
	}
	got = switchNote(switchPlan{why: "the door keeper itself changed"}, &control.Status{Online: 1}, "1.3.0")
	want = "\n  This update reconnects everyone once (the door keeper itself changed):\n  the 1 person online is disconnected for a few seconds; their clients\n  reconnect by themselves.\n"
	if got != want {
		t.Errorf("restart note:\n%q\nwant\n%q", got, want)
	}
	for _, c := range []struct {
		h    control.Handover
		want string
	}{
		{control.Handover{Kept: 97, Of: 98}, "97 of 98 people stayed connected"},
		{control.Handover{Kept: 1, Of: 1}, "1 of 1 person stayed connected"},
		{control.Handover{}, "nobody was connected"},
		{control.Handover{Kept: 3, Of: 2}, "3 of 3 people stayed connected"},
	} {
		if got := stayed(&c.h); got != c.want {
			t.Errorf("stayed(%+v) = %q, want %q", c.h, got, c.want)
		}
	}
}

func TestNewGateProto(t *testing.T) {
	u := &updater{}
	for out, want := range map[string]string{"2\n": "2", "off\n": "off", "Unknown command": "", "": ""} {
		u.run = func(string, ...string) (string, error) { return out, nil }
		if got := u.newGateProto("x"); got != want {
			t.Errorf("gate-proto printing %q: got %q, want %q", out, got, want)
		}
	}
}

func TestNewHandoverVersion(t *testing.T) {
	u := &updater{}
	for out, want := range map[string]int{"1\n": 1, "2\n": 2, "Unknown command": 1, "": 1, "0": 1} {
		u.run = func(string, ...string) (string, error) { return out, nil }
		if got := u.newHandoverVersion("x"); got != want {
			t.Errorf("handover-version printing %q: got %d, want %d", out, got, want)
		}
	}
	u.run = func(string, ...string) (string, error) { return "2", errors.New("exit 2") }
	if got := u.newHandoverVersion("x"); got != 1 {
		t.Errorf("a failed handover-version: got %d, want 1", got)
	}
}

func TestUpdateHandsOver(t *testing.T) {
	key := signingKey(t)
	srv := releaseServer(t, "1.0.1", key, "2")
	fs := &fakeSystem{running: "1.0.0", gate: "2", progGate: "2"}
	u, out := testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	text := out.String()
	for _, want := range []string{"The server switches to 1.0.1 without disconnecting anyone", "hands the 47 people on this server over to it",
		"✓ switched to 1.0.1 · 46 of 47 people stayed connected", "--rollback"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "restarted ·") || fs.quit != "" {
		t.Errorf("restarted too (quit %q):\n%s", fs.quit, text)
	}
	if slices.Contains(fs.calls, "systemctl restart beeircd") {
		t.Errorf("systemctl restart ran: %v", fs.calls)
	}

	// --rollback hands over too.
	u.current, u.in = "1.0.1", bufio.NewReader(strings.NewReader("y\n"))
	out.Reset()
	if code := u.rollback(false); code != 0 || fs.running != "1.0.0" {
		t.Fatalf("rollback exit %d, running %s\n%s", code, fs.running, out)
	}
	for _, want := range []string{"The server switches without disconnecting anyone.", "✓ switched to 1.0.0 · 46 of 47 people stayed connected"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("rollback output lacks %q:\n%s", want, out)
		}
	}
}

func TestUpdateHandoverFallbacks(t *testing.T) {
	key := signingKey(t)
	srv := releaseServer(t, "1.0.1", key, "2")
	restarted := func(fs *fakeSystem) bool { return slices.Contains(fs.calls, "systemctl restart beeircd") }

	// The release says yes, the downloaded program can't (an old one).
	fs := &fakeSystem{running: "1.0.0", gate: "2", comesUp: true}
	u, out := testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 || !restarted(fs) {
		t.Fatalf("exit %d, restarted %v\n%s", code, restarted(fs), out)
	}
	for _, want := range []string{"this update reconnects everyone once after all (BeeIRCd 1.0.1 can't take people over)", "restarted · 1.0.1 answering"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// The downloaded program loads another hand-over format.
	fs = &fakeSystem{running: "1.0.0", gate: "2", progGate: "2", progSnap: "2", comesUp: true}
	u, out = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 || !restarted(fs) {
		t.Fatalf("exit %d, restarted %v\n%s", code, restarted(fs), out)
	}
	if want := "this update reconnects everyone once after all (the hand-over format changed)"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}

	// The running server refuses: restart instead, with the quit message.
	fs = &fakeSystem{running: "1.0.0", gate: "2", progGate: "2", comesUp: true, handoverErr: true}
	u, out = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 || !restarted(fs) || fs.quit == "" {
		t.Fatalf("exit %d, restarted %v, quit %q\n%s", code, restarted(fs), fs.quit, out)
	}
	for _, want := range []string{"the hand-over couldn't start (a hand-over is already running); restarting instead: everyone reconnects once", "restarted · 1.0.1 answering"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// The new server started without them: say so, still done.
	fs = &fakeSystem{running: "1.0.0", gate: "2", progGate: "2", handoverWhy: "the old server didn't answer"}
	u, out = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 || restarted(fs) {
		t.Fatalf("exit %d, restarted %v\n%s", code, restarted(fs), out)
	}
	for _, want := range []string{"the hand-over failed (the old server didn't answer); people reconnected as in a normal restart", "restarted · 1.0.1 answering", "--rollback"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Without the door keeper (-no-gate): the restart text says why.
	fs = &fakeSystem{running: "1.0.0", gate: "off", progGate: "2", comesUp: true}
	u, out = testUpdater(t, "1.0.0", srv.URL, fs, "y\n")
	if code := u.update(false, false, false); code != 0 || !restarted(fs) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if want := "This update reconnects everyone once (the server runs without the door keeper, -no-gate)"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}
