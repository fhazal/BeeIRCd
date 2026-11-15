package server

import (
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
)

// IRCv3 draft/account-registration: REGISTER <account> <email|*>
// <password> and VERIFY <account> <code>, answered with REGISTER/VERIFY
// SUCCESS, VERIFICATION_REQUIRED or FAIL. Offered only where services run
// (the services server); it works once the connection is complete.

const accountRegCap = "draft/account-registration"

func (s *Server) accountRegValue() string {
	if s.svc.RegEmailRequired() {
		return accountRegCap + "=email-required"
	}
	return accountRegCap
}

func (s *Server) regFail(c *Client, cmd, code string, params ...string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{cmd, code}, params...), Trailing: true}.String())
}

// regStart checks what both commands need; false when it answered.
func (s *Server) regStart(c *Client, cmd string, m irc.Message, need int) bool {
	switch {
	case !c.caps[accountRegCap]:
		if c.registered {
			s.reply(c, irc.ErrUnknownCommand, cmd, "Unknown command")
		} else {
			s.reply(c, irc.ErrNotRegistered, "You have not registered")
		}
		return false
	case len(m.Params) < need:
		s.regFail(c, cmd, "NEED_MORE_PARAMS", "Missing parameters")
		return false
	case !c.registered:
		s.regFail(c, cmd, "COMPLETE_CONNECTION_REQUIRED", m.Params[0], "Finish connecting first, then register")
		return false
	}
	return true
}

func (s *Server) regAnswer(c *Client, cmd string, r services.RegResult) {
	if c.closed {
		return
	}
	switch r.Code {
	case "SUCCESS", "VERIFICATION_REQUIRED":
		c.send(irc.Message{Source: s.cfg.Server.Name, Command: cmd, Params: []string{r.Code, r.Account, r.Text}, Trailing: true}.String())
	default:
		s.regFail(c, cmd, r.Code, r.Account, r.Text)
	}
}

// cmdRegister is REGISTER <account> <email|*> <password>.
func (s *Server) cmdRegister(c *Client, m irc.Message) {
	if !s.regStart(c, "REGISTER", m, 3) {
		return
	}
	s.svc.RegisterAccount(c.uid, m.Params[0], m.Params[1], m.Params[2], func(r services.RegResult) { s.regAnswer(c, "REGISTER", r) })
	s.svc.Flush()
}

// cmdVerify is VERIFY <account> <code>.
func (s *Server) cmdVerify(c *Client, m irc.Message) {
	if !s.regStart(c, "VERIFY", m, 2) {
		return
	}
	s.svc.VerifyAccount(c.uid, m.Params[0], m.Params[1], func(r services.RegResult) { s.regAnswer(c, "VERIFY", r) })
	s.svc.Flush()
}
