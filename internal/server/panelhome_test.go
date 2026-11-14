package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
)

// Member Home and the Memos page: the member's own facts only.

// sync waits until the server has handled everything c sent before.
func syncWith(c *tc) {
	c.send("PING :sync-point")
	c.expect("sync-point")
}

func memoReq(sess control.Request, cmd string, id int64, to, text string) control.Request {
	q := as(sess, cmd)
	q.ID, q.Name, q.Value = id, to, text
	return q
}

func TestPanelMemberHome(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	e := registered(t, s, "ember")
	o := registered(t, s, "other")
	x := registered(t, s, "stranger")

	e.send("JOIN #kampung")
	e.expect(" 366 ember #kampung ")
	e.send("PRIVMSG ChanServ :REGISTER #kampung")
	e.expect("#kampung is registered")
	e.send("TOPIC #kampung :welcome home")
	e.send("CS ACCESS #kampung ADD other AOP")
	e.send("CS AKICK #kampung ADD *!*@bad.example spam")
	syncWith(e)

	// A channel ember has nothing to do with: its list stays hidden.
	x.send("JOIN #secret")
	x.expect(" 366 stranger #secret ")
	x.send("PRIVMSG ChanServ :REGISTER #secret")
	x.expect("#secret is registered")
	x.send("CS ACCESS #secret ADD other VOP")
	o.send("PRIVMSG MemoServ :SEND ember hello from other")
	o.expect("Memo sent to ember")
	syncWith(x)

	m := memberSession(t, s, "ember", pw)
	r := panelAsk(t, s, as(m, control.CmdMyHome))
	h := r.Home
	if h == nil || h.Name != "ember" {
		t.Fatalf("my-home: %+v", r)
	}
	if h.Unread != 1 || h.Kept != 1 || len(h.Memos) != 1 || h.Memos[0].From != "other" || h.Memos[0].Text != "hello from other" {
		t.Errorf("memos on Home: %+v", h)
	}
	if len(h.Channels) != 1 || h.Channels[0].Name != "#kampung" || !h.Channels[0].Founder || h.Channels[0].Users != 1 ||
		h.Channels[0].Status != "@" || h.Channels[0].TopicBy != "ember" {
		t.Errorf("channels: %+v", h.Channels)
	}
	if h.OnMyLists != 1 {
		t.Errorf("on my lists: %d, want 1 (other)", h.OnMyLists)
	}
	var sawAop, sawKick bool
	for _, a := range h.Access {
		if a.Channel == "#secret" {
			t.Errorf("access of a channel ember isn't on: %+v", a)
		}
		sawAop = sawAop || a.Channel == "#kampung" && a.Who == "other" && a.Level == "AOP" && a.By == "ember"
		sawKick = sawKick || a.Level == "AKICK" && a.Who == "*!*@bad.example"
	}
	if !sawAop || !sawKick {
		t.Errorf("access changes: %+v", h.Access)
	}
	if len(h.Conns) != 1 || h.Conns[0].Nick != "ember" || h.Conns[0].Web {
		t.Errorf("connections: %+v", h.Conns)
	}
	var successor bool
	for _, n := range h.Needs {
		successor = successor || strings.Contains(n.Text, "#kampung has no successor")
	}
	if !successor {
		t.Errorf("needs: %+v", h.Needs)
	}
	if h.Users < 3 || h.ChanCount != 2 || h.Servers != 1 {
		t.Errorf("network line: %d users, %d channels, %d servers", h.Users, h.ChanCount, h.Servers)
	}
	// Never an IP or real host, not even the member's own.
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "127.0.0.1") {
		t.Errorf("Home carries an IP: %s", raw)
	}

	// Once ember is on #secret's list, that entry (about ember) shows.
	x.send("CS ACCESS #secret ADD ember HOP")
	syncWith(x)
	h = panelAsk(t, s, as(m, control.CmdMyHome)).Home
	var own bool
	for _, a := range h.Access {
		own = own || a.Channel == "#secret" && a.Who == "ember"
	}
	if !own {
		t.Errorf("own entry on #secret: %+v", h.Access)
	}
}

func TestPanelMemberMemos(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	e := registered(t, s, "ember")
	o := registered(t, s, "other")
	o.send("PRIVMSG MemoServ :SEND ember first")
	o.expect("Memo sent to ember")
	e.send("PRIVMSG MemoServ :SEND other for other only")
	e.expect("Memo sent to other")

	m := memberSession(t, s, "ember", pw)
	mm := panelAsk(t, s, as(m, control.CmdMyMemos)).Memos
	if mm == nil || len(mm.Memos) != 1 || mm.Memos[0].Text != "first" || mm.Memos[0].Read || mm.Limit <= 0 {
		t.Fatalf("my-memos: %+v", mm)
	}
	id := mm.Memos[0].ID

	// other's memo can't be read or deleted from ember's session.
	om := panelAsk(t, s, as(memberSession(t, s, "other", pw), control.CmdMyMemos)).Memos
	if om == nil || len(om.Memos) != 1 {
		t.Fatalf("other's memos: %+v", om)
	}
	for _, cmd := range []string{control.CmdMemoRead, control.CmdMemoDel} {
		if r := panelAsk(t, s, memoReq(m, cmd, om.Memos[0].ID, "", "")); r.Done != nil || r.Error == "" {
			t.Errorf("%s of another's memo: %+v", cmd, r)
		}
	}
	if om2 := panelAsk(t, s, as(memberSession(t, s, "other", pw), control.CmdMyMemos)).Memos; len(om2.Memos) != 1 || om2.Memos[0].Read {
		t.Errorf("other's memo changed: %+v", om2)
	}

	if r := panelAsk(t, s, memoReq(m, control.CmdMemoRead, id, "", "")); r.Done == nil {
		t.Fatalf("memo-read: %+v", r)
	}
	if mm = panelAsk(t, s, as(m, control.CmdMyMemos)).Memos; !mm.Memos[0].Read {
		t.Errorf("not read after memo-read: %+v", mm)
	}

	// SEND is MemoServ's: other, online, hears about it at once.
	if r := panelAsk(t, s, memoReq(m, control.CmdMemoSend, 0, "other", "sent  from the panel")); r.Done == nil || !strings.Contains(r.Done.Text, "Memo sent to other") {
		t.Fatalf("memo-send: %+v", r)
	}
	o.expect("You have a new memo from ember")
	o.send("PRIVMSG MemoServ :READ LAST")
	o.expect("sent  from the panel")
	if r := panelAsk(t, s, memoReq(m, control.CmdMemoSend, 0, "nobody-here", "hi")); r.Done != nil || !strings.Contains(r.Error, "isn't a registered nick") {
		t.Errorf("memo-send to an unknown nick: %+v", r)
	}
	if r := panelAsk(t, s, memoReq(m, control.CmdMemoSend, 0, "other", strings.Repeat("x", 301))); r.Done != nil || !strings.Contains(r.Error, "up to 300") {
		t.Errorf("long memo: %+v", r)
	}

	if r := panelAsk(t, s, memoReq(m, control.CmdMemoDel, id, "", "")); r.Done == nil {
		t.Fatalf("memo-del: %+v", r)
	}
	if mm = panelAsk(t, s, as(m, control.CmdMyMemos)).Memos; len(mm.Memos) != 0 {
		t.Errorf("after memo-del: %+v", mm)
	}
}

func TestPanelMemoSendDelay(t *testing.T) {
	s, _, _ := fileServerWith(t, membersOn, func(s *Server) {
		s.cfg.Services.MemoSendDelay = &config.Duration{Duration: time.Hour}
	})
	registered(t, s, "ember")
	registered(t, s, "other")
	m := memberSession(t, s, "ember", pw)
	if r := panelAsk(t, s, memoReq(m, control.CmdMemoSend, 0, "other", "one")); r.Done == nil {
		t.Fatalf("first send: %+v", r)
	}
	// A new request (a new page load) still waits.
	if r := panelAsk(t, s, memoReq(m, control.CmdMemoSend, 0, "other", "two")); r.Done != nil || !strings.Contains(r.Error, "Please wait") {
		t.Errorf("second send at once: %+v", r)
	}
}
