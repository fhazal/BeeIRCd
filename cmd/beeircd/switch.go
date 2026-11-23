package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/gate"
)

// gateProto is what `beeircd gate-proto` prints: the door keeper protocol
// this program speaks, "off" where it has no door keeper.
func gateProto() string {
	if gate.Supported {
		return gate.Proto
	}
	return "off"
}

// switchPlan is how `beeircd update` (and --rollback) moves the running
// server to the program it installs.
type switchPlan struct {
	handover bool   // the door keeper hands everyone to the new program
	why      string // otherwise: why everyone reconnects once
}

// planSwitch decides it from the running server's status and what the new
// program says for gate-proto (next; "" when it can't say: it is from
// before the hand-over) and handover-version (nextSnap; 0 when not known
// yet, before the download). nextVersion names the new program in the text.
func planSwitch(st *control.Status, next string, nextSnap int, nextVersion string) switchPlan {
	switch {
	case st.Gate == "":
		return switchPlan{why: "the server running now can't hand people over yet"}
	case st.Gate == "off" && st.OS != "" && st.OS != "linux":
		return switchPlan{why: "the door keeper only runs on Linux"}
	case st.Gate == "off":
		return switchPlan{why: "the server runs without the door keeper, -no-gate"}
	case !handoverProto(st.Gate):
		return switchPlan{why: "the door keeper running now can't hand people over"}
	case next == "":
		return switchPlan{why: "BeeIRCd " + nextVersion + " can't take people over"}
	case next != st.Gate:
		return switchPlan{why: "the door keeper itself changed"}
	case nextSnap != 0 && nextSnap != max(st.HandoverVersion, 1):
		return switchPlan{why: "the hand-over format changed"}
	}
	return switchPlan{handover: true}
}

// handoverProto: door keeper protocol 2 brought the hand-over.
func handoverProto(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 2
}

// newGateProto asks the program file exe which door keeper protocol it
// speaks; "" when it doesn't know the question (older than the hand-over).
func (u *updater) newGateProto(exe string) string {
	out, err := u.run(exe, "gate-proto")
	out = strings.TrimSpace(out)
	if _, nerr := strconv.Atoi(out); err != nil || (out != "off" && nerr != nil) {
		return ""
	}
	return out
}

// newHandoverVersion asks the program file exe which hand-over snapshot
// format it loads; 1 when it doesn't know the question (it is from before
// the question, when 1 was the only format).
func (u *updater) newHandoverVersion(exe string) int {
	out, err := u.run(exe, "handover-version")
	n, nerr := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || nerr != nil || n < 1 {
		return 1
	}
	return n
}

// switchNote is the line before the question: what happens to the people
// online.
func switchNote(plan switchPlan, st *control.Status, nextVersion string) string {
	if plan.handover {
		return fmt.Sprintf("\n  The server switches to %s without disconnecting anyone: the door keeper\n  hands %s on this server over to it.\n", nextVersion, people(st.Local))
	}
	return fmt.Sprintf("\n  This update reconnects everyone once (%s):\n  %s online %s disconnected for a few seconds; their clients\n  reconnect by themselves.\n",
		plan.why, people(st.Local), map[bool]string{true: "is", false: "are"}[st.Local == 1])
}

// stayed is "47 of 48 people stayed connected".
func stayed(h *control.Handover) string {
	of := max(h.Of, h.Kept)
	switch {
	case of == 0:
		return "nobody was connected"
	case of == 1:
		return fmt.Sprintf("%d of 1 person stayed connected", h.Kept)
	}
	return fmt.Sprintf("%d of %d people stayed connected", h.Kept, of)
}

// switchTo moves the running server (st, as it was before) to the program
// now installed, version v: by hand-over when plan says so, else (or when
// the hand-over can't start) by restarting the service, after setting the
// quit message. answered: v is running. handed: it took people over and
// said so (done, like "switched to 1.5.0", then who stayed); otherwise the
// caller prints its restart line.
func (u *updater) switchTo(v string, st *control.Status, plan switchPlan, quit, done string) (answered, handed bool) {
	if plan.handover {
		if _, err := u.ask(control.CmdHandover, "", 5*time.Second); err != nil {
			u.c.warn("the hand-over couldn't start (" + err.Error() + "); restarting instead: everyone reconnects once")
		} else {
			now, ok := u.waitForNew(v, st.PID)
			if !ok {
				return false, false
			}
			if h := now.Handover; h != nil && h.Problem == "" {
				u.c.ok(done + " · " + stayed(h))
				return true, true
			}
			why := "the new server started without them"
			if now.Handover != nil {
				why = now.Handover.Problem
			}
			u.c.warn("the hand-over failed (" + why + "); people reconnected as in a normal restart")
			return true, false
		}
	}
	if quit != "" {
		u.ask(control.CmdQuitReason, quit, 5*time.Second)
	}
	if _, err := u.run("systemctl", "restart", serviceName); err != nil {
		return false, false
	}
	return u.waitFor(v), false
}

// waitForNew waits until a new server process (not oldPID) answers with
// version v, and returns its status. Twice waitUp: the door keeper gives
// the old process up to 30 s to hand over, then the new one starts.
func (u *updater) waitForNew(v string, oldPID int) (*control.Status, bool) {
	deadline := time.Now().Add(2 * u.waitUp)
	for time.Now().Before(deadline) {
		if st, err := u.ask(control.CmdStatus, "", 2*time.Second); err == nil && st.Status != nil && st.Status.Version == v && st.Status.PID != oldPID {
			return st.Status, true
		}
		time.Sleep(u.pollStep)
	}
	return nil, false
}

// handoverRestart is `beeircd restart` behind the door keeper: the running
// server hands everyone over to a fresh start of the program on disk, so
// nobody is disconnected. tried false: it can't (the server isn't running,
// or why says what stops it) and the caller restarts the service instead.
func (u *updater) handoverRestart() (tried, ok bool, why string) {
	resp, err := u.ask(control.CmdStatus, "", 2*time.Second)
	if err != nil || resp.Status == nil {
		return false, false, ""
	}
	st := resp.Status
	plan := planSwitch(st, u.newGateProto(u.exe), u.newHandoverVersion(u.exe), u.current)
	if !plan.handover {
		return false, false, plan.why
	}
	answered, handed := u.switchTo(u.current, st, plan, "Server restarting, back in a few seconds", "restarted")
	if !answered {
		u.c.bad("BeeIRCd didn't answer after the restart; see beeircd status")
		return true, false, ""
	}
	if !handed {
		u.c.ok("BeeIRCd is running")
	}
	return true, true, ""
}
