package server

import (
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

type handler func(s *Server, c *Client, m irc.Message)

type command struct {
	min    int  // parameters needed
	preReg bool // allowed before registration
	run    handler
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"CAP":          {1, true, (*Server).cmdCap},
		"NICK":         {0, true, (*Server).cmdNick},
		"USER":         {4, true, (*Server).cmdUser},
		"PASS":         {1, true, (*Server).cmdPass},
		"PING":         {0, true, (*Server).cmdPing},
		"PONG":         {0, true, (*Server).cmdPong},
		"QUIT":         {0, true, (*Server).cmdQuit},
		"JOIN":         {1, false, (*Server).cmdJoin},
		"PART":         {1, false, (*Server).cmdPart},
		"PRIVMSG":      {0, false, (*Server).cmdPrivmsg},
		"NOTICE":       {0, false, (*Server).cmdNotice},
		"TOPIC":        {1, false, (*Server).cmdTopic},
		"NAMES":        {0, false, (*Server).cmdNames},
		"LIST":         {0, false, (*Server).cmdList},
		"MODE":         {1, false, (*Server).cmdMode},
		"KICK":         {2, false, (*Server).cmdKick},
		"INVITE":       {2, false, (*Server).cmdInvite},
		"WHO":          {0, false, (*Server).cmdWho},
		"WHOIS":        {1, false, (*Server).cmdWhois},
		"WHOWAS":       {1, false, (*Server).cmdWhowas},
		"AWAY":         {0, false, (*Server).cmdAway},
		"ISON":         {1, false, (*Server).cmdIson},
		"USERHOST":     {1, false, (*Server).cmdUserhost},
		"MOTD":         {0, false, (*Server).cmdMotd},
		"LUSERS":       {0, false, (*Server).cmdLusers},
		"VERSION":      {0, false, (*Server).cmdVersion},
		"TIME":         {0, false, (*Server).cmdTime},
		"TAGMSG":       {1, false, (*Server).cmdTagmsg},
		"AUTHENTICATE": {1, true, (*Server).cmdAuthenticate},
		"NS":           {1, false, (*Server).cmdNS},
		"NICKSERV":     {1, false, (*Server).cmdNS},
		"MS":           {1, false, (*Server).cmdMS},
		"MEMOSERV":     {1, false, (*Server).cmdMS},
		"CS":           {1, false, (*Server).cmdCS},
		"CHANSERV":     {1, false, (*Server).cmdCS},
		"OS":           {1, false, (*Server).cmdOS},
		"OPERSERV":     {1, false, (*Server).cmdOS},
		"AS":           {1, false, (*Server).cmdAS},
		"ADMINSERV":    {1, false, (*Server).cmdAS},
		"OPER":         {2, false, (*Server).cmdOper},
		"KILL":         {1, false, (*Server).cmdKill},
		"WALLOPS":      {1, false, (*Server).cmdWallops},
	}
}

// handleLine runs one line from c.
func (s *Server) handleLine(c *Client, line string) {
	if c.closed {
		return
	}
	c.lastRecv = time.Now()
	c.pingSent = time.Time{}
	m, err := irc.Parse(line)
	if err != nil {
		return
	}
	if label := m.Tags["label"]; label != "" && c.caps["labeled-response"] {
		held := []string{}
		c.capture = &held
		s.runCommand(c, m)
		if c.capture != nil { // nil when the command closed the connection
			c.capture = nil
			s.flushLabeled(c, label, held)
		}
		return
	}
	s.runCommand(c, m)
}

func (s *Server) runCommand(c *Client, m irc.Message) {
	cmd, ok := commands[m.Command]
	switch {
	case !ok && !c.registered:
		s.reply(c, irc.ErrNotRegistered, "You have not registered")
	case !ok:
		s.reply(c, irc.ErrUnknownCommand, m.Command, "Unknown command")
	case !c.registered && !cmd.preReg:
		s.reply(c, irc.ErrNotRegistered, "You have not registered")
	case len(m.Params) < cmd.min:
		s.reply(c, irc.ErrNeedMoreParams, m.Command, "Not enough parameters")
	case c.registered && !shunAllowed[m.Command] && s.svc.Shunned(c.user):
		// Shunned: dropped without a word (UnrealIRCd).
	default:
		if c.registered && m.Command == "PART" && len(m.Params) > 1 && s.svc.Shunned(c.user) {
			m.Params = m.Params[:1] // no part message either
		}
		cmd.run(s, c, m)
	}
}
