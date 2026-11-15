package server

import (
	"context"
	"testing"
)

// A session exception with limit 0 means no limit, as in Anope (found
// when SKINTERNET moved over: Anope's "0" exceptions locked people out).
func TestSessionExceptionZeroIsUnlimited(t *testing.T) {
	s, _, _ := fileServerWith(t, "", func(s *Server) { s.cfg.Limits.SessionLimit = 3 })
	b := boss(t, s)
	b.send("OS EXCEPTION ADD 127.0.0.1 0 the bridge")
	b.expect("127.0.0.1 may now have any number of connections.")
	b.send("OS EXCEPTION LIST")
	b.expect("127.0.0.1            unlimited")
	for _, n := range []string{"one", "two", "three", "four"} {
		login(t, s, n) // boss + 4 more from 127.0.0.1, over the limit of 3
	}
}

// An exception on a host name lets in someone over their IP's limit once
// their hostname is known; without one they are still refused.
func TestSessionExceptionOnHostName(t *testing.T) {
	on := true
	s, _, _ := fileServerWith(t, "", func(s *Server) {
		s.cfg.Server.ResolveHosts = &on
		s.cfg.Limits.SessionLimit = 2
		s.resolve = func(ctx context.Context, ip string) (string, bool) { return "bnc.example.org", true }
	})
	b := boss(t, s)
	b.send("OS EXCEPTION ADD *.example.org 10 the bouncer")
	b.expect("*.example.org may now have 10 connections.")
	for _, n := range []string{"one", "two", "three"} {
		login(t, s, n)
	}
	b.send("OS EXCEPTION DEL *.example.org")
	b.expect("*.example.org")
	// With no host name exception left the refusal comes at connect, before
	// anything is sent: read it rather than write into a closed socket.
	c := raw(t, s)
	c.expect("ERROR :Closing Link: 127.0.0.1 (Too many connections from your IP (the limit is 2))")
}
