package server

import (
	"bytes"
	"encoding/base64"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/cloak"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	saslChunk = 400  // AUTHENTICATE lines carry at most this much
	saslMax   = 8192 // a whole SASL message, decoded or not
)

// cmdAuthenticate is SASL PLAIN: the client sends account and password
// during connect, so it is identified before it joins anything.
func (s *Server) cmdAuthenticate(c *Client, m irc.Message) {
	if !c.caps["sasl"] {
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		return
	}
	if s.accountID(c) != 0 || c.saslAccount != nil {
		s.reply(c, irc.ErrSaslAlready, "You have already authenticated using SASL")
		return
	}
	arg := m.Params[0]
	if s.svc.Off() {
		s.saslRelay(c, arg) // services run on another server
		return
	}
	if arg == "*" {
		c.resetSASL()
		s.reply(c, irc.ErrSaslAborted, "SASL authentication aborted")
		return
	}
	if c.saslMech == "" {
		switch mech := strings.ToUpper(arg); mech {
		case "PLAIN", "EXTERNAL":
			c.saslMech = mech
			c.send("AUTHENTICATE +")
		default:
			s.reply(c, irc.RplSaslMechs, "PLAIN,EXTERNAL", "are available SASL mechanisms")
			s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		}
		return
	}
	if arg != "+" {
		c.saslBuf.WriteString(arg)
	}
	// One line over 400 bytes is 905; a whole message over the limit is
	// a plain failure (904), as irctest, Ergo and InspIRCd have it.
	if len(arg) > saslChunk {
		c.resetSASL()
		s.reply(c, irc.ErrSaslTooLong, "SASL message too long")
		return
	}
	if c.saslBuf.Len() > saslMax {
		c.resetSASL()
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed: message too long")
		return
	}
	if len(arg) == saslChunk {
		return // more is coming
	}
	raw, err := base64.StdEncoding.DecodeString(c.saslBuf.String())
	mech := c.saslMech
	c.resetSASL()
	if mech == "EXTERNAL" {
		s.saslExternal(c, string(raw), err)
		return
	}
	parts := bytes.Split(raw, []byte{0})
	if err != nil || len(parts) != 3 || len(parts[1]) == 0 {
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		return
	}
	authz, authc, pw := string(parts[0]), string(parts[1]), string(parts[2])
	if authz != "" && !irc.EqualFold(authz, authc) {
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		return
	}
	a, err := s.store.AccountByNick(authc)
	if err != nil || a == nil {
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		return
	}
	c.saslBusy = true
	s.checkSecret(c, a.ID, a.PassHash, pw, func(r services.PwResult) {
		c.saslBusy = false
		switch r {
		case services.PwOK:
			s.saslDone(c, a)
		case services.PwLocked:
			s.reply(c, irc.ErrSaslFail, "SASL authentication failed: too many wrong passwords, try again later")
		default:
			s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		}
		s.tryRegister(c)
	})
}

func (s *Server) saslDone(c *Client, a *store.Account) {
	fresh, err := s.store.AccountByID(a.ID)
	if err != nil || fresh == nil {
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
		return
	}
	if c.registered {
		s.svc.LoginSASL(c.uid, fresh) // sends 900 and 903
		return
	}
	c.saslAccount = fresh
	host := c.host
	if s.cfg.CloakEnabled() {
		host = cloak.Host(s.cfg.Cloak.Secret, s.cfg.Server.Network, c.host, c.ip, c.host == irc.DisplayIP(c.ip))
	}
	mask := c.target() + "!" + c.ident + "@" + host
	s.reply(c, irc.RplLoggedIn, mask, fresh.Name, "You are now logged in as "+fresh.Name)
	s.reply(c, irc.RplSaslSuccess, "SASL authentication successful")
}

// saslExternal logs in with the TLS client certificate: the account whose
// NickServ CERT list has its fingerprint. authz, when given, must be that
// account.
func (s *Server) saslExternal(c *Client, authz string, decodeErr error) {
	who := c.host + " (" + c.ip + ")"
	a := s.store.AccountByCert(c.certfp)
	switch {
	case decodeErr != nil || c.certfp == "":
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
	case a == nil:
		s.svc.LogSASLExternal(who, "", c.certfp, false)
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
	case authz != "" && !irc.EqualFold(authz, a.Name) || a.Suspended(time.Now()):
		s.svc.LogSASLExternal(who, "", c.certfp, false)
		s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
	default:
		s.svc.LogSASLExternal(who, a.Name, c.certfp, true)
		s.saslDone(c, a)
	}
	s.tryRegister(c)
}

func (c *Client) resetSASL() {
	c.saslMech = ""
	c.saslBuf.Reset()
}
