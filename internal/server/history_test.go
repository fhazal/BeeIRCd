package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// +H channel history, playback on join and CHATHISTORY.

var histCaps = []string{"batch", "message-tags", "server-time", "draft/chathistory"}

func TestHistoryModeAndChathistory(t *testing.T) {
	s := testServer(t, nil)
	a := capLogin(t, s, "alice", histCaps...)
	a.send("VERSION")
	a.expect("CHATHISTORY=100")
	b := login(t, s, "bob")
	joinAll("#h", a, b)

	// Bad values are refused, big ones cut to the unregistered cap (200, 31d).
	a.send("MODE #h +H nonsense")
	a.expectNumeric("696", "#h", "H", "nonsense")
	a.send("MODE #h +H 9999:90d")
	a.expect("MODE #h +H 200:31d")
	b.expect("MODE #h +H 200:31d")

	for _, l := range []string{"one", "two", "three", "four"} {
		b.send("PRIVMSG #h :" + l)
		a.expect("PRIVMSG #h :" + l)
	}
	b.send("PRIVMSG @#h :ops only, not kept")
	a.expect("ops only, not kept")

	a.send("CHATHISTORY LATEST #h * 2")
	start := a.expect("BATCH +")
	if !strings.HasSuffix(start, " chathistory #h") {
		t.Fatalf("batch start: %s", start)
	}
	id := strings.Fields(start)[2][1:]
	l3 := a.expect(":bob!")
	if !strings.Contains(l3, "batch="+id) || !strings.Contains(l3, "msgid=") || !strings.HasSuffix(l3, "PRIVMSG #h :three") {
		t.Errorf("history line: %s", l3)
	}
	a.expect("PRIVMSG #h :four")
	a.expect("BATCH -" + id)

	// BEFORE the msgid of "three".
	var msgid string
	for _, tag := range strings.Split(strings.SplitN(l3, " ", 2)[0][1:], ";") {
		if v, ok := strings.CutPrefix(tag, "msgid="); ok {
			msgid = v
		}
	}
	a.send("CHATHISTORY BEFORE #h msgid=" + msgid + " 10")
	a.expect("PRIVMSG #h :one")
	a.expect("PRIVMSG #h :two")
	end := a.expect("BATCH -")
	_ = end
	a.send("CHATHISTORY AFTER #h msgid=" + msgid + " 10")
	a.expect("PRIVMSG #h :four")
	a.expect("BATCH -")

	// TARGETS lists the channel with its latest time.
	a.send("CHATHISTORY TARGETS timestamp=2020-01-01T00:00:00.000Z timestamp=2262-01-01T00:00:00.000Z 10")
	a.expect("BATCH +")
	a.expect("CHATHISTORY TARGETS #h 20")
	a.expect("BATCH -")

	// Errors: not a member, unknown subcommand, bad reference.
	c := login(t, s, "carol")
	c.send("CHATHISTORY LATEST #h * 10")
	c.expect("FAIL CHATHISTORY INVALID_TARGET LATEST #h :")
	c.send("CHATHISTORY LATEST #nowhere * 10")
	c.expect("FAIL CHATHISTORY INVALID_TARGET LATEST #nowhere :")
	c.send("CHATHISTORY FOO #h * 10")
	c.expect("FAIL CHATHISTORY UNKNOWN_COMMAND FOO :")
	a.send("CHATHISTORY BEFORE #h * 10")
	a.expect("FAIL CHATHISTORY INVALID_PARAMS BEFORE * :")

	// Playback on join for clients without chathistory: newest lines.
	c.send("JOIN #h")
	c.expect(" 366 carol #h ")
	c.expect("PRIVMSG #h :one")
	c.expect("PRIVMSG #h :four")
	// With chathistory the client asks itself: no playback.
	d := capLogin(t, s, "dave", histCaps...)
	d.send("JOIN #h")
	d.expect(" 366 dave #h ")
	d.quiet("PRIVMSG #h")

	// -H forgets everything; the reply is an empty batch.
	a.send("MODE #h -H")
	a.expect("MODE #h -H")
	a.send("CHATHISTORY LATEST #h * 10")
	a.expect("BATCH +")
	if l := a.next(); !strings.Contains(l, "BATCH -") {
		t.Errorf("history left after -H: %s", l)
	}
}

func TestHistoryLabeled(t *testing.T) {
	s := testServer(t, nil)
	a := capLogin(t, s, "alice", append(histCaps, "labeled-response")...)
	b := login(t, s, "bob")
	joinAll("#h", a, b)
	a.send("MODE #h +H 10:1h")
	a.expect("MODE #h +H 10:1h")
	b.send("PRIVMSG #h :hi")
	a.expect("PRIVMSG #h :hi")
	a.send("@label=L1 CHATHISTORY LATEST #h * 10")
	outer := a.expect("BATCH +")
	if !strings.Contains(outer, "label=L1") || !strings.Contains(outer, "labeled-response") {
		t.Fatalf("outer batch: %s", outer)
	}
	inner := a.expect("BATCH +")
	if !strings.Contains(inner, "chathistory #h") || strings.Count(inner, "batch=") != 1 {
		t.Errorf("inner batch: %s", inner)
	}
	msg := a.expect("PRIVMSG #h :hi")
	if strings.Count(msg, "batch=") != 1 {
		t.Errorf("history line should carry only the inner batch tag: %s", msg)
	}
}

func TestHistoryDefaultAndGone(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.History.Default = "50:1d" })
	a := capLogin(t, s, "alice", histCaps...)
	a.send("JOIN #new")
	a.expect(" 366 alice #new ")
	a.send("MODE #new")
	a.expectNumeric("324", "#new", "+H")
	a.send("PRIVMSG #new :kept while the channel lives")
	a.sync()
	a.send("PART #new")
	a.expect("PART #new")
	// The channel is gone, so is its history (like UnrealIRCd).
	a.send("JOIN #new")
	a.expect(" 366 alice #new ")
	a.send("CHATHISTORY LATEST #new * 10")
	a.expect("BATCH +")
	if l := a.next(); !strings.Contains(l, "BATCH -") {
		t.Errorf("history survived an empty channel: %s", l)
	}
}

func TestHistorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	tweak := func(c *config.Config) {
		c.History.Default = "50:1d"
		c.History.Secret = "restart-history-secret"
		c.History.File = filepath.Join(dir, "history.db")
	}
	s := testServer(t, tweak)
	a := login(t, s, "alice")
	a.send("JOIN #keep")
	a.expect(" 366 alice #keep ")
	a.send("PRIVMSG #keep :before the restart")
	a.sync()
	stopServer(s)

	s2 := testServer(t, tweak)
	b := login(t, s2, "bob")
	b.send("JOIN #keep")
	b.expect(" 366 bob #keep ")
	b.expect("PRIVMSG #keep :before the restart")

	// Another secret can't read it: the server starts empty.
	stopServer(s2)
	s3 := testServer(t, func(c *config.Config) {
		tweak(c)
		c.History.Secret = "another-secret-value"
	})
	d := login(t, s3, "dave")
	d.send("JOIN #keep")
	d.expect(" 366 dave #keep ")
	d.quiet("before the restart")
}
