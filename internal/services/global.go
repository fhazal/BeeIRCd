package services

import (
	"strconv"
	"strings"
)

// Global's commands and OperServ GLOBAL, as in Anope 2.1 (gl_global,
// gl_queue, gl_server) and OperServ RELOAD (os_reload).

// maxGlobalQueue is Anope's default for how many lines a queue holds.
const maxGlobalQueue = 10

func globalCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"GLOBAL": {"GLOBAL [text]", "a notice to everyone",
			"Sends a notice from Global to everyone on the network. Without text it\nsends your queue, one line at a time, and empties it.\nExample: /msg Global global The server restarts at 22:00 UTC.", glGlobal},
		"QUEUE": {"QUEUE ADD <text> | DEL <num> | LIST | CLEAR", "build a notice of several lines",
			"Your own queue of lines, kept until you quit, up to 10.\nQUEUE ADD <text>   adds a line\nQUEUE DEL <num>    drops a line (numbers from LIST)\nQUEUE LIST         shows the lines\nQUEUE CLEAR        empties it\nThen GLOBAL (or SERVER <server>) with no text sends the lines in order.", glQueue},
		"SERVER": {"SERVER <server> [text]", "a notice to one server",
			"Like GLOBAL, but only to the people on that server. Without text it sends\nyour queue to them.\nExample: /msg Global server leaf.example.org Restarting in 5 minutes.", glServer},
	}
}

func osGlobal(s *Services, c *Client, args []string) { s.globalRun(c, s.operServ, args) }
func glGlobal(s *Services, c *Client, args []string) { s.globalRun(c, s.global, args) }

// globalRun is GLOBAL, from OperServ or from Global; sv is who answers.
func (s *Services) globalRun(c *Client, sv *service, args []string) {
	if !s.needFlag(c, sv, 'g') {
		return
	}
	lines := []string{strings.Join(args, " ")}
	if lines[0] == "" {
		if len(c.globalQueue) == 0 {
			s.say(c, sv, "Syntax: GLOBAL [text]")
			return
		}
		lines = c.globalQueue
		c.globalQueue = nil
	}
	s.globalSend(c, sv, "", lines, "GLOBAL")
}

func glServer(s *Services, c *Client, args []string) {
	sv := s.global
	if !s.needFlag(c, sv, 'g') {
		return
	}
	if len(args) == 0 {
		s.say(c, sv, "Syntax: SERVER <server> [text]")
		return
	}
	name := args[0]
	if !s.serverKnown(name) {
		s.say(c, sv, "No server named %s.", name)
		return
	}
	lines := []string{strings.Join(args[1:], " ")}
	if lines[0] == "" {
		if len(c.globalQueue) == 0 {
			s.say(c, sv, "Your queue is empty. Syntax: SERVER <server> [text]")
			return
		}
		lines = c.globalQueue
		c.globalQueue = nil
	}
	s.globalSend(c, sv, name, lines, "SERVER "+name)
}

// serverKnown reports whether name is this server or
// the server of someone online (the network keeps no list of servers).
func (s *Services) serverKnown(name string) bool {
	if strings.EqualFold(name, s.cfg.Server.Name) {
		return true
	}
	for _, oc := range s.clients {
		if !oc.closed && strings.EqualFold(oc.user.Server, name) {
			return true
		}
	}
	return false
}

// globalSend gives every line, in order, to everyone online, or only to
// those on server when it is not "". It audits, logs and tells c how many
// people got it.
func (s *Services) globalSend(c *Client, sv *service, server string, lines []string, what string) {
	n := 0
	for _, oc := range s.clients {
		if oc.closed || (server != "" && !strings.EqualFold(oc.user.Server, server)) {
			continue
		}
		for _, l := range lines {
			s.host.Notice(s.global.user.UID, oc.user.UID, l)
		}
		n++
	}
	s.audit(c, sv, "%s %s", what, strings.Join(lines, " | "))
	s.logCmd(c, sv, logAdmin, "", "")
	if server == "" {
		s.say(c, sv, "Sent to everyone online (%s).", plural(n, "person"))
	} else {
		s.say(c, sv, "Sent to everyone on %s (%s).", server, plural(n, "person"))
	}
}

func glQueue(s *Services, c *Client, args []string) {
	sv := s.global
	if !s.needFlag(c, sv, 'g') {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "ADD":
		text := strings.Join(args[1:], " ")
		switch {
		case text == "":
			s.say(c, sv, "Syntax: QUEUE ADD <text>")
		case len(c.globalQueue) >= maxGlobalQueue:
			s.say(c, sv, "Your queue has %d lines already, the most. Send it with GLOBAL or clear it with QUEUE CLEAR.", maxGlobalQueue)
		default:
			c.globalQueue = append(c.globalQueue, text)
			s.say(c, sv, "Added line %d to your queue.", len(c.globalQueue))
		}
	case "DEL":
		n, err := strconv.Atoi(argAt(args, 1))
		switch {
		case argAt(args, 1) == "":
			s.say(c, sv, "Syntax: QUEUE DEL <num>")
		case err != nil || n < 1 || n > len(c.globalQueue):
			s.say(c, sv, "No line %s in your queue.", args[1])
		default:
			c.globalQueue = append(c.globalQueue[:n-1:n-1], c.globalQueue[n:]...)
			s.say(c, sv, "Deleted line %d from your queue.", n)
		}
	case "LIST":
		if len(c.globalQueue) == 0 {
			s.say(c, sv, "Your queue is empty.")
			return
		}
		for i, l := range c.globalQueue {
			s.say(c, sv, "%d. %s", i+1, l)
		}
	case "CLEAR":
		c.globalQueue = nil
		s.say(c, sv, "Your queue is empty now.")
	default:
		s.say(c, sv, "Syntax: QUEUE ADD <text> | DEL <num> | LIST | CLEAR")
	}
}

// osReload is OperServ RELOAD: read beeircd.conf again, like /REHASH.
func osReload(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.operServ, 'a') {
		return
	}
	if err := s.host.Rehash(c.user.Nick); err != nil {
		s.os(c, "%v", err)
		return
	}
	s.audit(c, s.operServ, "RELOAD")
	s.osLog(c, "")
	s.os(c, "Reloaded the config.")
}
