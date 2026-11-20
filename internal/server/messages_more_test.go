package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// quit_prefix replaces "Quit: " word for word (its space too), "none"
// drops it, and AdminServ sets it with quotes; DEFAULT puts "Quit: " back.
func TestQuitPrefix(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nquit_prefix = \"Exit: \"\n")
	w := login(t, s, "watch")
	joinRoom(w)
	ann := login(t, s, "ann")
	joinRoom(ann)
	ann.send("QUIT :bye")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Exit: bye") {
		t.Errorf("with Exit: %q", l)
	}
	b := boss(t, s)
	b.send("AS SET quit-prefix none")
	b.expect(`quit-prefix is now none (was "Exit: ")`)
	bob := login(t, s, "bob")
	joinRoom(bob)
	bob.send("QUIT :bye")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :bye") || strings.Contains(l, "Quit") {
		t.Errorf("with none: %q", l)
	}
	b.send(`AS SET quit-prefix "Gone: "`)
	b.expect(`quit-prefix is now "Gone: " (was none)`)
	cy := login(t, s, "cy")
	joinRoom(cy)
	cy.send("QUIT :later")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Gone: later") {
		t.Errorf("after SET: %q", l)
	}
	b.send("AS SET quit-prefix default")
	b.expect(`quit-prefix is now "Quit: " (was "Gone: ")`)
	s.onLoop(func() {
		if s.cfg.Messages.QuitPrefix != "" {
			t.Errorf("quit_prefix after DEFAULT: %q", s.cfg.Messages.QuitPrefix)
		}
	})
}

// reject_full and reject_too_many replace the texts people refused at
// connect get; left out, the built-in ones stay.
func TestRejectTexts(t *testing.T) {
	for _, own := range []bool{false, true} {
		s := testServer(t, func(c *config.Config) {
			c.Limits.MaxUsers = 1
			c.Limits.SessionLimit = 2
			if own {
				c.Messages.RejectFull = "No room, sorry"
				c.Messages.RejectTooMany = "One is enough"
			}
		})
		full, many := "This server is full. Please try again later.", "Too many connections from your IP"
		if own {
			full, many = "No room, sorry", "One is enough"
		}
		login(t, s, "first")
		late := raw(t, s)
		late.send("NICK late")
		late.send("USER late 0 * :Late")
		late.expect("ERROR :Closing Link: 127.0.0.1 (" + full + ")")
		hold := raw(t, s) // the second of two the IP may have
		hold.send("NICK hold")
		third := raw(t, s)
		third.expect("ERROR :Closing Link: 127.0.0.1 (" + many + ")")
	}
}

// reject_password is the answer to a wrong /OPER password, and the oper
// notice still names the wrong password; AdminServ SET reject-password
// and DEFAULT work.
func TestRejectPassword(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nreject_password = \"Nope, wrong one\"\n")
	b := boss(t, s)
	ember := login(t, s, "ember")
	ember.send("OPER Boss guess-the-pass")
	ember.expect(" 464 ember :Nope, wrong one")
	b.expect("OPER: ember (~ember@127.0.0.1) [127.0.0.1] failed to log into the \x02Boss\x02 oper account because they specified the wrong password.")
	b.send("AS SET reject-password default")
	b.expect("reject-password is now Password incorrect (was Nope, wrong one)")
	ember.send("OPER Boss guess-again")
	ember.expect(" 464 ember :Password incorrect")
}

// A relayed /OPER answer is read the same with the built-in or the own
// wrong-password text.
func TestOperFailWhyOwnText(t *testing.T) {
	const want = "they specified the wrong password"
	for _, answer := range []string{"Password incorrect", "Nope"} {
		if got := operFailWhy(answer, "Nope"); got != want {
			t.Errorf("operFailWhy(%q) = %q", answer, got)
		}
	}
	if got := operFailWhy("Password incorrect", "Password incorrect"); got != want {
		t.Errorf("default: %q", got)
	}
}

// ban_notice goes to the banned person first, ban_quit "yes" hides the
// reason from others while the person still sees it.
func TestBanNoticeAndQuit(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nban_notice = \"Read the rules at example.net\"\nban_quit = \"yes\"\n")
	w := login(t, s, "watch")
	joinRoom(w)
	b := boss(t, s)
	guest := login(t, s, "guest")
	joinRoom(guest)
	w.expect(":guest!")
	b.send("KLINE +1h *guest@127.0.0.1 spam")
	guest.expect(" 465 guest ")
	guest.expect(":irc.test.local NOTICE guest :Read the rules at example.net")
	guest.expect("ERROR :Closing Link: 127.0.0.1 (K-Lined: spam)")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :K-Lined") {
		t.Errorf("others with yes: %q", l)
	}

	// At connect: a text replaces the reason; the notice goes to their nick.
	b.send("AS SET ban-quit Removed from the network")
	b.expect("ban-quit is now Removed from the network (was yes)")
	again := raw(t, s)
	again.nick = "again"
	again.send("NICK again")
	again.send("USER guest 0 * :Again")
	again.expect(":irc.test.local NOTICE again :Read the rules at example.net")
	again.expect("ERROR :Closing Link: 127.0.0.1 (Banned: spam)")

	b.send("AS SET ban-notice off")
	b.expect("ban-notice is now off (was Read the rules at example.net)")
	b.send("AS SET ban-quit off")
	b.expect("ban-quit is now off (was Removed from the network)")
	third := raw(t, s)
	third.nick = "third"
	third.send("NICK third")
	third.send("USER guest 0 * :Third")
	third.expect(" 465 ")
	if l := third.expect("ERROR "); l != "ERROR :Closing Link: 127.0.0.1 (Banned: spam)" {
		t.Errorf("after OFF: %q", l)
	}
}

// With ban_quit a text, others see only that text at a K-line; with the
// defaults the quit is unchanged and no notice is sent.
func TestBanQuitTextAndDefault(t *testing.T) {
	for _, own := range []bool{false, true} {
		extra := ""
		if own {
			extra = "\n[messages]\nban_quit = \"Removed\"\n"
		}
		s, _, _ := fileServer(t, extra)
		w := login(t, s, "watch")
		joinRoom(w)
		b := boss(t, s)
		guest := login(t, s, "guest")
		joinRoom(guest)
		w.expect(":guest!")
		b.send("KLINE +1h *guest@127.0.0.1 spam")
		guest.expect(" 465 guest ")
		if l := guest.expect("ERROR "); strings.Contains(l, "NOTICE") || l != "ERROR :Closing Link: 127.0.0.1 (K-Lined: spam)" {
			t.Errorf("own %v: the banned user got %q", own, l)
		}
		want := "QUIT :K-Lined: spam"
		if own {
			want = "QUIT :Removed"
		}
		if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), want) {
			t.Errorf("own %v: others saw %q", own, l)
		}
	}
}

// hide_killed_by shows its name instead of the oper to everyone, the
// victim too; the oper notice keeps the real killer.
func TestHideKilledBy(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nhide_killed_by = \"OperServ\"\n")
	b := boss(t, s)
	w := login(t, s, "watch")
	joinRoom(w)
	victim := login(t, s, "victim")
	joinRoom(victim)
	w.expect(":victim!")
	b.send("KILL victim :go away")
	victim.expect(":irc.test.local KILL victim :go away")
	victim.expect("ERROR :Closing Link: 127.0.0.1 (Killed (OperServ (go away)))")
	b.expect("KILL: Local kill by Boss: victim!~victim@127.0.0.1 (go away)")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Killed (OperServ (go away))") {
		t.Errorf("others saw %q", l)
	}
	s.onLoop(func() {
		if got := s.killedBy(s.cfg.Server.Name); got != s.cfg.Server.Name {
			t.Errorf("a server kill shows %q", got)
		}
	})
	b.send("AS SET hide-killed-by off")
	b.expect("hide-killed-by is now off (was OperServ)")
	v2 := login(t, s, "v2")
	b.send("KILL v2 :bye")
	v2.expect(":Boss KILL v2 :bye")
	v2.expect("ERROR :Closing Link: 127.0.0.1 (Killed (Boss (bye)))")
}

// hide_killed_by works on both sides of a link: the killer's server shows
// it in the quit, and the victim's server in its KILL and ERROR.
func TestHideKilledByLinked(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.onLoop(func() { n.s.cfg.Messages.HideKilledBy = "OperServ" })
	}
	alice := login(t, hub.s, "alice")
	bob := login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	leaf.seen(t, "alice")
	alice.send("JOIN #x")
	alice.expect("366")
	waitLink(t, "the leaf knows #x", func() bool {
		known := false
		leaf.s.onLoop(func() { known = leaf.s.net.Channel("#x") != nil })
		return known
	})
	bob.send("JOIN #x")
	bob.expect("366")
	alice.expect(":bob!~bob@")
	hub.s.onLoop(func() { hub.s.killUser(hub.s.net.UserByNick("bob"), "alice", "enough") })
	bob.expect(":leaf.test.local KILL bob :enough")
	bob.expect("ERROR :Closing Link: 127.0.0.1 (Killed (OperServ (enough)))")
	if l := alice.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Killed (OperServ (enough))") {
		t.Errorf("the hub saw %q", l)
	}
}
