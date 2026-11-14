package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// A job that panics must not stop the loop: the server keeps serving.
func TestPanicDoesNotStopServer(t *testing.T) {
	s := testServer(t, nil)
	a := login(t, s, "alice")
	done := make(chan struct{})
	s.post(func() { defer close(done); panic("test panic") })
	<-done
	a.send("PING :still-here")
	a.expect("PONG")
	b := login(t, s, "bob")
	b.send("PRIVMSG alice :hi")
	a.expect("PRIVMSG alice :hi")
}

// A line that panics drops only the person who sent it, with an error;
// everyone else carries on (release test 1.0.0).
func TestPanicLineDropsOnlyThatClient(t *testing.T) {
	commands["PANICTEST"] = command{0, true, func(*Server, *Client, irc.Message) { panic("bad line") }}
	defer delete(commands, "PANICTEST")
	s := testServer(t, nil)
	a, b := login(t, s, "alice"), login(t, s, "bob")
	b.send("PANICTEST")
	b.expect("ERROR :Closing Link: 127.0.0.1 (Internal server error)")
	a.send("PING :still-here")
	a.expect("PONG irc.test.local :still-here")
	c := login(t, s, "carol")
	c.send("PRIVMSG alice :hi")
	a.expect(":carol!~carol@127.0.0.1 PRIVMSG alice :hi")
}
