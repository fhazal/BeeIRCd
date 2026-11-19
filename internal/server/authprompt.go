package server

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// Soft bans and the login prompt, like UnrealIRCd's soft bans and its
// authprompt module. An AKILL or K-line written with a % before the mask
// (or a DNSBL with action "soft-reject") stops only people who aren't
// logged in. Someone it stops is held at connect, asked to log in with
// SASL or /AUTH nick:password, and let in once they do; after
// [authprompt] timeout they get the ban message.

// authHowTo is the second notice of the prompt.
const authHowTo = "Turn on SASL in your client, or type now: /AUTH nick:password"

// authPrompt holds c (stopped by a soft ban with this reason) for a login,
// or refuses it when the prompt is off.
func (s *Server) authPrompt(c *Client, reason string) {
	ap := s.cfg.AuthPrompt
	if !ap.On() {
		s.reply(c, irc.ErrYoureBannedCreep, s.cfg.Messages.BanText("You are banned from this network: "+reason+" (log in with SASL to connect)"))
		s.quit(c, "Banned: "+reason)
		return
	}
	c.authHeld = true
	c.authReason = reason
	c.authUntil = time.Now().Add(ap.Wait())
	s.notice(c, "*** "+ap.Text())
	s.notice(c, "*** "+authHowTo)
}

// authExpired ends a hold that ran out (from checkTimeouts).
func (s *Server) authExpired(c *Client) {
	c.authHeld = false
	s.reply(c, irc.ErrYoureBannedCreep, s.cfg.Messages.BanText("You are banned from this network: "+c.authReason))
	s.quit(c, "Banned: "+c.authReason)
}

// cmdAuth is /AUTH nick:password (or /AUTH nick password) from someone
// held by a soft ban.
func (s *Server) cmdAuth(c *Client, m irc.Message) {
	if c.registered {
		s.notice(c, "*** You are already connected. To log in, use /msg NickServ IDENTIFY.")
		return
	}
	if !c.authHeld {
		s.notice(c, "*** /AUTH is for logging in when the server asks for it.")
		return
	}
	if c.saslBusy || c.saslMech != "" {
		return // the last try is still being checked
	}
	nick, pw, ok := strings.Cut(m.Params[0], ":")
	if !ok && len(m.Params) > 1 {
		nick, pw, ok = m.Params[0], m.Params[1], true
	}
	if !ok || nick == "" || pw == "" {
		s.notice(c, "*** Type /AUTH nick:password")
		return
	}
	fail := func(why string) {
		if strings.Contains(why, "too many wrong passwords") || strings.Contains(why, "suspended") || strings.Contains(why, "maximum number") {
			s.notice(c, "*** "+strings.TrimPrefix(why, "SASL authentication failed: "))
			return
		}
		s.notice(c, "*** "+s.cfg.AuthPrompt.Fail())
	}
	if s.svc.Off() {
		// Services run on another server: send it as SASL PLAIN.
		payload := base64.StdEncoding.EncodeToString([]byte("\x00" + nick + "\x00" + pw))
		if len(payload) >= saslChunk {
			fail("")
			return
		}
		c.authCmd = true
		s.saslRelay(c, "PLAIN")
		if c.saslMech == "" {
			c.authCmd = false
			s.notice(c, "*** Logging in isn't possible right now: services are unavailable. Try again in a minute.")
			return
		}
		c.authRelay = payload
		return
	}
	c.authCmd = true
	s.plainLogin(c, nick, pw, func(why string) {
		c.authCmd = false
		if why != "" {
			fail(why)
			return
		}
		s.tryRegister(c)
	})
}

// authRelayAnswer is the services server's answer to a relayed /AUTH; it
// reports whether it was one.
func (s *Server) authRelayAnswer(c *Client, mode, data string) bool {
	if !c.authCmd {
		return false
	}
	switch mode {
	case "C":
		if p := c.authRelay; p != "" && data == "+" {
			c.authRelay = ""
			s.saslRelay(c, p)
		}
	case "D":
		c.resetSASL()
		c.authCmd = false
		if data == "S" && c.saslAccount != nil {
			c.authHeld = false
			s.tryRegister(c)
			return true
		}
		if c.authHeld {
			s.notice(c, "*** "+s.cfg.AuthPrompt.Fail())
		}
	}
	return true
}
