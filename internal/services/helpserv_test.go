package services

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

func helpUser(t *testing.T, h *fakeHost, uid, nick string) *state.User {
	t.Helper()
	u := &state.User{UID: uid, Nick: nick, Ident: "~" + strings.ToLower(nick), Host: "leaf.example", RealHost: "leaf.example",
		Server: "leaf.test", Modes: map[byte]bool{}}
	if err := h.Emit(&state.UserAdd{User: u}); err != nil {
		t.Fatal(err)
	}
	h.svc.Flush()
	return u
}

// noticeTo is the notices to nick so far, and clears them.
func noticeTo(h *fakeHost, nick string) string {
	var out []string
	rest := h.notices[:0]
	for _, n := range h.notices {
		if strings.HasPrefix(n, nick+": ") {
			out = append(out, strings.TrimPrefix(n, nick+": "))
		} else {
			rest = append(rest, n)
		}
	}
	h.notices = rest
	return strings.Join(out, "\n")
}

func TestHelpServTopics(t *testing.T) {
	svc, h := newServices(t)
	svc.OnServerHelp(func(topic string) []string {
		if topic == "CHMODES" {
			return []string{"***** Channel modes *****", " n no outside messages"}
		}
		return nil
	})
	hs := h.net.UserByNick("HelpServ")
	if hs == nil {
		t.Fatal("no HelpServ")
	}
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")

	svc.Message(bee.UID, hs, "chmodes")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "no outside messages") {
		t.Errorf("chmodes: %q", got)
	}
	svc.Message(bee.UID, hs, "register")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "NickServ REGISTER") || !strings.Contains(got, "Syntax: REGISTER") {
		t.Errorf("register: %q", got)
	}
	svc.Message(bee.UID, hs, "nickserv identify")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Syntax: IDENTIFY") {
		t.Errorf("nickserv identify: %q", got)
	}
	svc.Message(bee.UID, hs, "help chmodes")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "no outside messages") {
		t.Errorf("help chmodes: %q", got)
	}
	svc.Message(bee.UID, hs, "zzzz")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "I have no help on zzzz") {
		t.Errorf("unknown word: %q", got)
	}
	// TOPIC needs flag s: for Bee "topic" is the IRC TOPIC help (none here).
	svc.Message(bee.UID, hs, "topic add rules be nice")
	if _, ok := svc.help.topics["rules"]; ok {
		t.Fatal("a non-oper added a topic")
	}
	noticeTo(h, "Bee")
	// Nor do helper or oper commands become questions for the helpers.
	for _, line := range []string{"topic add rules hacked", "helper add Bee", "take 1", "close 1 done"} {
		svc.Message(bee.UID, hs, line)
	}
	if len(svc.help.questions) != 0 {
		t.Errorf("commands queued as questions: %d", len(svc.help.questions))
	}
	noticeTo(h, "Bee")

	h.opers = map[string]string{bee.UID: "s"}
	svc.Message(bee.UID, hs, "topic add rules No spam.")
	svc.Message(bee.UID, hs, "topic add Rules Be nice.")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "has 2 lines now") {
		t.Errorf("topic add: %q", got)
	}
	h.opers = nil
	svc.Message(bee.UID, hs, "RULES")
	if got := noticeTo(h, "Bee"); got != "No spam.\nBe nice." {
		t.Errorf("rules: %q", got)
	}
	// Kept in the database.
	if ts, _ := svc.store.HelpTopics(); len(ts) != 1 || ts[0].Text != "No spam.\nBe nice." {
		t.Errorf("stored topics %+v", ts)
	}
}

func TestHelpServQuestions(t *testing.T) {
	svc, h := newServices(t)
	hs := h.net.UserByNick("HelpServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	oper := helpUser(t, h, "1LFAAAAAB", "Belon")
	h.opers = map[string]string{oper.UID: "o"}

	svc.Message(bee.UID, hs, "how do i get a vhost?")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "No helper is online") {
		t.Errorf("no helper: %q", got)
	}
	// The oper goes on duty and hears about the waiting question.
	h.SetUserModes(oper.UID, oper.UID, "+h")
	svc.Flush()
	if got := noticeTo(h, "Belon"); !strings.Contains(got, "1 question is waiting") {
		t.Errorf("on duty: %q", got)
	}
	svc.Message(bee.UID, hs, "another question here")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "You have a question waiting (#1)") {
		t.Errorf("second question: %q", got)
	}
	svc.Message(oper.UID, hs, "list")
	if got := noticeTo(h, "Belon"); !strings.Contains(got, "#1 Bee") || !strings.Contains(got, "open: how do i get a vhost?") {
		t.Errorf("list: %q", got)
	}
	// Bee isn't a helper: LIST is the help on LIST, not the queue.
	svc.Message(bee.UID, hs, "list")
	if got := noticeTo(h, "Bee"); strings.Contains(got, "#1 Bee") {
		t.Errorf("Bee saw the queue: %q", got)
	}
	svc.Message(oper.UID, hs, "take 1")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Belon is helping you now") {
		t.Errorf("take, asker: %q", got)
	}
	if got := noticeTo(h, "Belon"); !strings.Contains(got, "You took #1") {
		t.Errorf("take, helper: %q", got)
	}
	svc.Message(oper.UID, hs, "close 1 see /msg HostServ help request")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Belon closed your question. see /msg HostServ") {
		t.Errorf("close: %q", got)
	}
	if len(svc.help.questions) != 0 {
		t.Errorf("questions after close: %d", len(svc.help.questions))
	}

	// Rate limit, then a new question reaches the helper; quitting drops it.
	svc.Message(bee.UID, hs, "one more thing please")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "wait a moment") {
		t.Errorf("rate limit: %q", got)
	}
	svc.help.asked[bee.UID] = time.Now().Add(-time.Minute)
	svc.Message(bee.UID, hs, "one more thing please")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "(1 online)") {
		t.Errorf("ask with a helper on: %q", got)
	}
	if got := noticeTo(h, "Belon"); !strings.Contains(got, "#2 Bee (~bee@leaf.example) asks: one more thing please  (/msg HelpServ take 2)") {
		t.Errorf("helper notice: %q", got)
	}
	h.Emit(&state.UserQuit{UID: bee.UID, Reason: "bye"})
	svc.Flush()
	if len(svc.help.questions) != 0 {
		t.Errorf("question kept after the asker quit")
	}
}

func TestHelpServHelperList(t *testing.T) {
	svc, h := newServices(t)
	hs, ns := h.net.UserByNick("HelpServ"), h.net.UserByNick("NickServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	svc.Message(bee.UID, ns, "REGISTER honeypot42")
	h.waitFor("Bee", "registered")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	h.opers = map[string]string{boss.UID: "s"}

	svc.Message(boss.UID, hs, "helper add Bee")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Bee is a helper now") {
		t.Errorf("helper add: %q", got)
	}
	if !bee.HasMode('h') || !svc.IsHelper("Bee") {
		t.Fatalf("Bee after HELPER ADD: +h %v, IsHelper %v", bee.HasMode('h'), svc.IsHelper("Bee"))
	}
	// Log out: +h goes; identify again: it comes back.
	svc.Message(bee.UID, ns, "LOGOUT")
	svc.Flush()
	if bee.HasMode('h') {
		t.Error("+h kept after LOGOUT")
	}
	svc.Message(bee.UID, ns, "IDENTIFY honeypot42")
	h.waitFor("Bee", "Password accepted")
	if !bee.HasMode('h') {
		t.Error("no +h after IDENTIFY")
	}
	svc.Message(boss.UID, hs, "helper list")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Bee, online as Bee, added by Boss") {
		t.Errorf("helper list: %q", got)
	}
	svc.Message(boss.UID, hs, "helper del Bee")
	if bee.HasMode('h') || svc.IsHelper("Bee") {
		t.Error("Bee kept +h after HELPER DEL")
	}
}
