package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// The channel manager: a channel's page by ChanServ level, and changes
// through the real ChanServ.

func chanDo(sess control.Request, channel, kind, value, mask, reason string) control.Request {
	q := as(sess, control.CmdChanDo)
	q.Name, q.Kind, q.Value, q.Mask, q.Reason = channel, kind, value, mask, reason
	return q
}

func myChan(t *testing.T, s *Server, sess control.Request, name string) *control.MyChan {
	t.Helper()
	q := as(sess, control.CmdMyChan)
	q.Name = name
	r := panelAsk(t, s, q)
	if r.Chan == nil {
		t.Fatalf("my-chan %s: %+v", name, r)
	}
	return r.Chan
}

func TestPanelChannelManager(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	e := registered(t, s, "ember")
	o := registered(t, s, "other")
	registered(t, s, "third")
	e.send("JOIN #kampung")
	e.expect(" 366 ember #kampung ")
	e.send("PRIVMSG ChanServ :REGISTER #kampung")
	e.expect("#kampung is registered")
	syncWith(e)

	m := memberSession(t, s, "ember", pw)
	c := myChan(t, s, m, "#kampung")
	if !c.Can.Founder || !c.Can.AccessChange || !c.Can.Akick || !c.Can.Set || !c.Can.Memo || c.Toggles == nil || c.Founder != "ember" {
		t.Fatalf("founder's page: %+v", c)
	}
	if len(c.People) != 1 || c.People[0].Nick != "ember" || c.People[0].Prefix != "@" {
		t.Errorf("people: %+v", c.People)
	}

	do := func(sess control.Request, kind, value, mask, reason string) *control.Response {
		t.Helper()
		return panelAsk(t, s, chanDo(sess, "#kampung", kind, value, mask, reason))
	}
	if r := do(m, "ACCESS", "ADD", "other", "AOP"); r.Done == nil || !strings.Contains(r.Done.Text, "other added to the access list of #kampung at level AOP") {
		t.Fatalf("ACCESS ADD: %+v", r)
	}
	if r := do(m, "ACCESS", "ADD", "other", "AOP"); r.Done != nil || !strings.Contains(r.Error, "already") {
		t.Errorf("ACCESS ADD twice: %+v", r)
	}
	c = myChan(t, s, m, "#kampung")
	if len(c.Access) != 1 || c.Access[0].Nick != "other" || c.Access[0].Level != "AOP" || c.Access[0].By != "ember" || !c.Access[0].Online {
		t.Errorf("access after ADD: %+v", c.Access)
	}
	if r := do(m, "AKICK", "ADD", "*!*@bad.example", "spam bots"); r.Done == nil {
		t.Fatalf("AKICK ADD: %+v", r)
	}
	if c = myChan(t, s, m, "#kampung"); len(c.Akicks) != 1 || c.Akicks[0].Reason != "spam bots" {
		t.Errorf("akicks: %+v", c.Akicks)
	}
	if r := do(m, "AKICK", "DEL", "*!*@bad.example", ""); r.Done == nil {
		t.Errorf("AKICK DEL: %+v", r)
	}
	if r := do(m, "SET", "KEEPTOPIC", "", "ON"); r.Done == nil || !myChan(t, s, m, "#kampung").Toggles["KEEPTOPIC"] {
		t.Errorf("SET KEEPTOPIC ON: %+v", r)
	}
	if r := do(m, "SET", "DESCRIPTION", "", "home of the bees"); r.Done == nil || myChan(t, s, m, "#kampung").Description != "home of the bees" {
		t.Errorf("SET DESCRIPTION: %+v", r)
	}
	if r := do(m, "SET", "SUCCESSOR", "", "other"); r.Done == nil || myChan(t, s, m, "#kampung").Successor != "other" {
		t.Errorf("SET SUCCESSOR: %+v", r)
	}
	for _, n := range panelAsk(t, s, as(m, control.CmdMyHome)).Home.Needs {
		if strings.Contains(n.Text, "successor") {
			t.Errorf("Home still says no successor: %+v", n)
		}
	}
	if r := do(m, "TOPIC", "", "", "welcome to the hive"); r.Done == nil {
		t.Fatalf("TOPIC: %+v", r)
	}
	e.expect("TOPIC #kampung :welcome to the hive")
	if r := do(m, "TOPIC", "", "", "welcome to the hive"); r.Done == nil || !strings.Contains(r.Done.Text, "topic of #kampung is set") {
		t.Errorf("TOPIC answer: %+v", r)
	}
	// A topic that starts like a TOPIC subcommand is still just a topic.
	for _, text := range []string{"Append your name below", "LOCK", "prepend me"} {
		if r := do(m, "TOPIC", "", "", text); r.Done == nil {
			t.Fatalf("TOPIC %q: %+v", text, r)
		}
		e.expect("TOPIC #kampung :" + text)
	}
	if myChan(t, s, m, "#kampung").Toggles["TOPICLOCK"] {
		t.Errorf("a topic \"LOCK\" locked the topic")
	}
	// Only what the page offers.
	for _, bad := range [][]string{{"SET", "MLOCK", "", "+k key"}, {"SET", "NOEXPIRE", "", "ON"}, {"REGISTER", "", "", ""}, {"ACCESS", "CLEAR", "", ""}, {"LEVELS", "SET", "OP", "1"}} {
		if r := do(m, bad[0], bad[1], bad[2], bad[3]); r.Done != nil || r.Error != "That can't be done here." {
			t.Errorf("%v: %+v", bad, r)
		}
	}

	// An AOP sees the list but changes nothing; no settings, no memo box.
	om := memberSession(t, s, "other", pw)
	oc := myChan(t, s, om, "#kampung")
	if !oc.Can.AccessList || oc.Can.AccessChange || oc.Can.Set || oc.Can.Memo || oc.Can.Founder || oc.Toggles != nil || oc.Memos != nil || len(oc.Access) != 1 {
		t.Errorf("AOP's page: %+v", oc)
	}
	if r := do(om, "ACCESS", "ADD", "third", "VOP"); r.Done != nil || !strings.Contains(r.Error, "SOP or higher") {
		t.Errorf("AOP adds: %+v", r)
	}
	if r := do(om, "SET", "KEEPTOPIC", "", "OFF"); r.Done != nil {
		t.Errorf("AOP sets: %+v", r)
	}
	if r := do(om, "DROP", "", "#kampung", ""); r.Done != nil {
		t.Errorf("AOP drops: %+v", r)
	}
	// Not on the list: no page at all.
	tq := as(memberSession(t, s, "third", pw), control.CmdMyChan)
	tq.Name = "#kampung"
	if r := panelAsk(t, s, tq); r.Chan != nil || !strings.Contains(r.Error, "aren't on the access list") {
		t.Errorf("stranger's page: %+v", r)
	}

	// The channel's memo box: MEMO privilege only.
	o.send("PRIVMSG MemoServ :SEND #kampung meeting on friday")
	o.expect("Memo sent to #kampung")
	c = myChan(t, s, m, "#kampung")
	if len(c.Memos) != 1 || c.Memos[0].Read {
		t.Fatalf("memo box: %+v", c.Memos)
	}
	mq := func(sess control.Request, verb string) *control.Response {
		q := as(sess, control.CmdChanMemo)
		q.Name, q.ID, q.Value = "#kampung", c.Memos[0].ID, verb
		return panelAsk(t, s, q)
	}
	if r := mq(om, "del"); r.Done != nil {
		t.Errorf("AOP deletes a channel memo: %+v", r)
	}
	if r := mq(m, "read"); r.Done == nil || !myChan(t, s, m, "#kampung").Memos[0].Read {
		t.Errorf("read channel memo: %+v", r)
	}
	if r := mq(m, "del"); r.Done == nil || len(myChan(t, s, m, "#kampung").Memos) != 0 {
		t.Errorf("del channel memo: %+v", r)
	}

	// Hand over, then the old founder can't drop it.
	if r := do(m, "SET", "FOUNDER", "", "other"); r.Done == nil {
		t.Fatalf("SET FOUNDER: %+v", r)
	}
	if r := do(m, "DROP", "", "#kampung", ""); r.Done != nil {
		t.Errorf("old founder drops: %+v", r)
	}
	// The new founder drops it: the name twice, as on IRC.
	if r := do(om, "DROP", "", "#wrong", ""); r.Done != nil {
		t.Errorf("DROP with the wrong name: %+v", r)
	}
	if r := do(om, "DROP", "", "#kampung", ""); r.Done == nil || !strings.Contains(r.Done.Text, "no longer registered") {
		t.Errorf("DROP: %+v", r)
	}
}
