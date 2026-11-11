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
		"CHATHISTORY":  {1, false, (*Server).cmdChathistory},
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
		"HS":           {1, false, (*Server).cmdHS},
		"HOSTSERV":     {1, false, (*Server).cmdHS},
		"ADMINSERV":    {1, false, (*Server).cmdAS},
		"OPER":         {2, false, (*Server).cmdOper},
		"KILL":         {1, false, (*Server).cmdKill},
		"HELPOP":       {0, false, (*Server).cmdHelp},
		"HELP":         {0, false, (*Server).cmdHelp},
		"INFO":         {0, false, (*Server).cmdInfo},
		"WALLOPS":      {1, false, (*Server).cmdWallops},
		"SILENCE":      {0, false, (*Server).cmdSilence},
		"KNOCK":        {1, false, (*Server).cmdKnock},
		"SETNAME":      {1, false, (*Server).cmdSetname},
		"CHGNAME":      {2, false, (*Server).cmdChgname},
		"SETHOST":      {1, false, (*Server).cmdSethost},
		"SETIDENT":     {1, false, (*Server).cmdSetident},
		"CHGHOST":      {2, false, (*Server).cmdChghost},
		"CHGIDENT":     {2, false, (*Server).cmdChgident},
		"SAJOIN":       {2, false, (*Server).cmdSajoin},
		"REHASH":       {0, false, (*Server).cmdRehash},
		"SAPART":       {2, false, (*Server).cmdSapart},
		"SANICK":       {2, false, (*Server).cmdSanick},
		"SAMODE":       {2, false, (*Server).cmdSamode},
		"SATOPIC":      {2, false, (*Server).cmdSatopic},
	}
}

// handleLine runs one line from c.
func (s *Server) handleLine(c *Client, line string) {
	if c.closed {
		return
	}
	c.lastRecv = time.Now()
	c.pingSent = time.Time{}
	// A client's commands run in the order it sent them: while one of its
	// passwords is being checked (off the loop), what follows waits (O1).
	if len(c.heldLines) > 0 || s.passwordBusy(c) {
		if len(c.heldLines) >= maxHeldLines {
			s.quit(c, "Excess flood")
			return
		}
		c.heldLines = append(c.heldLines, line)
		s.held[c] = struct{}{}
		return
	}
	s.runLine(c, line)
}

// maxHeldLines is how many lines one client may send while its password is
// checked; normal flood control slows a sender long before this.
const maxHeldLines = 512

// passwordBusy reports whether a password check or hash runs for c, here
// (SASL, /oper) or in services (IDENTIFY, REGISTER, ...).
func (s *Server) passwordBusy(c *Client) bool {
	return c.pwBusy || (c.user != nil && s.svc.PasswordBusy(c.uid))
}

// releaseHeld runs the held lines of every client whose check finished,
// in order, until one of them starts another check.
func (s *Server) releaseHeld() {
	for c := range s.held {
		for len(c.heldLines) > 0 && !c.closed && !s.passwordBusy(c) {
			line := c.heldLines[0]
			c.heldLines = c.heldLines[1:]
			s.runLine(c, line)
		}
		if c.closed || len(c.heldLines) == 0 {
			c.heldLines = nil
			delete(s.held, c)
		}
	}
}

// runLine parses and runs one line from c.
func (s *Server) runLine(c *Client, line string) {
	if c.closed {
		return
	}
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
