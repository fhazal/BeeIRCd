package server

import (
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// checkLine is an independent check of every line a test client reads:
// it does not use internal/irc, so the server's own parser is not checking
// the server. It only holds rules every IRC line must follow (RFC 1459 /
// modern IRC framing, IRCv3 message-tags limits), not BeeIRCd choices.

var (
	commandRe = regexp.MustCompile(`^([A-Za-z]+|[0-9]{3})$`)
	tagKeyRe  = regexp.MustCompile(`^\+?([A-Za-z0-9.-]+/)?[A-Za-z0-9-]+$`)
)

// lineProblem returns what is wrong with one received line (with its CRLF),
// or "".
func lineProblem(raw string) string {
	if !strings.HasSuffix(raw, "\r\n") {
		return "does not end in CRLF"
	}
	line := strings.TrimSuffix(raw, "\r\n")
	if strings.ContainsAny(line, "\r\n\x00") {
		return "holds a CR, LF or NUL"
	}
	if strings.HasPrefix(line, "@") {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return "tags without a message"
		}
		tags := line[1:sp]
		if len(tags)+2 > 8191 {
			return "tags longer than 8191 bytes"
		}
		for _, t := range strings.Split(tags, ";") {
			key, _, _ := strings.Cut(t, "=")
			if !tagKeyRe.MatchString(key) {
				return fmt.Sprintf("bad tag key %q", key)
			}
		}
		line = line[sp+1:]
	}
	if len(line)+2 > 512 {
		return fmt.Sprintf("body is %d bytes with CRLF (limit 512)", len(line)+2)
	}
	if strings.HasPrefix(line, ":") {
		sp := strings.IndexByte(line, ' ')
		if sp <= 1 {
			return "empty source"
		}
		line = line[sp+1:]
	}
	if strings.HasPrefix(line, " ") {
		return "more than one space before the command"
	}
	cmd, rest, _ := strings.Cut(line, " ")
	if !commandRe.MatchString(cmd) {
		return fmt.Sprintf("bad command %q", cmd)
	}
	// A numeric's first parameter is the client it is for (nick or "*").
	if len(cmd) == 3 && cmd[0] >= '0' && cmd[0] <= '9' {
		target, _, _ := strings.Cut(rest, " ")
		if target == "" || strings.HasPrefix(target, ":") {
			return "numeric without a target"
		}
	}
	// Middle parameters are never empty (two spaces in a row).
	mid, _, _ := strings.Cut(rest, " :")
	if strings.Contains(mid, "  ") {
		return "empty middle parameter"
	}
	return ""
}

// check fails the test when a received line breaks the framing rules.
func (c *tc) check(raw string) {
	c.t.Helper()
	if p := lineProblem(raw); p != "" {
		c.t.Errorf("%s: protocol violation (%s): %q", c.nick, p, raw)
	}
}

// expectNumeric waits for numeric code for this client whose parameters
// after the target start with params, and returns the line.
func (c *tc) expectNumeric(code string, params ...string) string {
	c.t.Helper()
	want := " " + code + " " + c.nick
	for _, p := range params {
		want += " " + p
	}
	return c.expect(want)
}

func TestLineProblem(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{":srv 001 nick :Welcome\r\n", ""},
		{"@time=2026-11-11T00:00:00.000Z;+typing=active :n!u@h PRIVMSG #a :hi\r\n", ""},
		{"PING :srv\r\n", ""},
		{":srv 001 nick :Welcome\n", "does not end in CRLF"},
		{":n!u@h PRIVMSG #a :a\rb\r\n", "holds a CR, LF or NUL"},
		{":n!u@h PRIVMSG #a :a\x00b\r\n", "holds a CR, LF or NUL"},
		{":srv 001\r\n", "numeric without a target"},
		{":srv BAD-CMD x\r\n", `bad command "BAD-CMD"`},
		{":srv MODE #a  +n\r\n", "empty middle parameter"},
		{": PING x\r\n", "empty source"},
		{":n PRIVMSG #a :" + strings.Repeat("x", 500) + "\r\n", "body is 517 bytes with CRLF (limit 512)"},
	} {
		if got := lineProblem(tc.line); got != tc.want {
			t.Errorf("lineProblem(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// goroutineBaseline is called when a test server starts; the returned
// func, called after it stopped, fails the test when goroutines are left
// behind (readers, writers, timers or lookups that never ended).
func goroutineBaseline(t *testing.T) func() {
	before := runtime.NumGoroutine()
	return func() {
		deadline := time.Now().Add(3 * time.Second)
		for {
			n := runtime.NumGoroutine()
			if n <= before {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("goroutine leak: %d running after the server stopped, %d before it started\n%s", n, before, buf)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
