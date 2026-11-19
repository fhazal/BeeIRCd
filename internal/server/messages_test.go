package server

import (
	"strings"
	"testing"
)

// joinRoom joins #room and waits for the names list.
func joinRoom(c *tc) {
	c.t.Helper()
	c.send("JOIN #room")
	c.expect(" 366 " + c.nick + " #room ")
}

// static_quit replaces the quit reason of people but not of opers, and
// server-made quits keep theirs.
func TestStaticQuit(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nstatic_quit = \"  Client exited \"\n")
	w := login(t, s, "watch")
	joinRoom(w)
	b := boss(t, s)
	joinRoom(b)
	ann := login(t, s, "ann")
	joinRoom(ann)
	ann.send("QUIT :visit my site")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Client exited") {
		t.Errorf("ann's quit: %q", l)
	}
	bob := login(t, s, "bob")
	joinRoom(bob)
	bob.send("QUIT")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Client exited") {
		t.Errorf("bob's quit: %q", l)
	}
	b.send("QUIT :back soon")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Quit: back soon") {
		t.Errorf("the oper's quit: %q", l)
	}
}

// quit_message_min_time drops the reason of someone who just came.
func TestQuitMessageMinTime(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nquit_message_min_time = \"1h\"\n")
	w := login(t, s, "watch")
	joinRoom(w)
	sp := login(t, s, "spammer")
	joinRoom(sp)
	sp.send("QUIT :buy cheap stuff at example.com")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Quit") {
		t.Errorf("early quit: %q", l)
	}
	b := boss(t, s)
	b.send("AS SET quit-message-min-time off")
	b.expect("quit-message-min-time is now off (was 1h)")
	ok := login(t, s, "fine")
	joinRoom(ok)
	ok.send("QUIT :bye")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Quit: bye") {
		t.Errorf("quit after OFF: %q", l)
	}
	b.send("AS SET quit-message-min-time 2h")
	b.expect("quit-message-min-time takes a time up to 1h")
}

// static_part "yes" drops part reasons, a text replaces them, opers keep
// theirs; set live with AdminServ.
func TestStaticPart(t *testing.T) {
	s, _, _ := fileServer(t, "")
	w := login(t, s, "watch")
	joinRoom(w)
	b := boss(t, s)
	joinRoom(b)
	b.send("AS SET static-part yes")
	b.expect("static-part is now yes (was off)")
	ann := login(t, s, "ann")
	joinRoom(ann)
	ann.send("PART #room :visit my site")
	if l := w.expect(":ann!~ann@127.0.0.1 PART "); strings.Contains(l, "visit") || !strings.Contains(l, "PART #room") {
		t.Errorf("part with yes: %q", l)
	}
	b.send("AS SET static-part Left the room")
	b.expect("static-part is now Left the room (was yes)")
	joinRoom(ann)
	w.expect(":ann!~ann@127.0.0.1 JOIN")
	ann.send("PART #room")
	if l := w.expect(":ann!~ann@127.0.0.1 PART "); !strings.HasSuffix(strings.TrimSpace(l), "PART #room :Left the room") {
		t.Errorf("part with a text: %q", l)
	}
	b.send("PART #room :mine")
	if l := w.expect(" PART #room :mine"); !strings.HasSuffix(strings.TrimSpace(l), "PART #room :mine") {
		t.Errorf("the oper's part: %q", l)
	}
	b.send("AS SET static-part off")
	b.expect("static-part is now off (was Left the room)")
	s.onLoop(func() {
		if s.cfg.Messages.StaticPart != "" {
			t.Errorf("static_part after OFF: %q", s.cfg.Messages.StaticPart)
		}
	})
}

// ban_contact is added to "You are banned" at a K-line and at connect.
func TestBanContact(t *testing.T) {
	s, _, _ := fileServer(t, "\n[messages]\nban_contact = \"kline@example.net\"\n")
	b := boss(t, s)
	guest := login(t, s, "guest")
	b.send("KLINE +1h guest spam")
	if l := guest.expect(" 465 guest "); !strings.HasSuffix(strings.TrimSpace(l), ": spam. Email kline@example.net for more information.") {
		t.Errorf("K-line 465: %q", l)
	}
	again := raw(t, s)
	again.nick = "again"
	again.send("NICK again")
	again.send("USER again 0 * :Again")
	if l := again.expect(" 465 "); !strings.HasSuffix(strings.TrimSpace(l), "spam. Email kline@example.net for more information.") {
		t.Errorf("at connect: %q", l)
	}
	s.onLoop(func() {
		if got := s.cfg.Messages.BanText("You are banned: go away!"); got != "You are banned: go away! Email kline@example.net for more information." {
			t.Errorf("no second stop: %q", got)
		}
	})
}

// AdminServ SET static-quit works at once and UNDO puts it back.
func TestStaticQuitSetUndo(t *testing.T) {
	s, _, _ := fileServer(t, "")
	w := login(t, s, "watch")
	joinRoom(w)
	b := boss(t, s)
	b.send("AS SET static-quit Client exited")
	b.expect("static-quit is now Client exited (was off)")
	ann := login(t, s, "ann")
	joinRoom(ann)
	ann.send("QUIT :visit my site")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Client exited") {
		t.Errorf("after SET: %q", l)
	}
	b.send("AS HISTORY 1")
	l := b.expect("static-quit")
	n := strings.Fields(strings.SplitN(l, " :", 2)[1])[0]
	b.send("AS UNDO " + n)
	b.expect("static-quit is now off (was Client exited)")
	bob := login(t, s, "bob")
	joinRoom(bob)
	bob.send("QUIT :bye")
	if l := w.expect(" QUIT "); !strings.HasSuffix(strings.TrimSpace(l), "QUIT :Quit: bye") {
		t.Errorf("after UNDO: %q", l)
	}
	b.send("AS SET ban-contact kline@example.net")
	b.expect("ban-contact is now kline@example.net (was off)")
	b.send("AS SET ban-contact off")
	b.expect("ban-contact is now off (was kline@example.net)")
}
