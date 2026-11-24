package services

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

func TestCountSmileys(t *testing.T) {
	for text, want := range map[string]int64{
		"":             0,
		"hi :)":        1,
		":) :-) ;) :D": 4,
		":P:P":         2,
		":-(":          0, // sad, not happy
	} {
		if got := countSmileys(text, smileysHappy); got != want {
			t.Errorf("countSmileys(%q) = %d, want %d", text, got, want)
		}
	}
}

// Chanstats: only identified people with NickServ SET CHANSTATS on count,
// in channels with ChanServ SET CHANSTATS on; fantasy lines, CTCPs and
// services don't. STATS, TOP and the fantasy commands show the counts, the
// periods start again, and DROP takes the channel's counts along.
func TestChanStats(t *testing.T) {
	svc, h := newServices(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local) // a Wednesday
	svc.now = func() time.Time { return now }
	ns, cs, bs := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ"), h.net.UserByNick("BotServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp")
	svc.Message(wasp.UID, ns, "REGISTER honeypot42")
	h.waitFor("Wasp", "registered")
	ann := helpUser(t, h, "1LFAAAAAC", "Ann") // not registered
	for _, u := range []*state.User{wasp, ann} {
		if err := h.Emit(&state.Join{UID: u.UID, Channel: "#hive", TS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Flush()

	// A bot for the fantasy commands.
	h.opers = map[string]string{bee.UID: "b"}
	svc.Message(bee.UID, bs, "BOT ADD Bzz bot bot.host Busy Bee")
	svc.Message(bee.UID, bs, "ASSIGN #hive Bzz")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Bot Bzz has been assigned to #hive.") {
		t.Fatalf("ASSIGN: %q", got)
	}
	h.opers = nil
	h.notices = nil

	hive, _ := svc.store.ChannelByName("#hive")
	beeID := svc.AccountID(bee.UID)
	ch := h.net.Channel("#hive")
	say := func(u *state.User, text string) {
		t.Helper()
		svc.ChannelText(u.UID, ch, text)
		svc.Flush()
	}
	stats := func(who *state.User, line string) string {
		t.Helper()
		svc.Message(who.UID, cs, line)
		return noticeTo(h, who.Nick)
	}

	say(bee, "before anything is on")
	if got := stats(bee, "STATS #hive"); got != "Chanstats is off for #hive." {
		t.Fatalf("STATS with CHANSTATS off: %q", got)
	}
	if got := stats(bee, "SET #hive CHANSTATS ON"); got != "Chanstats is now on for #hive." {
		t.Fatalf("SET CHANSTATS ON: %q", got)
	}
	say(bee, "Bee hasn't opted in")
	if _, ok := svc.store.ChanStatsOf(hive.ID, beeID, now); ok {
		t.Fatal("counted before NickServ SET CHANSTATS ON")
	}
	svc.Message(bee.UID, ns, "SET CHANSTATS ON")
	if got := noticeTo(h, "Bee"); got != "What Bee says in channels with CHANSTATS on is counted now." {
		t.Fatalf("NS SET CHANSTATS ON: %q", got)
	}

	say(bee, "hello there :) :D")         // 17 letters, 2 words, 2 smileys
	say(bee, "\x01ACTION waves\x01")      // an action: 5 letters, 1 word
	say(bee, "\x01VERSION\x01")           // a CTCP: not counted
	say(bee, "!seen Wasp")                // a fantasy command: not counted
	say(bee, "Bzz: help")                 // to the bot: not counted
	say(wasp, "Wasp didn't opt in")       // not counted
	say(ann, "Ann isn't even identified") // not counted
	for _, ev := range []state.Event{
		&state.Kick{Source: bee.UID, UID: wasp.UID, Channel: "#hive", Reason: "out"},
		&state.Topic{Source: bee.UID, Channel: "#hive", Text: "busy busy", By: "Bee", At: now},
		&state.ChannelMode{Source: bee.UID, Channel: "#hive", Changes: []state.ModeChange{{Add: true, Mode: 'm'}}},
	} {
		if err := h.Emit(ev); err != nil {
			t.Fatal(err)
		}
	}
	svc.Flush()
	h.notices = nil

	want := "Channel stats for Bee on #hive:\n" +
		"  All time    letters: 22, words: 3, lines: 2, smileys: 2, actions: 1\n" +
		"  This month  letters: 22, words: 3, lines: 2, smileys: 2, actions: 1\n" +
		"  This week   letters: 22, words: 3, lines: 2, smileys: 2, actions: 1\n" +
		"  Today       letters: 22, words: 3, lines: 2, smileys: 2, actions: 1\n" +
		"  Kicks given: 1, kicked: 0, modes set: 1, topics set: 1"
	if got := stats(bee, "STATS #hive"); got != want {
		t.Fatalf("STATS:\n%s\nwant:\n%s", got, want)
	}
	if got := stats(wasp, "STATS #hive Bee"); got != want {
		t.Fatalf("STATS Bee for Wasp:\n%s", got)
	}
	if got := stats(wasp, "STATS #hive"); got != "No stats for Wasp." {
		t.Fatalf("STATS for Wasp: %q", got)
	}
	if got := stats(ann, "STATS #hive"); got != "Identify first: /ns identify <password>" {
		t.Fatalf("STATS for Ann: %q", got)
	}
	if got := stats(bee, "STATS #hive Nobody"); got != "Nobody isn't registered." {
		t.Fatalf("STATS Nobody: %q", got)
	}
	if got := stats(bee, "GSTATS"); !strings.HasPrefix(got, "Network stats for Bee:\n  All time    letters: 22,") {
		t.Fatalf("GSTATS: %q", got)
	}
	top := "Top 3 of #hive\n 1 \x02Bee             \x02 letters: 22, words: 3, lines: 2, smileys: 2, actions: 1"
	if got := stats(bee, "TOP #hive"); got != top {
		t.Fatalf("TOP: %q", got)
	}
	if got := stats(bee, "GTOP"); !strings.HasPrefix(got, "Top 3 of BeeNet\n 1 \x02Bee") {
		t.Fatalf("GTOP: %q", got)
	}

	// The fantasy commands answer through the bot and aren't counted.
	if err := h.Emit(&state.Join{UID: wasp.UID, Channel: "#hive", TS: 1}); err != nil {
		t.Fatal(err)
	}
	svc.Flush()
	h.notices = nil
	for line, prefix := range map[string]string{
		"!stats":      "Channel stats for Bee on #hive:\n  All time    letters: 22,",
		"!stats Wasp": "No stats for Wasp.",
		"!gstats":     "Network stats for Bee:",
		"!top":        top,
		"!top10":      "Top 10 of #hive\n 1 \x02Bee",
		"!gtop":       "Top 3 of BeeNet",
		"!gtop10":     "Top 10 of BeeNet",
	} {
		say(bee, line)
		if got := noticeTo(h, "Bee"); !strings.HasPrefix(got, prefix) {
			t.Errorf("%s: %q, want %q...", line, got, prefix)
		}
	}
	if p, _ := svc.store.ChanStatsOf(hive.ID, beeID, now); p[store.StatsTotal].Lines != 2 {
		t.Errorf("fantasy commands were counted: %+v", p[store.StatsTotal])
	}

	// A private channel's counts are for its own people.
	stats(bee, "SET #hive PRIVATE ON")
	if got := stats(wasp, "STATS #hive Bee"); got != "#hive is private." {
		t.Errorf("STATS of a private channel for an outsider: %q", got)
	}
	if got := stats(wasp, "TOP #hive"); got != "#hive is private." {
		t.Errorf("TOP of a private channel for an outsider: %q", got)
	}
	stats(bee, "SET #hive PRIVATE OFF")

	// INFO shows both settings.
	if got := stats(bee, "INFO #hive"); !strings.Contains(got, "chanstats") {
		t.Errorf("CS INFO misses chanstats: %q", got)
	}
	svc.Message(bee.UID, ns, "INFO Bee")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "chanstats") {
		t.Errorf("NS INFO misses chanstats: %q", got)
	}

	// The next day today starts from zero; the next Monday the week does.
	now = now.AddDate(0, 0, 1)
	say(bee, "thursday")
	got := stats(bee, "STATS #hive")
	for _, line := range []string{
		"  All time    letters: 30, words: 4, lines: 3,",
		"  This week   letters: 30, words: 4, lines: 3,",
		"  Today       letters: 8, words: 1, lines: 1,",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("Thursday: no %q in\n%s", line, got)
		}
	}
	now = time.Date(2026, 11, 30, 9, 0, 0, 0, time.Local)
	got = stats(bee, "STATS #hive")
	for _, line := range []string{
		"  All time    letters: 30,",
		"  This month  letters: 30,",
		"  This week   letters: 0, words: 0, lines: 0, smileys: 0, actions: 0",
		"  Today       letters: 0,",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("Monday: no %q in\n%s", line, got)
		}
	}

	// Opting out stops the counting.
	svc.Message(bee.UID, ns, "SET CHANSTATS OFF")
	if got := noticeTo(h, "Bee"); got != "What Bee says is no longer counted." {
		t.Fatalf("NS SET CHANSTATS OFF: %q", got)
	}
	say(bee, "not counted")
	if p, _ := svc.store.ChanStatsOf(hive.ID, beeID, now); p[store.StatsTotal].Lines != 3 {
		t.Errorf("counted after SET CHANSTATS OFF: %+v", p[store.StatsTotal])
	}

	// DROP takes the channel's counts; the network-wide ones stay.
	svc.SaveChanStats()
	stats(bee, "DROP #hive #hive")
	if reg, _ := svc.store.ChannelByName("#hive"); reg != nil {
		t.Fatal("#hive not dropped")
	}
	if _, ok := svc.store.ChanStatsOf(hive.ID, beeID, now); ok {
		t.Error("dropped channel still has stats")
	}
	if p, ok := svc.store.ChanStatsOf(0, beeID, now); !ok || p[store.StatsTotal].Lines != 3 {
		t.Errorf("network-wide stats after DROP: %+v %v", p[store.StatsTotal], ok)
	}
}

func TestChanStatsVisible(t *testing.T) {
	open := &store.Channel{}
	priv := &store.Channel{Private: true}
	if !ChanStatsVisible(open, store.LevelNone) || !ChanStatsVisible(open, store.LevelVOP) {
		t.Error("a public channel's stats are for everyone")
	}
	if ChanStatsVisible(priv, store.LevelNone) {
		t.Error("a private channel's stats are not for people off its list")
	}
	if !ChanStatsVisible(priv, store.LevelVOP) || !ChanStatsVisible(priv, store.LevelFounder) {
		t.Error("a private channel's stats are for its list")
	}
}

func TestChanTogglesHasChanStats(t *testing.T) {
	if !ChanToggles(&store.Channel{ChanStats: true})["CHANSTATS"] || ChanToggles(&store.Channel{})["CHANSTATS"] {
		t.Error("CHANSTATS toggle")
	}
}
