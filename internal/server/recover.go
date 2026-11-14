package server

import (
	"fmt"
	"runtime/debug"
)

// A bug that panics while one connection's line is handled must not stop the
// whole server. The loop runs every job through runSafe; a client or link
// line that panics drops only that connection, logs the stack and tells the
// opers.

// runSafe runs one loop job. A panic is logged and reported; the loop keeps
// going.
func (s *Server) runSafe(f func()) {
	defer func() {
		if r := recover(); r != nil {
			s.reportPanic("loop job", r)
		}
	}()
	f()
}

// handleLineSafe handles one client line; a panic drops that client only.
func (s *Server) handleLineSafe(c *Client, line string) {
	defer func() {
		if r := recover(); r != nil {
			s.reportPanic(fmt.Sprintf("line from %s (%s)", c.target(), c.ip), r)
			if !c.closed {
				s.quit(c, "Internal server error")
			}
		}
	}()
	s.handleLine(c, line)
}

// handleLinkLineSafe handles one server-link line; a panic drops that link only.
func (s *Server) handleLinkLineSafe(l *linkConn, line string) {
	defer func() {
		if r := recover(); r != nil {
			s.reportPanic("link line", r)
			s.dropLink(l, "Internal server error")
		}
	}()
	s.handleLinkLine(l, line)
}

func (s *Server) reportPanic(where string, r any) {
	s.log.Printf("PANIC in %s: %v\n%s", where, r, debug.Stack())
	func() {
		defer func() { _ = recover() }() // never panic while reporting one
		s.snotice("Internal error in %s: %v (the server kept running; see the log)", where, r)
	}()
}
