package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// The network ban engine, IRCd-style ban commands, extbans
// and timed bans.

// addBan puts a ban straight into the store (for masks that would match
// the oper adding it: every test client is 127.0.0.1).
func addBan(s *Server, b store.NetBan) {
	done := make(chan struct{})
	s.post(func() {
		b.SetBy, b.SetAt = "test", time.Now()
		s.store.AddNetBan(b)
		s.svc.ReloadBans()
		close(done)
	})
	<-done
}

func TestZlineAndEline(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	addBan(s, store.NetBan{Kind: store.BanZline, Mask: "127.0.0.0/8", Reason: "flood"})
	c := raw(t, s)
	c.expect("ERROR :Closing Link: 127.0.0.1 (Z-lined: flood)")
	b.expect("*** XLINE: Z-line 127.0.0.0/8 stopped a connection from 127.0.0.1")

	b.send("OS EXCEPTION BAN ADD *@127.0.0.1 the test box")
	b.expect(osLine("Boss", "ELINE added: *@127.0.0.1 until removed (the test box). 1 person exempt online now."))
	login(t, s, "fine") // ELINEs lift ZLINEs too
	b.send("OS EXCEPTION BAN LIST")
	b.expect(osLine("Boss", "ELINEs:"))
	b.expect("*@127.0.0.1  by Boss · until removed · the test box")
	b.send("OS ZLINE LIST")
	b.expect("127.0.0.0/8  by test · until removed · flood")
	b.send("OS ZLINE ADD 300.1.2.3 nonsense")
	b.expect(osLine("Boss", "A ZLINE is an IP, like 203.0.113.5, 203.0.113.* or 203.0.113.0/24 (or the nick of someone online)."))
	b.send("OS ZLINE ADD 10.0.0.0/33 bad range")
	b.expect(osLine("Boss", "10.0.0.0/33 isn't a CIDR range. One looks like 203.0.113.0/24 or 2001:db8::/32."))
	b.send("OS ZLINE ADD 0::/0 everyone")
	b.expect(osLine("Boss", "That mask matches everyone. Give a narrower one."))
	b.send("OS ZLINE ADD 0.0.0.0/0 everyone")
	b.expect(osLine("Boss", "That mask matches everyone. Give a narrower one."))
	b.send("OS ZLINE DEL 1")
	b.expect(osLine("Boss", "ZLINE on 127.0.0.0/8 removed."))
	b.send("OS EXCEPTION BAN CLEAR")
	b.expect(osLine("Boss", "Every ELINE is removed (1)."))
}

func TestAkillCIDRShunSNLine(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bad := login(t, s, "bad")
	sh := login(t, s, "sh")
	ev := login(t, s, "evil")
	watcher := login(t, s, "watcher")
	joinAll("#room", sh, watcher)

	// AKILL with a CIDR host takes effect at once.
	b.send("OS AKILL ADD +1h ~bad@127.0.0.0/8 bots")
	bad.expect(" 465 bad :You are banned from this network: bots")
	b.expect(osLine("Boss", "AKILL added: ~bad@127.0.0.0/8 for 1 hour (bots). 1 person removed."))
	again := raw(t, s)
	again.send("NICK bad")
	again.send("USER bad 0 * :bad Real")
	again.expect(" 465 bad :You are banned from this network: bots")
	b.expect("*** XLINE: G-line ~bad@127.0.0.0/8 stopped bad")

	// SHUN: still connected, but only PART, QUIT and PING work.
	b.send("OS SHUN ADD ~sh@* noisy")
	b.expect(osLine("Boss", "SHUN added: ~sh@* for 30 days (noisy). 1 person shunned."))
	sh.send("PRIVMSG #room :can anyone hear me")
	sh.send("JOIN #other")
	sh.send("NICK shh")
	sh.sync()
	watcher.quiet("can anyone hear me")
	sh.send("PART #room :bye all")
	watcher.expect(":sh!~sh@127.0.0.1 PART #room")
	watcher.quiet("bye all")
	b.send("OS SHUN DEL ~sh@*")
	b.expect(osLine("Boss", "SHUN on ~sh@* removed."))
	sh.send("JOIN #room")
	watcher.expect(":sh!~sh@127.0.0.1 JOIN #room")

	// SNLINE: a realname glob (spaces as ?) and a /regex/.
	b.send("OS SNLINE ADD evil?Real spam realname")
	ev.expect(" 465 evil :You are banned from this network: spam realname")
	b.expect("SNLINE added: evil?Real for 30 days (spam realname). 1 person removed.")
	b.send(`OS SNLINE ADD /^w.tcher\sreal$/ regex test`) // a mask is one word: \s for spaces
	watcher.expect(" 465 watcher ")
	b.send("OS SNLINE ADD /([/ broken")
	b.expect(osLine("Boss", "That pattern isn't a valid regular expression: "))
	b.send("OS SNLINE ADD Boss match me")
	b.expect(osLine("Boss", "Boss?Real matches you. Give a narrower one."))
	b.send("OS STATS")
	b.expect(osLine("Boss", "  AKILLs     1"))
	b.expect(osLine("Boss", "  Other bans ZLINE 0, SNLINE 2, SHUN 0, ELINE 0"))
}

func TestXlineCommandsAndSQLine(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	plain := login(t, s, "plain")
	plain.send("GLINE *@spam.example 1d spam")
	plain.expect(" 481 plain :Permission denied: you're not an IRC operator")

	b.send("GLINE ~g@203.0.113.0/24 2h spam bots")
	b.expect(osLine("Boss", "AKILL added: ~g@203.0.113.0/24 for 2 hours (spam bots). 0 people removed."))
	b.send("GLINE")
	b.expect(osLine("Boss", "AKILLs:"))
	b.expect("~g@203.0.113.0/24  by Boss · ")
	b.send("GLINE -~g@203.0.113.0/24")
	b.expect(osLine("Boss", "AKILL on ~g@203.0.113.0/24 removed."))
	b.send("ZLINE 198.51.100.0/24 0 permanent flood")
	b.expect(osLine("Boss", "ZLINE added: 198.51.100.0/24 until removed (permanent flood). 0 people removed."))
	b.send("SHUN ~loud@* 3600 too loud")
	b.expect(osLine("Boss", "SHUN added: ~loud@* for 1 hour (too loud). 0 people shunned."))
	b.send("GLINE *@five.example 5s quick")
	b.expect(osLine("Boss", "AKILL added: *@five.example for 5 seconds (quick). 0 people removed."))
	b.send("GLINE *@mixed.example 2m30s mixed")
	b.expect(osLine("Boss", "AKILL added: *@mixed.example for 2 minutes and 30 seconds (mixed). 0 people removed."))
	b.send("OS AKILL ADD +1d12h *@combo.example combo")
	b.expect(osLine("Boss", "AKILL added: *@combo.example for 1 day and 12 hours (combo). 0 people removed."))
	b.send("RLINE *bitcoin* crypto spam")
	b.expect(osLine("Boss", "SNLINE added: *bitcoin* for 30 days (crypto spam)."))
	b.send("ELINE ~me@127.0.0.1 me")
	b.expect(osLine("Boss", "ELINE added: ~me@127.0.0.1 until removed (me)."))

	// QLINE / SQLINE is the FORBID NICK list.
	badger := login(t, s, "Badger")
	b.send("QLINE Bad* 1h reserved for staff")
	badger.expect("That nick is forbidden by the network, so you are now Guest")
	b.expect(osLine("Boss", "Bad* is forbidden now."))
	b.send("OS FORBID LIST NICK")
	l := b.expect("NICK     Bad*")
	if !strings.Contains(l, "minutes left · reserved for staff") {
		t.Errorf("forbid without its expiry: %q", l)
	}
	b.send("OS SQLINE DEL Bad*")
	b.expect(osLine("Boss", "Bad* can be used again."))
	b.send("OS FORBID ADD CHAN +1d #warez* piracy")
	b.expect(osLine("Boss", "#warez* is forbidden now (0 people kicked)."))
	b.send("OS EXCEPTION ADD +2h 10.0.0.0/8 30 lab")
	b.expect(osLine("Boss", "10.0.0.0/8 may now have 30 connections."))
	b.send("OS EXCEPTION ADD 10.0.0.0/99 30 lab")
	b.expect(osLine("Boss", "Syntax: EXCEPTION ADD [+expiry] <ip-mask> <limit> [reason], like 192.168.50.* 20"))
}

func TestExtbansAndTimedBans(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	mira := registered(t, s, "mira")
	x := login(t, s, "x")

	bee.send("MODE #bchat +b ~a:mira")
	bee.expect("MODE #bchat +b ~a:mira")
	mira.send("JOIN #bchat")
	mira.expect(" 474 mira #bchat :Cannot join channel (+b)")
	bee.send("MODE #bchat -b ~a:mira")
	bee.expect("MODE #bchat -b ~a:mira")
	bee.send("MODE #bchat +b ~a:0")
	bee.expect("+b ~a:0")
	x.send("JOIN #bchat")
	x.expect(" 474 x #bchat ")
	joinAll("#bchat", mira) // identified: ~a:0 doesn't match
	bee.send("MODE #bchat -b ~a:0")
	bee.expect("-b ~a:0")

	// Mute: may join, may not talk; an exception lifts it.
	bee.send("MODE #bchat +b ~m:x!*@*")
	bee.expect("+b ~m:x!*@*")
	joinAll("#bchat", x)
	x.send("PRIVMSG #bchat :hello?")
	x.expect(" 404 x #bchat :Cannot send to channel (you're banned)")
	mira.quiet("hello?")
	bee.send("MODE #bchat +e ~r:x?Real")
	bee.expect("+e ~r:x?Real")
	x.send("PRIVMSG #bchat :now?")
	mira.expect("PRIVMSG #bchat :now?")
	bee.send("MODE #bchat +b ~q:nobody!*@*")
	bee.expect("+b ~q:nobody!*@*")

	bee.send("MODE #bchat +b ~z:what")
	bee.expect(" 696 Bee #bchat b ~z:what :Invalid ban mask")
	bee.send("VERSION")
	bee.expect("EXTBAN=~,ACFGOSTacfijmnpqrt")

	// Timed bans lift themselves.
	bee.send("MODE #bchat +b ~t:1:gone!*@*")
	bee.expect("+b ~t:1:gone!*@*")
	done := make(chan struct{})
	s.post(func() { s.expireTimedBans(time.Now().Add(2 * time.Minute)); close(done) })
	<-done
	bee.expect(":irc.test.local MODE #bchat -b ~t:1:gone!*@*")

	// AKICK takes extbans.
	bee.send("CS AKICK #bchat ADD ~a:mira not you")
	mira.expect(csSrc + "KICK #bchat mira :not you")
	bee.send("CS AKICK #bchat ADD ~z:bad")
	bee.expect(csLine("Bee", "~z:bad isn't a mask or extban (~a:account, ~r:realname)."))

	// CS BAN +expiry is a ~t: ban (every test client shares 127.0.0.1).
	bee.send("CS BAN #bchat +2h x go cool off")
	x.expect(csSrc + "MODE #bchat +b ~t:120:*!*@127.0.0.1")
	x.expect(csSrc + "KICK #bchat x :go cool off") // the founder's kicks aren't signed
	bee.send("CS BAN #bchat +soon x")
	bee.expect(csLine("Bee", "+soon isn't a time. Use +30m, +12h, +7d."))
}

// A timed ban's expiry notice comes within seconds, not at the next
// hourly clean-up.
func TestBanExpiresPromptly(t *testing.T) {
	// A short client tick, as the default one can be 15 s.
	s, _, _ := fileServerWith(t, "", func(s *Server) { s.cfg.Limits.PingInterval.Duration = 2 * time.Second })
	b := boss(t, s)
	b.send("GLINE *@soon.example 1s gone soon")
	b.expect("AKILL added: *@soon.example for 1 second")
	b.expect("Removing an expired G-line on *@soon.example (set by Boss")
}

// SHUN -nick (and GLINE/ZLINE -nick) remove the ban that adding by that
// nick made.
func TestNetBanDelByNick(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	login(t, s, "noisy")
	// Not the oper's own address, or the ban would cover them too.
	s.onLoop(func() { s.net.UserByNick("noisy").IP = "192.0.2.7" })
	b.send("SHUN noisy 1h too loud")
	b.expect(osLine("Boss", "SHUN added: *@192.0.2.7 for 1 hour (too loud)."))
	b.send("SHUN -noisy")
	b.expect(osLine("Boss", "SHUN on *@192.0.2.7 removed."))
	b.send("SHUN -noisy")
	b.expect(osLine("Boss", "No SHUN on noisy."))
	// A nick that is gone (a G-line disconnects them) can't be resolved.
	b.send("GLINE -ghost")
	b.expect(osLine("Boss", "No AKILL on ghost. ghost isn't online, so remove it by its mask. See them all: /os akill list"))
	b.send("GLINE -*@198.51.100.9")
	b.expect(osLine("Boss", "No AKILL on *@198.51.100.9. See them all: /os akill list"))
}

// A ban an oper sets or lifts through OperServ reads like InspIRCd 4's own
// notice, in place of "used OperServ".
func TestXlineNoticeWording(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("GLINE *@words.example 1h qa gline")
	if l := b.expect("*** XLINE: Boss added a timed G-line on *@words.example, expires in 1 hour (on "); !strings.HasSuffix(l, "): qa gline") {
		t.Errorf("notice %q", l)
	}
	b.send("GLINE *@perm.example 0 forever")
	b.expect("*** XLINE: Boss added a permanent G-line on *@perm.example: forever")
	b.send("GLINE -*@words.example")
	b.expect("*** XLINE: Boss removed G-line on *@words.example")
	b.send("ZLINE 198.51.100.0/24 0 flood")
	b.expect("*** XLINE: Boss added a permanent Z-line on 198.51.100.0/24: flood")
	b.send("SHUN ~loud@* 3600 too loud")
	b.expect("*** XLINE: Boss added a timed SHUN on ~loud@*, expires in 1 hour (on ")
}

// A ZLINE is kept one way however it was typed (2001:db8::1), so it
// matches the IP the server checks, and DEL finds it typed either way.
func TestZlineKeptOneWay(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	login(t, s, "loopy")
	s.onLoop(func() { s.net.UserByNick("loopy").IP = "2001:db8::7" })
	b.send("OS ZLINE ADD 2001:DB8:0:0:0:0:0:7 by hand")
	b.expect(osLine("Boss", "ZLINE added: 2001:db8::7 for "))
	var hit bool
	s.onLoop(func() { hit = s.svc.ConnectBan("x", "h", "2001:db8::7", "r", false) != nil })
	if !hit {
		t.Error("ZLINE typed in full form doesn't match 2001:db8::7")
	}
	b.send("OS ZLINE DEL 2001:db8:0:0:0:0:0:7")
	b.expect(osLine("Boss", "ZLINE on 2001:db8::7 removed."))
}
