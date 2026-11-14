package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// The Hive Panel's Spam filter page: flag k, the same checks as /SPAMFILTER.
func TestPanelSpamFilter(t *testing.T) {
	s, _, _ := fileServer(t, "\n[spamfilter]\nexcept = [\"#help\"]\n")
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS helper HOST *@127.0.0.0/8 PASSWORD " + emberPW) // h only
	b.expect("ember is an oper now")
	ask := func(as string, r control.Request) *control.Response {
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}
	for _, cmd := range []string{control.CmdSpam, control.CmdSpamCheck, control.CmdSpamAdd, control.CmdSpamDel, control.CmdSpamTest} {
		if r := ask("ember", control.Request{Cmd: cmd, Mask: "*free nitro*", Value: "c", Name: "block", ID: 1}); r.Denied != "flag" {
			t.Errorf("%s without k: %+v", cmd, r)
		}
	}

	// The check while typing: problems, normal lines it would catch, fine.
	if c := ask("Boss", control.Request{Cmd: control.CmdSpamCheck, Mask: "*a*", Value: "c", Name: "block"}).SpamCheck; c == nil || !strings.Contains(c.Problem, "at least 4") {
		t.Errorf("short match: %+v", c)
	}
	if c := ask("Boss", control.Request{Cmd: control.CmdSpamCheck, Mask: "*free*", Value: "c", Name: "block"}).SpamCheck; c == nil || len(c.Unsafe) != 2 {
		t.Errorf("broad match: %+v", c)
	}
	if c := ask("Boss", control.Request{Cmd: control.CmdSpamCheck, Mask: "*free nitro*", Value: "cp", Name: "gline", Life: "1d"}).SpamCheck; c == nil || c.Problem != "" || len(c.Unsafe) != 0 || c.Lines < 200 {
		t.Errorf("good match: %+v", c)
	}

	// Add: refused when unsafe, unless "add anyway".
	if r := ask("Boss", control.Request{Cmd: control.CmdSpamAdd, Mask: "*free*", Value: "c", Name: "block"}); r.Error == "" || r.SpamCheck == nil || len(r.SpamCheck.Unsafe) == 0 {
		t.Errorf("unsafe add: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdSpamAdd, Mask: "*free*", Value: "c", Name: "warn", New: true}); r.Done == nil {
		t.Errorf("add anyway: %+v", r)
	}
	r := ask("Boss", control.Request{Cmd: control.CmdSpamAdd, Mask: "*free nitro*", Value: "cp", Name: "gline", Life: "1d", Reason: "No nitro spam"})
	if r.Done == nil || !strings.Contains(r.Done.Text, "*free nitro* on chan, private -> AKILL 1d (No nitro spam)") {
		t.Fatalf("add: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdSpamAdd, Mask: "*free nitro*", Value: "cp", Name: "gline", Life: "1d", Reason: "No nitro spam"}); !strings.Contains(r.Error, "there already") {
		t.Errorf("same again: %+v", r)
	}

	// The page, and a hit counted.
	v := login(t, s, "Vic")
	v.send("PRIVMSG Boss :free nitro!")
	v.expect("(Banned: No nitro spam)")
	p := ask("Boss", control.Request{Cmd: control.CmdSpam}).Spam
	if p == nil || len(p.Rules) != 2 || p.Caught24h != 1 || len(p.Except) != 1 || p.Except[0] != "#help" {
		t.Fatalf("page: %+v", p)
	}
	var id int64
	for _, rl := range p.Rules {
		if rl.Match == "*free nitro*" {
			id = rl.ID
			if rl.Hits != 1 || rl.Does != "AKILL 1d" || strings.Join(rl.Words, ",") != "chan,private" || rl.SetBy != "Boss" {
				t.Errorf("rule: %+v", rl)
			}
		}
	}

	// Try a line.
	if tr := ask("Boss", control.Request{Cmd: control.CmdSpamTest, Value: "c", Mask: "get FREE nitro"}).SpamTest; tr == nil || len(tr.Hits) != 2 {
		t.Errorf("test: %+v", tr)
	}
	if tr := ask("Boss", control.Request{Cmd: control.CmdSpamTest, Value: "t", Mask: "free nitro"}).SpamTest; tr == nil || len(tr.Hits) != 0 {
		t.Errorf("test on topics: %+v", tr)
	}

	// Remove.
	if r := ask("Boss", control.Request{Cmd: control.CmdSpamDel, ID: id}); r.Done == nil {
		t.Fatalf("del: %+v", r)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdSpamDel, ID: id}); !strings.Contains(r.Error, "no spam filter") {
		t.Errorf("del again: %+v", r)
	}
}
