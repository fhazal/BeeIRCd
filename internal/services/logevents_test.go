package services

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// newLogServices is services with the log channel #services and the given
// log_events (nil: the default).
func newLogServices(t *testing.T, events []string) (*Services, *fakeHost) {
	t.Helper()
	svc, h := newServices(t)
	svc.cfg.Services.LogChannel = "#services"
	svc.cfg.Services.LogEvents = events
	svc.SyncLogChannel("")
	h.said = nil
	return svc, h
}

// addLogUser puts Bee (or nick) on the network.
func addLogUser(t *testing.T, svc *Services, h *fakeHost, uid, nick string) *state.User {
	t.Helper()
	u := &state.User{UID: uid, Nick: nick, Ident: "~" + strings.ToLower(nick), Host: "cloak.test", RealHost: "real.example",
		IP: "192.0.2.7", Realname: "Real Name", Server: "hub.test", Modes: map[byte]bool{}}
	if err := h.Emit(&state.UserAdd{User: u}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	return u
}

// emit applies ev and lets services handle it.
func (h *fakeHost) emitFlush(t *testing.T, ev state.Event) {
	t.Helper()
	if err := h.Emit(ev); err != nil {
		t.Fatal(err)
	}
	h.svc.Flush()
}

// takeSaid returns the log channel lines so far and forgets them.
func (h *fakeHost) takeSaid() []string {
	out := h.said
	h.said = nil
	return out
}

func wantLines(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func TestLogOperLines(t *testing.T) {
	svc, h := newLogServices(t, nil)
	u := addLogUser(t, svc, h, "0BEAAAAAA", "Bee")
	h.takeSaid()

	u.OperClass, u.OperName = "admin", "beeop"
	h.emitFlush(t, &state.UserMode{UID: u.UID, Changes: []state.ModeChange{{Add: true, Mode: 'o'}}})
	wantLines(t, "oper up", h.takeSaid(),
		"Global #services: USERS: Bee!~bee@real.example (Real Name) is now an IRC operator (Server Administrator, oper account beeop)")

	h.emitFlush(t, &state.UserMode{UID: u.UID, Changes: []state.ModeChange{{Add: false, Mode: 'o'}}})
	wantLines(t, "oper down", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example (Real Name) is no longer an IRC operator")

	// An oper on another server: their title, no oper account.
	r := addLogUser(t, svc, h, "1LFAAAAAA", "Wasp")
	h.takeSaid()
	r.OperTitle = "is the Hive Queen"
	h.emitFlush(t, &state.UserMode{UID: r.UID, Changes: []state.ModeChange{{Add: true, Mode: 'o'}}})
	wantLines(t, "remote oper", h.takeSaid(), "Global #services: USERS: Wasp!~wasp@real.example (Real Name) is now an IRC operator (Hive Queen)")

	// Nothing known: no brackets.
	n := addLogUser(t, svc, h, "1LFAAAAAB", "Ant")
	h.takeSaid()
	h.emitFlush(t, &state.UserMode{UID: n.UID, Changes: []state.ModeChange{{Add: true, Mode: 'o'}}})
	wantLines(t, "plain oper", h.takeSaid(), "Global #services: USERS: Ant!~ant@real.example (Real Name) is now an IRC operator")

	// Other modes: no line by default.
	h.emitFlush(t, &state.UserMode{UID: u.UID, Changes: []state.ModeChange{{Add: true, Mode: 'i'}}})
	wantLines(t, "umode off by default", h.takeSaid())
}

func TestLogUmodeHostChannel(t *testing.T) {
	svc, h := newLogServices(t, []string{"umode", "host", "channel"})
	u := addLogUser(t, svc, h, "0BEAAAAAA", "Bee")
	u.Modes['w'] = true
	wantLines(t, "connect is off", h.takeSaid())

	h.emitFlush(t, &state.UserMode{UID: u.UID, Changes: []state.ModeChange{{Add: true, Mode: 'i'}, {Add: true, Mode: 'x'}, {Add: false, Mode: 'w'}}})
	wantLines(t, "umode", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example changes modes to +ix-w")

	h.emitFlush(t, &state.HostChange{UID: u.UID, Host: "bee.vhost"})
	wantLines(t, "host", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example changed host to bee.vhost")
	h.emitFlush(t, &state.HostChange{UID: u.UID, Host: "hive.vhost", Ident: "queen"})
	wantLines(t, "vident", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example changed host to queen@hive.vhost")

	h.emitFlush(t, &state.Join{UID: u.UID, Channel: "#hive", TS: 1})
	wantLines(t, "join new", h.takeSaid(),
		"Global #services: CHANNEL: #hive created by Bee",
		"Global #services: CHANNEL: Bee!~bee@real.example joined #hive")
	o := addLogUser(t, svc, h, "0BEAAAAAB", "Wasp")
	h.emitFlush(t, &state.Join{UID: o.UID, Channel: "#HIVE", TS: 1})
	wantLines(t, "join", h.takeSaid(), "Global #services: CHANNEL: Wasp!~wasp@real.example joined #hive")
	h.emitFlush(t, &state.Part{UID: o.UID, Channel: "#hive"})
	wantLines(t, "part", h.takeSaid(), "Global #services: CHANNEL: Wasp!~wasp@real.example parted #hive")
	h.emitFlush(t, &state.Part{UID: u.UID, Channel: "#hive"})
	wantLines(t, "last part", h.takeSaid(),
		"Global #services: CHANNEL: Bee!~bee@real.example parted #hive",
		"Global #services: CHANNEL: #hive is now empty")

	// The log channel itself and a link's sync stay out.
	h.emitFlush(t, &state.Join{UID: u.UID, Channel: "#services", TS: 1})
	h.emitFlush(t, &state.Part{UID: u.UID, Channel: "#services"})
	h.emitFlush(t, &state.Join{UID: u.UID, Channel: "#burst", TS: 1, Burst: true})
	wantLines(t, "log channel and burst", h.takeSaid())
}

func TestLogKillAndServers(t *testing.T) {
	svc, h := newLogServices(t, nil)
	u := addLogUser(t, svc, h, "0BEAAAAAA", "Bee")
	h.takeSaid()
	svc.LogKill(u, "Oper", "flooding")
	svc.LogKill(h.net.UserByNick("NickServ"), "Oper", "no") // a bot: no line
	svc.LogServerJoin("leaf.test", "The Leaf", "hub.test")
	svc.LogServerSplit("leaf.test", "The Leaf", "hub.test", "Ping timeout")
	wantLines(t, "kill and servers", h.takeSaid(),
		"Global #services: USERS: Bee!~bee@real.example was killed by Oper (Reason: flooding)",
		"Global #services: SERVER: leaf.test (The Leaf) has connected to the network (uplinked to hub.test)",
		"Global #services: SERVER: leaf.test (The Leaf) quit from hub.test (Ping timeout)")
}

func TestLogBanHitsCapped(t *testing.T) {
	svc, h := newLogServices(t, nil)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	svc.LogBanHit("sqline", "Bad*", "Bad1!~x@h", "", "No bad nicks")
	wantLines(t, "no setter", h.takeSaid(), "OperServ #services: SQLINE: Bad* hit Bad1!~x@h (No bad nicks)")

	for range 7 {
		svc.LogBanHit("akill", "*@bad.example", "X!~x@bad.example", "Admin", "go away")
	}
	got := h.takeSaid()
	if len(got) != 5 || got[0] != "OperServ #services: AKILL: *@bad.example hit X!~x@bad.example (set by Admin: go away)" {
		t.Fatalf("7 hits in a window: %q", got)
	}
	svc.Tick(now.Add(9 * time.Second))
	wantLines(t, "window still open", h.takeSaid())
	svc.Tick(now.Add(10 * time.Second))
	wantLines(t, "window over", h.takeSaid(), "OperServ #services: ... and 2 more hits on *@bad.example")

	// The summary also comes with the first hit after a window.
	for range 6 {
		svc.LogBanHit("zline", "192.0.2.*", "192.0.2.9", "Admin", "bots")
	}
	h.takeSaid()
	now = now.Add(15 * time.Second)
	svc.LogBanHit("zline", "192.0.2.*", "192.0.2.9", "Admin", "bots")
	wantLines(t, "next window", h.takeSaid(),
		"OperServ #services: ... and 1 more hits on 192.0.2.*",
		"OperServ #services: ZLINE: 192.0.2.* hit 192.0.2.9 (set by Admin: bots)")

	svc.LogSessionLimit("192.0.2.7", 3, "Bee!~bee@real.example")
	svc.LogSessionLimit("192.0.2.7", 3, "")
	wantLines(t, "sessions", h.takeSaid(),
		"OperServ #services: SESSION: limit of 3 reached for 192.0.2.7 (Bee!~bee@real.example)",
		"OperServ #services: SESSION: limit of 3 reached for 192.0.2.7")
}

// A new AKILL posts a line for each person it removes.
func TestLogNewBanHits(t *testing.T) {
	svc, h := newLogServices(t, nil)
	addLogUser(t, svc, h, "0BEAAAAAA", "Bee")
	addLogUser(t, svc, h, "0BEAAAAAB", "Wasp").RealHost = "other.example"
	h.takeSaid()
	if _, hit, problem := svc.BanVia("Admin", "RPC", "akill", "*@real.example", "go away", 0); problem != "" || hit != 1 {
		t.Fatalf("BanVia: %d hit, %q", hit, problem)
	}
	if got := h.takeSaid(); !slices.Contains(got, "OperServ #services: AKILL: *@real.example hit Bee!~bee@real.example (set by Admin: go away)") {
		t.Errorf("no AKILL hit line in %q", got)
	}
}

func TestLogEventFilter(t *testing.T) {
	svc, h := newLogServices(t, nil)
	u := addLogUser(t, svc, h, "0BEAAAAAA", "Bee")
	svc.LogUserConnect(u) // the server calls it for its own people
	wantLines(t, "connect on by default", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example (cloak.test) (Real Name) [192.0.2.7] connected to the network (hub.test)")

	// An old config with log_connects = false: no USERS connect lines,
	// the rest still on.
	no := false
	svc.cfg.Services.LogConnects = &no
	svc.LogUserConnect(u)
	svc.LogKill(u, "Oper", "x")
	wantLines(t, "log_connects off", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example was killed by Oper (Reason: x)")

	// Only kills.
	svc.cfg.Services.LogEvents = []string{"kill"}
	svc.cfg.Services.LogConnects = nil
	svc.LogUserConnect(u)
	svc.LogServerJoin("leaf.test", "x", "hub.test")
	svc.LogBanHit("akill", "*@x", "a", "b", "c")
	h.emitFlush(t, &state.UserMode{UID: u.UID, Changes: []state.ModeChange{{Add: true, Mode: 'o'}}})
	svc.LogKill(u, "Oper", "y")
	wantLines(t, "kill only", h.takeSaid(), "Global #services: USERS: Bee!~bee@real.example was killed by Oper (Reason: y)")

	// No log channel: nothing, and no flood cap kept.
	svc.cfg.Services.LogEvents = []string{"*"}
	svc.cfg.Services.LogChannel = ""
	svc.LogBanHit("akill", "*@x", "a", "b", "c")
	svc.LogKill(u, "Oper", "z")
	wantLines(t, "no log channel", h.takeSaid())
	if len(svc.hitCaps) != 0 {
		t.Errorf("flood caps kept with no log channel: %d", len(svc.hitCaps))
	}
}
