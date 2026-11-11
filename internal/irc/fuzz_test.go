package irc

import (
	"strings"
	"testing"
)

// Fuzz targets (audit T0). `go test` runs the seeds every time; longer runs:
//   go test -fuzz=FuzzParse -fuzztime=5m ./internal/irc/

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"PRIVMSG #a :hello",
		"@time=2026-11-11T00:00:00.000Z;+typing=active :n!u@h PRIVMSG #a :hi",
		":srv 001 nick :Welcome",
		"MODE #a +ov-b nick other *!*@*",
		"@a=b\\:c\\s\\\\ CMD x :",
		"   ",
		":",
		"@",
		"CMD :trailing with : colon",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		m, err := Parse(line)
		if err != nil {
			return
		}
		out := m.String()
		if strings.ContainsAny(out, "\r\n\x00") {
			t.Fatalf("String() of a parsed line holds framing bytes: %q -> %q", line, out)
		}
		// A framed line (the only kind the server parses: SplitFraming drops
		// or cuts the rest first) must survive a second round trip unchanged.
		if strings.ContainsAny(line, "\r\n\x00") {
			return
		}
		// Junk command words get 421 from the server; only real ones
		// (letters or a 3-digit numeric) need to round-trip.
		for _, r := range m.Command {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return
			}
		}
		m2, err := Parse(out)
		if err != nil {
			t.Fatalf("re-parse of %q failed: %v", out, err)
		}
		if m2.String() != out {
			t.Fatalf("round trip changed:\n%q\n%q", out, m2.String())
		}
	})
}

func FuzzNormalizeBan(f *testing.F) {
	for _, s := range []string{"nick", "*!*@host", "~a:acct", "~t:10:*!*@h", "~m:~r:x*", "1.2.3.0/24", "*!*@[::1]", "a!b", "~t:x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, mask string) {
		n, ok := NormalizeBan(mask)
		if !ok {
			return
		}
		if strings.ContainsAny(n, " \r\n\x00") {
			t.Fatalf("NormalizeBan(%q) = %q holds a space or framing byte", mask, n)
		}
		// Normalising twice changes nothing.
		if n2, ok2 := NormalizeBan(n); !ok2 || n2 != n {
			t.Fatalf("not idempotent: %q -> %q -> %q (%v)", mask, n, n2, ok2)
		}
		MatchHost(n, "host.example.org")
		MatchUserHost(n, "user", "host.example.org", "192.0.2.1")
	})
}

func FuzzTags(f *testing.F) {
	for _, s := range []string{"a=b", "+typing=active;msgid=1", "k=\\:\\s\\r\\n\\\\", "=", ";;", "a=\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		tags := parseTags(raw)
		for k, v := range tags {
			if e := EscapeTag(v); strings.ContainsAny(e, " ;\r\n\x00") && !strings.Contains(v, "\x00") {
				t.Fatalf("EscapeTag(%q) = %q for key %q", v, e, k)
			}
		}
	})
}
