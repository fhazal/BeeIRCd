package irc

import (
	"reflect"
	"strings"
	"testing"
)

// S1 (audit 11 Nov 2026): CR, LF and NUL must never let one message carry
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

// F6: an argument echoed in a middle parameter can't shift the others.
func TestMiddleParamsStayOneWord(t *testing.T) {
	out := Message{Command: "696", Params: []string{"me", "#p", "b", "foo bar", "", ":x", "Invalid"}, Trailing: true}.String()
	if want := "696 me #p b foo_bar * _x :Invalid"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}
