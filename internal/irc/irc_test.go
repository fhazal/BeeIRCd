package irc

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		line string
		want Message
	}{
		{"NICK Bee", Message{Command: "NICK", Params: []string{"Bee"}}},
		{"privmsg #bchat :hello there\r\n", Message{Command: "PRIVMSG", Params: []string{"#bchat", "hello there"}}},
		{":Bee!~b@h PRIVMSG #a :", Message{Source: "Bee!~b@h", Command: "PRIVMSG", Params: []string{"#a", ""}}},
		{"USER bee 0 * :Bee Real", Message{Command: "USER", Params: []string{"bee", "0", "*", "Bee Real"}}},
		{"MODE  #a   +o   Bee ", Message{Command: "MODE", Params: []string{"#a", "+o", "Bee"}}},
		{"@+typing=active;msgid=a\\sb :n!u@h TAGMSG #a",
			Message{Tags: map[string]string{"+typing": "active", "msgid": "a b"}, Source: "n!u@h", Command: "TAGMSG", Params: []string{"#a"}}},
		{"PING :x :y", Message{Command: "PING", Params: []string{"x :y"}}},
	}
	for _, c := range cases {
		got, err := Parse(c.line)
		if err != nil {
			t.Errorf("Parse(%q) error %v", c.line, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %#v, want %#v", c.line, got, c.want)
		}
	}
	for _, bad := range []string{"", "   ", ":src", ":src ", "@a=b", ": x"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		m    Message
		want string
	}{
		{Message{Source: "srv", Command: "001", Params: []string{"Bee", "Welcome to BeeNet, Bee"}}, ":srv 001 Bee :Welcome to BeeNet, Bee"},
		{Message{Command: "MODE", Params: []string{"#a", "+o", "Bee"}}, "MODE #a +o Bee"},
		{Message{Command: "PRIVMSG", Params: []string{"#a", ""}}, "PRIVMSG #a :"},
		{Message{Command: "PRIVMSG", Params: []string{"#a", ":)"}}, "PRIVMSG #a ::)"},
		{Message{Command: "PRIVMSG", Params: []string{"#a", "hi"}, Trailing: true}, "PRIVMSG #a :hi"},
		{Message{Tags: map[string]string{"b": "x y", "a": ""}, Command: "TAGMSG", Params: []string{"#a"}}, "@a;b=x\\sy TAGMSG #a"},
	}
	for _, c := range cases {
		if got := c.m.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	in := Message{Tags: map[string]string{"k": "a;b\\c d"}, Source: "s", Command: "PRIVMSG", Params: []string{"#x", "hi :there"}}
	out, err := Parse(in.String())
	out.Trailing = in.Trailing
	if err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip: %#v -> %q -> %#v (%v)", in, in.String(), out, err)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("héllo", 2); got != "h" {
		t.Errorf("Truncate cut a UTF-8 character: %q", got)
	}
	if got := Truncate("abc", 5); got != "abc" {
		t.Errorf("Truncate = %q", got)
	}
}

func TestFold(t *testing.T) {
	// ascii (the default): only A-Z fold.
	if Casemapping() != CaseASCII || Fold(`Bee[]\~`) != `bee[]\~` {
		t.Errorf("ascii: %s Fold = %q", Casemapping(), Fold(`Bee[]\~`))
	}
	if !EqualFold("Belon", "bELON") || EqualFold("Nick[a]", "nick{A}") {
		t.Error("ascii EqualFold wrong")
	}
	SetCasemapping(CaseRFC1459)
	defer SetCasemapping(CaseASCII)
	if Fold(`Bee[]\~`) != "bee{}|^" {
		t.Errorf("rfc1459: Fold = %q", Fold(`Bee[]\~`))
	}
	if !EqualFold("Nick[a]", "nick{A}") || !Match("[x]!*@*", "{X}!a@b") {
		t.Error("rfc1459 EqualFold/Match failed")
	}
}

func TestValidNick(t *testing.T) {
	good := []string{"Bee", "b", "[away]", "a-1", "_x_", "`q"}
	bad := []string{"", "1abc", "-a", "a b", "a!b", "a@b", "a.b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, n := range good {
		if !ValidNick(n, 30) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range bad {
		if ValidNick(n, 30) {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestValidChannel(t *testing.T) {
	good := []string{"#a", "#bchat", "#über", "#a-b.c"}
	bad := []string{"#", "a", "#a b", "#a,b", "#a:b", "#a\x07", "&a"}
	for _, n := range good {
		if !ValidChannel(n, 50) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range bad {
		if ValidChannel(n, 50) {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"*!*@*.example", "bee!~b@host.example", true},
		{"*!*@*.example", "bee!~b@example", false},
		{"bee!*@*", "BEE!x@y", true},
		{"b?e!*@*", "bee!x@y", true},
		{"*", "", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"[x]!*@*", "{X}!a@b", false}, // ascii: [ and { differ
		{"[x]!*@*", "[X]!a@b", true},
	}
	for _, c := range cases {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.p, c.s, got)
		}
	}
}

func TestNormalizeMask(t *testing.T) {
	cases := map[string]string{
		"bob":         "bob!*@*",
		"*.example":   "*!*@*.example",
		"u@h":         "*!u@h",
		"n!u":         "n!u@*",
		"n!u@h":       "n!u@h",
		"203.0.113.*": "*!*@203.0.113.*",
	}
	for in, want := range cases {
		if got := NormalizeMask(in); got != want {
			t.Errorf("NormalizeMask(%q) = %q, want %q", in, got, want)
		}
	}
}
