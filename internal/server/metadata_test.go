package server

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
)

func mdClient(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := capLogin(t, s, nick, "batch", metadataCap)
	return c
}

// mdUnrate forgets everyone's recent SETs (the rate limit).
func mdUnrate(s *Server) {
	s.do(func() {
		for _, c := range s.clients {
			c.mdSets = nil
		}
	})
}

func TestMetadataCap(t *testing.T) {
	s := testServer(t, draftsOn)
	c := raw(t, s)
	c.send("CAP LS 302")
	first := c.expect("CAP * LS * :") // too long for one line now
	if len(first) > 512 {
		t.Errorf("LS line is %d bytes", len(first))
	}
	if l := c.expect("CAP * LS :"); !strings.Contains(first+l, "draft/metadata-2=max-subs=20,max-keys=20,max-value-bytes=300") {
		t.Errorf("cap value missing: %s / %s", first, l)
	}
	off := testServer(t, nil)
	o := raw(t, off)
	o.send("CAP LS 302")
	if l := o.expect("CAP * LS :"); strings.Contains(l, metadataCap) {
		t.Errorf("metadata offered with the drafts off: %s", l)
	}
	plain := login(t, s, "plain")
	plain.send("METADATA * LIST")
	plain.expect(" 421 plain METADATA ")
}

func TestMetadataSubcommands(t *testing.T) {
	s := testServer(t, func(c *config.Config) { draftsOn(c); c.Censor.Words = []string{"darn"} })
	bee := mdClient(t, s, "Bee")

	bee.send("METADATA * SET color #ffcc00")
	bee.expect(" 761 Bee Bee color * :#ffcc00")
	bee.send("METADATA * GET color status")
	bee.expect("BATCH +")
	bee.expect(" 761 Bee Bee color * :#ffcc00")
	bee.expect(" 766 Bee Bee status :key not set")
	bee.expect("BATCH -")
	bee.send("METADATA * SET status :out for lunch")
	bee.expect(" 761 Bee Bee status * :out for lunch")
	bee.send("METADATA Bee LIST")
	bee.expect(" 761 Bee Bee color * :#ffcc00")
	bee.expect(" 761 Bee Bee status * :out for lunch")

	// Key and value checks.
	for _, tc := range []struct{ line, want string }{
		{"METADATA * SET Bad!key x", "FAIL METADATA KEY_INVALID Bad!key :Invalid key"},
		{"METADATA * SET foo bar", "FAIL METADATA KEY_NO_PERMISSION Bee foo :"},
		{"METADATA * SET avatar http://example.com/a.png", "FAIL METADATA VALUE_INVALID :Value must be an https:// address"},
		{"METADATA * SET homepage example.com", "FAIL METADATA VALUE_INVALID :Value must be an https:// address"},
		{"METADATA * SET color red", "FAIL METADATA VALUE_INVALID :Value must be a colour"},
		{"METADATA * SET status :well darn it", "FAIL METADATA VALUE_INVALID :Value contains a word"},
		{"METADATA * SET pronouns :" + strings.Repeat("x", 301), "FAIL METADATA VALUE_INVALID :Value is too long"},
		{"METADATA * SET pronouns", "FAIL METADATA KEY_NOT_SET Bee pronouns :Key not set"},
		{"METADATA * FROB", "FAIL METADATA SUBCOMMAND_INVALID FROB :"},
		{"METADATA nobody GET color", "FAIL METADATA INVALID_TARGET nobody :"},
		{"METADATA *", " 461 Bee METADATA "},
	} {
		bee.send(tc.line)
		bee.expect(tc.want)
	}
	bee.send("METADATA * SET avatar https://example.com/a.png")
	bee.expect(" 761 Bee Bee avatar * :https://example.com/a.png")

	// SUB, UNSUB, SUBS and the limit.
	bee.send("METADATA * SUB color status")
	bee.expect(" 770 Bee color status")
	bee.send("METADATA * UNSUB status")
	bee.expect(" 771 Bee status")
	bee.send("METADATA * SUBS")
	bee.expect("BATCH +")
	bee.expect(" 772 Bee color")
	keys := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		keys = append(keys, "k"+strconv.Itoa(i))
	}
	bee.send("METADATA * SUB " + strings.Join(keys, " "))
	bee.expect("FAIL METADATA TOO_MANY_SUBS k19 :")
	bee.send("METADATA * UNSUB " + strings.Join(keys, " "))
	bee.expect(" 771 Bee k0 ")

	mdUnrate(s)

	// Pushes to subscribers that share a channel.
	bob := mdClient(t, s, "bob")
	bob.send("METADATA * SUB color")
	bob.expect(" 770 bob color")
	bee.send("JOIN #x")
	bee.expect(" 366 Bee #x ")
	bob.send("JOIN #x")
	bob.expect("BATCH +")
	bob.expect("METADATA Bee color * :#ffcc00")
	bob.expect(" 366 bob #x ")
	bee.send("METADATA * SET color #000000")
	bee.expect(" 761 Bee Bee color * :#000000")
	bob.expect(":irc.test.local METADATA Bee color * :#000000")
	bee.send("METADATA * SET status :back")
	bee.expect(" 761 Bee Bee status * :back")
	bob.quiet("METADATA Bee status")

	// Channel keys need +o.
	bob.send("METADATA #x SET color #123456")
	bob.expect("FAIL METADATA KEY_NO_PERMISSION #x color :")
	bee.send("METADATA #x SET color #123456")
	bee.expect(" 761 Bee #x color * :#123456")
	bob.expect(":irc.test.local METADATA #x color * :#123456")
	bob.send("METADATA #x SYNC")
	bob.expect("BATCH +")
	bob.expect("METADATA #x color * :#123456")
	bob.expect("METADATA Bee color * :#000000")
	bob.expect("BATCH -")

	// CLEAR: one 766 per key, and the subscribers hear of it.
	bee.send("METADATA * CLEAR")
	bee.expect(" 766 Bee Bee avatar :key not set")
	bee.expect(" 766 Bee Bee color :key not set")
	bee.expect(" 766 Bee Bee status :key not set")
	bob.expect(":irc.test.local METADATA Bee color *")
	bee.send("METADATA * LIST")
	bee.expect("BATCH +")
	if l := bee.expect("BATCH -"); l == "" {
		t.Fatal("no end of batch")
	}

	// Pushes go to MONITOR too, without a shared channel.
	ann := mdClient(t, s, "ann")
	ann.send("METADATA * SUB status")
	ann.expect(" 770 ann status")
	ann.send("MONITOR + Bee")
	ann.expect(" 730 ann ")
	bee.send("METADATA * SET status :busy")
	ann.expect(":irc.test.local METADATA Bee status * :busy")
}

func TestMetadataRateLimit(t *testing.T) {
	s := testServer(t, draftsOn)
	bee := mdClient(t, s, "Bee")
	for i := 0; i < mdSetLimit; i++ {
		bee.send("METADATA * SET status :s" + strconv.Itoa(i))
		bee.expect(" 761 Bee Bee status * :s" + strconv.Itoa(i))
	}
	bee.send("METADATA * SET status :one too many")
	bee.expect("FAIL METADATA RATE_LIMITED Bee status ")
}

func TestMetadataAccount(t *testing.T) {
	s := testServer(t, draftsOn)
	bee := mdClient(t, s, "Bee")
	bee.send("METADATA * SET pronouns :they/them") // before the account: kept on register's login
	bee.expect(" 761 Bee Bee pronouns ")
	bee.send("NS REGISTER " + pw)
	bee.expect(":irc.test.local MODE Bee +r")
	bee.send("METADATA * SET color #ffcc00")
	bee.expect(" 761 Bee Bee color * :#ffcc00")
	bee.send("QUIT")
	bee.gone()

	again := mdClient(t, s, "guest")
	again.send("METADATA * SUB color pronouns")
	again.expect(" 770 guest color pronouns")
	again.send("METADATA * SET status :not kept") // replaced by the account's keys on login
	again.expect(" 761 guest guest status ")
	again.send("NS IDENTIFY Bee " + pw)
	again.expect(":irc.test.local METADATA guest color * :#ffcc00")
	again.expect(":irc.test.local METADATA guest pronouns * :they/them")
	again.send("METADATA * LIST")
	again.expect(" 761 guest guest color * :#ffcc00")
	again.expect(" 761 guest guest pronouns * :they/them")
	again.expect("BATCH -")

	// A registered channel keeps its keys with ChanServ.
	again.send("JOIN #hive")
	again.expect(" 366 guest #hive ")
	again.send("PRIVMSG ChanServ :REGISTER #hive")
	again.expect("registered")
	again.send("METADATA #hive SET status :buzzing")
	again.expect(" 761 guest #hive status * :buzzing")
	again.send("PART #hive")
	again.expect(" PART #hive")
	again.sync()
	var kept bool
	s.do(func() {
		kept = s.net.Channel("#hive") == nil || s.net.Channel("#hive").Metadata["status"] == "buzzing"
	})
	if !kept {
		t.Error("channel lost its key while in use")
	}
	again.send("JOIN #hive")
	again.expect(" 366 guest #hive ")
	again.send("METADATA #hive GET status")
	again.expect(" 761 guest #hive status * :buzzing")

	// An unregistered channel forgets its keys when it empties.
	again.send("JOIN #tmp")
	again.expect(" 366 guest #tmp ")
	again.send("METADATA #tmp SET status :gone soon")
	again.expect(" 761 guest #tmp status ")
	again.send("PART #tmp")
	again.expect(" PART #tmp")
	again.send("JOIN #tmp")
	again.expect(" 366 guest #tmp ")
	again.send("METADATA #tmp GET status")
	again.expect(" 766 guest #tmp status :key not set")
}

func TestMetadataOperAndReadOnly(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.do(func() { s.cfg.Server.IRCv3Drafts = true })
	b := boss(t, s)
	b.send("CAP REQ :batch " + metadataCap)
	b.expect("CAP Boss ACK :batch " + metadataCap)
	vic := mdClient(t, s, "Vic")
	vic.send("METADATA * SET status :buy cheap stuff")
	vic.expect(" 761 Vic Vic status ")
	vic.send("METADATA * SET color #ff0000")
	vic.expect(" 761 Vic Vic color ")

	// Others may not; an oper may delete and clear, and it is logged.
	ann := mdClient(t, s, "Ann")
	ann.send("METADATA Vic SET status")
	ann.expect("FAIL METADATA KEY_NO_PERMISSION Vic status :")
	b.send("METADATA Vic SET status :nope")
	b.expect("FAIL METADATA KEY_NO_PERMISSION Vic status :")
	b.send("METADATA Vic SET status")
	b.expect(" 766 Boss Vic status :key not set")
	b.send("METADATA Vic CLEAR")
	b.expect("Boss cleared the metadata of Vic")
	b.expect(" 766 Boss Vic color :key not set")

	readOnlyOn(t, b)
	vic.send("METADATA * SET color #00ff00")
	vic.expect("FAIL METADATA KEY_NO_PERMISSION Vic color :Services are in read-only mode")
}

func TestLinkMetadata(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	bee := mdClient(t, hub.s, "Bee")
	bob := mdClient(t, leaf.s, "bob")
	bee.send("METADATA * SUB color")
	bee.expect(" 770 Bee color")
	hub.seen(t, "bob")
	joinAll("#x", bee, bob)
	waitLink(t, "bob in #x on the hub", func() bool {
		n := 0
		hub.s.onLoop(func() {
			if ch := hub.s.net.Channel("#x"); ch != nil {
				n = len(ch.Members)
			}
		})
		return n == 2
	})

	// A change on the leaf reaches the hub's subscribers.
	bob.send("METADATA * SET color #abcdef")
	bob.expect(" 761 bob bob color * :#abcdef")
	bee.expect(":hub.test.local METADATA bob color * :#abcdef")

	// The account lives on the hub (services): a leaf user's keys are kept.
	bob.send("NS REGISTER " + pw)
	bob.expect(" MODE bob +r")
	bob.send("METADATA * SET status :from the leaf")
	bob.expect(" 761 bob bob status ")
	waitLink(t, "bob's status kept on the hub", func() bool {
		var v string
		hub.s.onLoop(func() {
			if a, _ := hub.s.store.AccountByNick("bob"); a != nil {
				v = hub.s.store.AccountMetadata(a.ID)["status"]
			}
		})
		return v == "from the leaf"
	})

	// Channel keys go both ways.
	bee.send("METADATA #x SET color #010203")
	bee.expect(" 761 Bee #x color ")
	waitLink(t, "#x color on the leaf", func() bool {
		var v string
		leaf.s.onLoop(func() {
			if ch := leaf.s.net.Channel("#x"); ch != nil {
				v = ch.Metadata["color"]
			}
		})
		return v == "#010203"
	})
}

// The Hive Panel: a member edits their own keys (My nick), an oper clears
// a person's or a channel's.
func TestPanelMetadata(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	e := registered(t, s, "ember")
	m := memberSession(t, s, "ember", pw)
	myMeta := func(key, value string) *control.Response {
		q := as(m, control.CmdMyMeta)
		q.Name, q.Value = key, value
		return panelAsk(t, s, q)
	}
	if r := myMeta("color", "#ff0000"); !strings.Contains(r.Error, "off") {
		t.Errorf("my-meta with the drafts off: %+v", r)
	}
	if my := panelAsk(t, s, as(m, control.CmdMyNick)).MyNick; my == nil || my.MetaOn {
		t.Errorf("my-nick with the drafts off: %+v", my)
	}
	s.do(func() { s.cfg.Server.IRCv3Drafts = true })

	if r := myMeta("color", "#ff0000"); r.Done == nil || r.Done.Text != "Your color is saved." {
		t.Fatalf("my-meta color: %+v", r)
	}
	for _, bad := range [][2]string{{"color", "red"}, {"avatar", "http://example.org/a.png"}, {"shoe-size", "9"}} {
		if r := myMeta(bad[0], bad[1]); r.Error == "" {
			t.Errorf("my-meta %s %s: %+v", bad[0], bad[1], r)
		}
	}
	if r := myMeta("status", ""); r.Done == nil || r.Done.Text != "Your status is not set." {
		t.Errorf("removing a key not set: %+v", r)
	}
	my := panelAsk(t, s, as(m, control.CmdMyNick)).MyNick
	if my == nil || !my.MetaOn || my.Metadata["color"] != "#ff0000" {
		t.Fatalf("my-nick: %+v", my)
	}
	var uid, live string
	s.onLoop(func() {
		if u := s.net.UserByNick("ember"); u != nil {
			uid, live = u.UID, u.Metadata["color"]
		}
	})
	if live != "#ff0000" {
		t.Errorf("ember online has color %q", live)
	}

	// Clearing is for opers; members get no say.
	if r := panelAsk(t, s, func() control.Request { q := as(m, control.CmdMetaClear); q.UID = uid; return q }()); r.Denied != "flag" {
		t.Errorf("meta-clear by a member: %+v", r)
	}
	boss(t, s)
	op := control.Request{As: "Boss", IP: "127.0.0.1"}
	clear := func(uid, name string) *control.Response {
		q := as(op, control.CmdMetaClear)
		q.UID, q.Name = uid, name
		return panelAsk(t, s, q)
	}
	if r := clear(uid, ""); r.Done == nil || r.Done.Text != "The metadata of ember is cleared." {
		t.Fatalf("meta-clear ember: %+v", r)
	}
	if my := panelAsk(t, s, as(m, control.CmdMyNick)).MyNick; my == nil || len(my.Metadata) != 0 {
		t.Errorf("ember's account still has %+v", my)
	}
	if r := clear(uid, ""); r.Done == nil || r.Done.Text != "ember has no metadata." {
		t.Errorf("meta-clear again: %+v", r)
	}

	// A channel: live keys, and a registered one with none.
	e.send("JOIN #kampung")
	e.expect(" 366 ember #kampung ")
	e.send("PRIVMSG ChanServ :REGISTER #kampung")
	e.expect("#kampung is registered")
	ann := mdClient(t, s, "Ann")
	ann.send("JOIN #open")
	ann.expect(" 366 Ann #open ")
	ann.send("METADATA #open SET color #00ff00")
	ann.expect(" 761 Ann #open color ")
	if r := clear("", "#open"); r.Done == nil || r.Done.Text != "The metadata of #open is cleared." {
		t.Errorf("meta-clear #open: %+v", r)
	}
	if r := clear("", "#kampung"); r.Done == nil || r.Done.Text != "#kampung has no metadata." {
		t.Errorf("meta-clear #kampung: %+v", r)
	}
	if r := clear("", "#nowhere"); r.Error != "No such channel." {
		t.Errorf("meta-clear #nowhere: %+v", r)
	}
}
