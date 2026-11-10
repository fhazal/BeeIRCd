package server

import (
	"bytes"
	"encoding/base64"
	"strings"

	"github.com/fhazal/beeircd/internal/cloak"
	"github.com/fhazal/beeircd/internal/irc"
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
	if c.accountID != 0 || c.saslAccount != nil {
		s.reply(c, irc.ErrSaslAlready, "You have already authenticated using SASL")
		return
	}
	arg := m.Params[0]
	if arg == "*" {
		c.resetSASL()
		s.reply(c, irc.ErrSaslAborted, "SASL authentication aborted")
		return
	}
	if c.saslMech == "" {
		if !strings.EqualFold(arg, "PLAIN") {
			s.reply(c, irc.RplSaslMechs, "PLAIN", "are available SASL mechanisms")
			s.reply(c, irc.ErrSaslFail, "SASL authentication failed")
			return
		}
		c.saslMech = "PLAIN"
		c.send("AUTHENTICATE +")
		return
	}
	if arg != "+" {
		c.saslBuf.WriteString(arg)
	}
	if c.saslBuf.Len() > saslMax || len(arg) > saslChunk {
		c.resetSASL()
		s.reply(c, irc.ErrSaslTooLong, "SASL message too long")
		return
	}
	if len(arg) == saslChunk {
		return // more is coming
	}
	raw, err := base64.StdEncoding.DecodeString(c.saslBuf.String())
	c.resetSASL()
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
	s.checkPassword(c, a, pw, func(r pwResult) {
		c.saslBusy = false
		switch r {
		case pwOK:
			s.saslDone(c, a)
		case pwLocked:
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
		s.login(c, fresh)
	} else {
		c.saslAccount = fresh
		host := c.host
		if s.cfg.CloakEnabled() {
			host = cloak.Host(s.cfg.Cloak.Secret, s.cfg.Server.Network, c.host, c.ip, c.host == displayIP(c.ip))
		}
		mask := c.target() + "!" + c.ident + "@" + host
		s.reply(c, irc.RplLoggedIn, mask, fresh.Name, "You are now logged in as "+fresh.Name)
	}
	s.reply(c, irc.RplSaslSuccess, "SASL authentication successful")
}

func (c *Client) resetSASL() {
	c.saslMech = ""
	c.saslBuf.Reset()
}
