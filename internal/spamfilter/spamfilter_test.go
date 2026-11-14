package spamfilter

import (
	"strings"
	"testing"
	"time"
)

func f(typ, match, targets, action string) Filter {
	return Filter{Type: typ, Match: match, Targets: targets, Action: action, SetAt: time.Unix(1, 0)}
}

func TestSimpleMatchIgnoresCaseAndColours(t *testing.T) {
	s, bad := NewSet([]Filter{f(Simple, "*free nitro*", "cp", Block)}, time.Now())
	if len(bad) > 0 {
		t.Fatal(bad)
	}
	for _, text := range []string{"get FREE NITRO now", "\x0304free\x03 \x02nitro\x02 here", "free nitro"} {
		if s.First(Channel, text) == nil {
			t.Errorf("missed %q", text)
		}
	}
	for _, text := range []string{"nitro free", "freenitro"} {
		if s.First(Channel, text) != nil {
			t.Errorf("caught %q", text)
		}
	}
	if s.First(Topic, "free nitro") != nil {
		t.Error("a channel+private rule caught a topic")
	}
}

func TestRegexIsCaseInsensitiveAndRE2(t *testing.T) {
	s, _ := NewSet([]Filter{f(Regex, `^[a-z]{3}\d{5}$`, "u", Kill)}, time.Now())
	if s.First(User, "ABC12345") == nil || s.First(User, "abc1234") != nil {
		t.Error("regex match wrong")
	}
	// RE2 has no backreferences: such a rule from another server is skipped.
	if _, bad := NewSet([]Filter{f(Regex, `(a)\1`, "c", Block)}, time.Now()); len(bad) != 1 {
		t.Error("an RE2-incompatible regex was not reported")
	}
}

func TestFirstIsOldest(t *testing.T) {
	a := f(Simple, "*spam*", "c", Warn)
	b := f(Simple, "*spam link*", "c", Kill)
	a.SetAt, b.SetAt = time.Unix(20, 0), time.Unix(10, 0)
	s, _ := NewSet([]Filter{a, b}, time.Now())
	if r := s.First(Channel, "a spam link"); r == nil || r.Action != Kill {
		t.Fatalf("first = %+v", r)
	}
	if n := len(s.All(Channel, "a spam link")); n != 2 {
		t.Fatalf("all = %d", n)
	}
}

func TestExpiredRulesAreLeftOut(t *testing.T) {
	x := f(Simple, "*old rule*", "c", Block)
	x.Expires = time.Now().Add(-time.Minute)
	if s, _ := NewSet([]Filter{x}, time.Now()); !s.Empty(Channel) {
		t.Fatal("expired rule in the set")
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		f    Filter
		want string
	}{
		{f(Simple, "*a*", "c", Block), "at least 4"},
		{f(Simple, "*free nitro*", "", Block), "where to look"},
		{f(Simple, "*free nitro*", "cx", Block), "not a target"},
		{f(Simple, "*free nitro*", "c", "viruschan"), "not an action"},
		{f(Regex, "(", "c", Block), "regex doesn't work"},
		{f("glob", "*free nitro*", "c", Block), "simple or regex"},
		{f(Simple, strings.Repeat("x", MaxMatch+1), "c", Block), "1 to 500"},
		{f(Simple, "*free nitro*", "pc", Gline), ""},
	}
	for _, c := range cases {
		err := Check(c.f)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v, want %q", c.f, err, c.want)
		}
	}
	if got, _ := NormTargets("upc"); got != "cpu" {
		t.Errorf("NormTargets = %q", got)
	}
}

func TestReasonRoundTrip(t *testing.T) {
	for _, r := range []string{"Spam is not allowed", "no_under_scores here", `back\slash`} {
		if got := DecodeReason(EncodeReason(r)); got != r {
			t.Errorf("%q -> %q -> %q", r, EncodeReason(r), got)
		}
		if strings.Contains(EncodeReason(r), " ") {
			t.Errorf("space left in %q", EncodeReason(r))
		}
	}
	if DecodeReason("Spam_is_bad") != "Spam is bad" || DecodeReason("a__b_c") != "a_b c" || ActionOf(ActionLetter(Gzline)) != Gzline {
		t.Error("UnrealIRCd reason not read")
	}
}

func TestSafetyNet(t *testing.T) {
	if hit := SafetyHits(f(Simple, "*free*", "c", Block), 2); len(hit) != 2 {
		t.Errorf("*free* should catch normal lines, got %q", hit)
	}
	if hit := SafetyHits(f(Simple, "*free nitro*", "cp", Block), 2); len(hit) != 0 {
		t.Errorf("*free nitro* caught %q", hit)
	}
	if hit := SafetyHits(f(Regex, `^[a-z]{3}\d{5}!`, "u", Kill), 2); len(hit) != 0 {
		t.Errorf("nick regex caught %q", hit)
	}
	if hit := SafetyHits(f(Simple, "*!*@*:*", "u", Kill), 1); len(hit) != 1 {
		t.Error("a rule on every user was not caught")
	}
	if n := SafetyCount(f(Simple, "x", "c", Block)); n < 200 {
		t.Errorf("only %d safety lines", n)
	}
}
