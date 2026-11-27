package server

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// A split reaches batch clients as an IRCv3 netsplit batch; with
// hide_split non-opers see "*.net *.split" (opers the real names).
func TestLinkNetsplitBatch(t *testing.T) {
	hub, leaf := linkedPair(t)
	watcher := capLogin(t, hub.s, "watch", "batch")
	plain := login(t, hub.s, "plain")
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	for _, c := range []*tc{watcher, plain, op} {
		c.send("JOIN #split")
	}
	gone := login(t, leaf.s, "gone")
	gone.send("JOIN #split")
	watcher.expect(":gone!")
	plain.expect(":gone!")
	op.expect(":gone!")
	hub.s.onLoop(func() { hub.s.cfg.Link.HideSplit = true })
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			hub.s.dropLink(l, "test split")
		}
	})
	if l := watcher.expect("BATCH +"); !strings.Contains(l, " netsplit *.net *.split") {
		t.Errorf("batch start %q", l)
	}
	if l := watcher.expect("QUIT"); !strings.HasPrefix(l, "@batch=") || !strings.Contains(l, ":*.net *.split") {
		t.Errorf("tagged quit %q", l)
	}
	watcher.expect("BATCH -")
	if l := plain.expect("QUIT"); !strings.Contains(l, ":*.net *.split") {
		t.Errorf("plain quit %q", l)
	}
	if l := op.expect("QUIT"); !strings.Contains(l, ":hub.test.local leaf.test.local") {
		t.Errorf("oper quit %q", l)
	}
}

// LINKS, MAP, remote VERSION/MOTD/ADMIN/TIME/STATS, CONNECT and SQUIT.
func TestLinkOperInfoCommands(t *testing.T) {
	hub, leaf := linkedPair(t)
	ann := login(t, leaf.s, "ann")
	hub.seen(t, "ann")
	ann.send("LINKS")
	ann.expect(" 364 ann leaf.test.local leaf.test.local :0 ")
	ann.expect(" 364 ann hub.test.local leaf.test.local :1 ")
	ann.expect(" 364 ann services.test.local hub.test.local :2 ")
	ann.expect(" 365 ann * ")
	ann.send("MAP")
	ann.expect(" 006 ann :leaf.test.local (0LF) 1")
	ann.expect(" 006 ann :`- hub.test.local (0HB) 0")
	ann.expect(" 006 ann :   `- services.test.local (9HB) ")
	ann.expect(" 007 ann ")
	ann.send("VERSION hub.test.local")
	ann.expect(" 351 ann ")
	ann.send("ADMIN hub.test.local")
	ann.expect(" 256 ann hub.test.local ")
	ann.send("MOTD hub.test.local")
	ann.expect(" 37")
	ann.send("TIME hub.test.local")
	ann.expect(" 391 ann hub.test.local ")
	ann.send("STATS u hub.test.local")
	ann.expect(" 242 ann ")
	ann.send("VERSION no.such.server")
	ann.expect(" 402 ann no.such.server ")

	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	op.send("STATS l")
	op.expect(" 211 op leaf.test.local ")
	op.send("STATS c")
	op.expect(" 213 op C ")
	op.send("SQUIT leaf.test.local testing")
	waitLink(t, "leaf split", func() bool { return hub.user("ann") == nil })
	op.send("CONNECT leaf.test.local")
	op.expect("connects to us; it has no host")
	waitBack(t, leaf, "leaf back", func() bool { return hub.user("ann") != nil })
}

// A K-line holds on this server only.
func TestKline(t *testing.T) {
	hub, leaf := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	victim := login(t, hub.s, "victim")
	far := login(t, leaf.s, "far") // same host, other server
	hub.seen(t, "far")
	vh := hub.user("victim").RealHost
	op.send("KLINE +1h victim go away")
	op.expect("K-line on *@")
	victim.expect(" 465 ")
	victim.expect("ERROR")
	op.send("STATS k")
	op.expect(" 216 op K ")
	again := raw(t, hub.s)
	again.nick = "again"
	again.send("NICK again")
	again.send("USER again 0 * :Again")
	if l := again.expect(" 465 "); !strings.Contains(l, "go away") {
		t.Errorf("refused with %q", l)
	}
	far.send("PING :still")
	far.expect("PONG")
	if hub.user("far") == nil {
		t.Error("the K-line reached the leaf")
	}
	op.send("UNKLINE *@" + vh)
	op.expect("K-line on *@" + strings.ToLower(vh) + " removed.")
	back := login(t, hub.s, "back")
	back.send("PING :ok")
	back.expect("PONG")
}

// netState is everyone and every channel with members and ranks, as one
// server sees it.
func (n *linkNode) netState() string {
	var out []string
	n.s.onLoop(func() {
		for _, u := range n.s.net.Users() {
			out = append(out, "U "+u.UID+" "+u.Nick)
		}
		for _, ch := range n.s.net.Channels() {
			for uid, m := range ch.Members {
				out = append(out, "C "+ch.Name+" "+uid+" "+rankLetters(m.Ranks))
			}
		}
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// After a split both sides change things; when the link is back both
// servers agree on every user, channel, member and rank.
func TestLinkRejoinSettles(t *testing.T) {
	hub, leaf := linkedPair(t)
	a := login(t, hub.s, "alpha")
	b := login(t, leaf.s, "beta")
	a.send("JOIN #both")
	b.send("JOIN #both")
	a.expect(":beta!")
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			hub.s.dropLink(l, "test split")
		}
	})
	waitLink(t, "split", func() bool { return hub.user("beta") == nil && leaf.user("alpha") == nil })
	// While split: the same new nick and a new channel on both sides.
	login(t, hub.s, "twin").send("JOIN #new")
	login(t, leaf.s, "twin").send("JOIN #new")
	b.send("JOIN #leafonly")
	time.Sleep(200 * time.Millisecond)
	waitBack(t, leaf, "rejoin", func() bool { return hub.user("beta") != nil && leaf.user("alpha") != nil })
	waitLink(t, "same state on both", func() bool { return hub.netState() == leaf.netState() })
}

// KLINE takes InspIRCd's forms: KLINE <mask> <duration> :<reason> sets a
// timed K-line, KLINE <mask> alone and KLINE -<mask> remove it.
func TestKlineInspForms(t *testing.T) {
	s, _, _ := fileServer(t, "")
	op := boss(t, s)
	op.send("KLINE *@x.example 1h :spamming")
	op.expect("K-line on *@x.example added for 1 hour.")
	var exp time.Time
	s.onLoop(func() {
		for _, b := range s.svc.Klines() {
			if b.Mask == "*@x.example" {
				exp = b.Expires
				if b.Reason != "spamming" {
					t.Errorf("reason %q", b.Reason)
				}
			}
		}
	})
	if d := time.Until(exp); d < 59*time.Minute || d > time.Hour {
		t.Errorf("expires in %v, want about 1h", d)
	}
	op.send("KLINE *@x.example")
	op.expect("K-line on *@x.example removed.")
	op.send("KLINE *@y.example")
	op.expect("No such K-line: *@y.example.")
	op.send("KLINE *@z.example 0 :forever")
	op.expect("K-line on *@z.example added until removed.")
	op.send("KLINE -*@z.example")
	op.expect("K-line on *@z.example removed.")
	s.onLoop(func() {
		if n := len(s.svc.Klines()); n != 0 {
			t.Errorf("%d K-lines left: %v", n, s.svc.Klines())
		}
	})
}
