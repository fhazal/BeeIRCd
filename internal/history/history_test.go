package history

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// t0 is "now" for the tests: SetLimit trims by the real clock, so a fixed
// date here made TestLimits fail once that date was over an hour old.
var t0 = time.Now().UTC().Truncate(time.Second)

func fill(s *Store, ch string, n int, lim Limit) {
	for i := 0; i < n; i++ {
		s.Add(ch, lim, Line{At: t0.Add(time.Duration(i) * time.Second), MsgID: "m" + strconv.Itoa(i), Source: "a!a@h", Verb: "PRIVMSG", Target: ch, Text: "line " + strconv.Itoa(i)})
	}
}

func texts(ls []Line) string {
	var out []string
	for _, l := range ls {
		out = append(out, strings.TrimPrefix(l.Text, "line "))
	}
	return strings.Join(out, ",")
}

func TestQueries(t *testing.T) {
	s := New(0, strings.ToLower)
	fill(s, "#Bee", 10, Limit{Lines: 100, Age: 24 * time.Hour})
	cases := []struct {
		name string
		got  []Line
		want string
	}{
		{"latest *", s.Latest("#bee", Ref{Star: true}, 3), "7,8,9"},
		{"latest after msgid", s.Latest("#bee", Ref{MsgID: "m7"}, 5), "8,9"},
		{"before msgid", s.Before("#bee", Ref{MsgID: "m5"}, 2), "3,4"},
		{"after msgid", s.After("#bee", Ref{MsgID: "m5"}, 2), "6,7"},
		{"before time", s.Before("#bee", Ref{At: t0.Add(2 * time.Second)}, 10), "0,1"},
		{"after time", s.After("#bee", Ref{At: t0.Add(7 * time.Second)}, 10), "8,9"},
		{"around", s.Around("#bee", Ref{MsgID: "m5"}, 3), "4,5,6"},
		{"between forwards", s.Between("#bee", Ref{MsgID: "m2"}, Ref{MsgID: "m6"}, 10), "3,4,5"},
		{"between backwards", s.Between("#bee", Ref{MsgID: "m6"}, Ref{MsgID: "m2"}, 2), "4,5"},
		{"unknown msgid", s.Before("#bee", Ref{MsgID: "nope"}, 5), ""},
		{"recent", s.Recent("#bee", 2, t0), "8,9"},
		{"recent since", s.Recent("#bee", 5, t0.Add(8*time.Second)), "8,9"},
	}
	for _, c := range cases {
		if got := texts(c.got); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLimits(t *testing.T) {
	s := New(0, strings.ToLower)
	fill(s, "#a", 10, Limit{Lines: 4, Age: time.Hour})
	if got := texts(s.Latest("#a", Ref{Star: true}, 100)); got != "6,7,8,9" {
		t.Errorf("line cap: %q", got)
	}
	s.SetLimit("#a", Limit{Lines: 2, Age: time.Hour})
	if s.Lines("#a") != 2 {
		t.Errorf("SetLimit kept %d lines", s.Lines("#a"))
	}
	s.Expire(t0.Add(2 * time.Hour))
	if s.Lines("#a") != 0 || s.Bytes() != 0 {
		t.Errorf("Expire left %d lines, %d bytes", s.Lines("#a"), s.Bytes())
	}
	fill(s, "#b", 3, Limit{Lines: 10, Age: time.Hour})
	s.Drop("#B")
	if s.Lines("#b") != 0 || s.Bytes() != 0 {
		t.Error("Drop left lines")
	}
}

func TestMemoryCap(t *testing.T) {
	s := New(20_000, strings.ToLower)
	lim := Limit{Lines: 100000, Age: 24 * time.Hour}
	fill(s, "#old", 100, lim)
	for i := 0; i < 300; i++ {
		s.Add("#new", lim, Line{At: t0.Add(time.Hour + time.Duration(i)*time.Second), MsgID: "n" + strconv.Itoa(i), Text: strings.Repeat("x", 50)})
	}
	if s.Bytes() > 20_000 {
		t.Errorf("over the cap: %d", s.Bytes())
	}
	if s.Lines("#old") != 0 {
		t.Errorf("oldest channel should go first, %d lines left", s.Lines("#old"))
	}
	if s.Lines("#new") == 0 {
		t.Error("newest lines were dropped")
	}
}

func TestTargets(t *testing.T) {
	s := New(0, strings.ToLower)
	lim := Limit{Lines: 10, Age: 24 * time.Hour}
	s.Add("#One", lim, Line{At: t0.Add(1 * time.Second), MsgID: "1"})
	s.Add("#Two", lim, Line{At: t0.Add(3 * time.Second), MsgID: "2"})
	s.Add("#One", lim, Line{At: t0.Add(5 * time.Second), MsgID: "3"})
	got := s.Targets([]string{"#one", "#two", "#none"}, t0, t0.Add(time.Hour), 10)
	if len(got) != 2 || got[0].Name != "#Two" || got[1].Name != "#One" {
		t.Fatalf("targets: %+v", got)
	}
	got = s.Targets([]string{"#one", "#two"}, t0.Add(4*time.Second), t0, 10)
	if len(got) != 2 || got[0].Name != "#One" || got[0].Latest != t0.Add(time.Second) {
		t.Errorf("window (either order) should see only lines in it: %+v", got)
	}
	if got := s.Targets([]string{"#one", "#two"}, t0, t0.Add(time.Hour), 1); len(got) != 1 {
		t.Errorf("limit: %+v", got)
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s := New(0, strings.ToLower)
	now := time.Now()
	lim := Limit{Lines: 50, Age: 24 * time.Hour}
	s.Add("#Bee", lim, Line{At: now.Add(-time.Minute), MsgID: "x1", Source: "a!b@c", Account: "acct", Verb: "PRIVMSG", Target: "#Bee", Text: "hello", Tags: map[string]string{"+draft/reply": "x0"}})
	if err := Save(path, "a-long-history-secret", s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	r := New(0, strings.ToLower)
	if _, _, err := r.Load(path, "the-wrong-secret-here", now); err != ErrWrongSecret {
		t.Fatalf("wrong secret: %v", err)
	}
	chans, lines, err := r.Load(path, "a-long-history-secret", now)
	if err != nil || chans != 1 || lines != 1 {
		t.Fatalf("load: %d %d %v", chans, lines, err)
	}
	l := r.Latest("#bee", Ref{Star: true}, 10)[0]
	if l.Text != "hello" || l.Account != "acct" || l.Tags["+draft/reply"] != "x0" || l.Target != "#Bee" {
		t.Errorf("line changed on the way: %+v", l)
	}
	if _, n, _ := New(0, strings.ToLower).Load(path, "a-long-history-secret", now.Add(48*time.Hour)); n != 0 {
		t.Errorf("lines past their age came back: %d", n)
	}
	if c, n, err := New(0, strings.ToLower).Load(filepath.Join(t.TempDir(), "none.db"), "a-long-history-secret", now); err != nil || c+n != 0 {
		t.Errorf("missing file: %d %d %v", c, n, err)
	}
}
