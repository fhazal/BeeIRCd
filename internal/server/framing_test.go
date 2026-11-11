package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// S1 regression tests (audit 11 Nov 2026): line framing over real TCP.

// sendRaw writes bytes exactly as given (no CRLF added).
func (c *tc) sendRaw(b string) {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(b)); err != nil {
		c.t.Fatalf("%s raw send: %v", c.nick, err)
	}
}

func TestFramingCRLFAndLF(t *testing.T) {
	s := testServer(t, nil)
	a, b := login(t, s, "pa"), login(t, s, "pb")
	joinAll("#p", a, b)
	a.sendRaw("PRIVMSG #p :crlf\r\n")
	b.expect(":pa!~pa@127.0.0.1 PRIVMSG #p :crlf")
	a.sendRaw("PRIVMSG #p :bare lf\n")
	b.expect(":pa!~pa@127.0.0.1 PRIVMSG #p :bare lf")
	a.sendRaw("\r\n\r\nPRIVMSG #p :after empty lines\r\n")
	b.expect("PRIVMSG #p :after empty lines")
	// A line split over several TCP writes still arrives whole.
	a.sendRaw("PRIVMSG #p :in ")
	a.sendRaw("two parts\r\n")
	b.expect("PRIVMSG #p :in two parts")
}

func TestFramingBareCRCannotForge(t *testing.T) {
	s := testServer(t, nil)
	a, b := login(t, s, "pa"), login(t, s, "pb")
	joinAll("#p", a, b)
	a.sendRaw("PRIVMSG #p :hello\r:evil!x@y PRIVMSG #p :forged\r\n")
	line := b.expect("PRIVMSG #p :hello")
	if strings.ContainsAny(line, "\r\x00") || strings.Contains(line, "evil") {
		t.Fatalf("first line carries more than hello: %q", line)
	}
	// The part after the CR is a line of its own from pa: the source a
	// client sends is never trusted.
	b.expect(":pa!~pa@127.0.0.1 PRIVMSG #p :forged")
	a.send("PRIVMSG #p :end")
	b.quiet("evil!x@y")
}

func TestFramingNULDropsTheLine(t *testing.T) {
	s := testServer(t, nil)
	a, b := login(t, s, "pa"), login(t, s, "pb")
	joinAll("#p", a, b)
	a.sendRaw("PRIVMSG #p :bad\x00byte\r\nPRIVMSG #p :good\r\n")
	line := b.expect("PRIVMSG #p :")
	if !strings.HasSuffix(line, ":good") {
		t.Fatalf("a line with NUL got through: %q", line)
	}
	// TOPIC and other commands are the same path.
	a.sendRaw("TOPIC #p :t\x00x\r\n")
	a.send("TOPIC #p")
	a.expectNumeric("331", "#p", ":No topic is set")
}

func TestFramingCleansServerText(t *testing.T) {
	// Text that doesn't come from a client (here the MOTD from the config,
	// written on Windows) can't break a line either.
	s := testServer(t, func(c *config.Config) { c.MOTD.Text = "first\r\nsecond\rhalf" })
	c := raw(t, s)
	c.nick = "m"
	c.send("NICK m")
	c.send("USER m 0 * :M")
	for _, want := range []string{" 372 m :- first", " 372 m :- second half"} {
		if line := c.expect(want); strings.ContainsAny(line, "\r\x00") {
			t.Fatalf("MOTD line kept a framing byte: %q", line)
		}
	}
}
