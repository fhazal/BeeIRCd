package irc

import (
	"reflect"
	"strings"
	"testing"
)

// S1: CR, LF and NUL must never let one message carry
// a second line.

func TestSplitFraming(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"PRIVMSG #a :hi", []string{"PRIVMSG #a :hi"}},
		{"", nil},
		{"PRIVMSG #a :hello\r:evil!x@y PRIVMSG #a :forged", []string{"PRIVMSG #a :hello", ":evil!x@y PRIVMSG #a :forged"}},
		{"A\r\rB", []string{"A", "B"}},
		{"\r", nil},
		{"PRIVMSG #a :bad\x00byte", nil},
		{"PING :a\rPRIVMSG #a :x\x00y\rPING :b", []string{"PING :a", "PING :b"}},
		{"QUIT :bye\n", []string{"QUIT :bye"}},
	} {
		if got := SplitFraming(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitFraming(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCleanFraming(t *testing.T) {
	if got := CleanFraming("plain text"); got != "plain text" {
		t.Errorf("changed clean text: %q", got)
	}
	if got := CleanFraming("a\rb\nc\x00d"); got != "a b c d" {
		t.Errorf("CleanFraming = %q", got)
	}
}

// TestStringNeverBreaksLines builds messages from hostile parts: the
// result is always exactly one line.
func TestStringNeverBreaksLines(t *testing.T) {
	m := Message{Source: "s\rx", Command: "PRIVMSG", Params: []string{"#a\n", "text\r\n:evil PRIVMSG #a :x\x00"}, Tags: map[string]string{"+k": "v\r\n"}}
	out := m.String()
	if strings.ContainsAny(out, "\r\n\x00") {
		t.Fatalf("String() kept framing bytes: %q", out)
	}
}

// Fuzz findings, kept as plain tests.
func TestFuzzFindings(t *testing.T) {
	// F1: an empty or invalid tag key is dropped.
	if m, err := Parse("@=x;+ok=1;a.b=2;+/c=3 CMD"); err != nil || len(m.Tags) != 1 || m.Tags["+ok"] != "1" {
		t.Errorf("bad tag keys kept: %v %v", m.Tags, err)
	}
	// F2: a ban mask with a space is refused (it broke 367 list lines).
	if _, ok := NormalizeBan("foo bar"); ok {
		t.Error("NormalizeBan took a mask with a space")
	}
	// F3: a long mask keeps its nick!user@host shape after the cut.
	long := NormalizeMask(strings.Repeat("n", 200))
	if !strings.HasSuffix(long, "!*@*") || len(long) > 100 {
		t.Errorf("long mask lost its shape: %q (%d)", long, len(long))
	}
	if again := NormalizeMask(long); again != long {
		t.Errorf("NormalizeMask not idempotent: %q -> %q", long, again)
	}
	// F4: NUL in a tag value or the command word never reaches the wire.
	m := Message{Command: "X\x00", Tags: map[string]string{"+a": "v\x00w"}, Params: []string{"p"}}
	if strings.ContainsAny(m.String(), "\x00") {
		t.Errorf("NUL on the wire: %q", m.String())
	}
	// F5: an extban whose argument is cut away to nothing is refused.
	if _, ok := NormalizeBan("~a:0" + strings.Repeat("\x9e", 120)); ok {
		t.Error("empty ~a: after the cut was accepted")
	}
}

// F6: an argument echoed in a middle parameter can't shift the others.
func TestMiddleParamsStayOneWord(t *testing.T) {
	out := Message{Command: "696", Params: []string{"me", "#p", "b", "foo bar", "", ":x", "Invalid"}, Trailing: true}.String()
	if want := "696 me #p b foo_bar * _x :Invalid"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}
