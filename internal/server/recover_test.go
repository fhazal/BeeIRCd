package server

import "testing"

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
