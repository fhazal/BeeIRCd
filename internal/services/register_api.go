package services

import (
	"errors"
	"strconv"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// IRCv3 draft/account-registration: REGISTER and VERIFY from a client's
// own sign-up screen. They follow NickServ REGISTER and CONFIRM (the same
// rules: force_email, registration, reg_delay, forbidden names), with
// answers a client can read.

// RegResult answers REGISTER or VERIFY. Code is SUCCESS,
// VERIFICATION_REQUIRED or a FAIL code (ACCOUNT_EXISTS, WEAK_PASSWORD...).
type RegResult struct {
	Code    string
	Account string
	Text    string
}

// RegEmailRequired reports whether REGISTER needs an email here.
func (s *Services) RegEmailRequired() bool {
	return s.cfg.Services.ForceEmailOn() || s.regMode() == "mail"
}

// RegisterAccount registers uid's nick (account must be it, or "*") with
// email ("*" for none) and password; done gets the answer, on the loop.
func (s *Services) RegisterAccount(uid, account, email, pw string, done func(RegResult)) {
	c := s.clients[uid]
	if c == nil || s.off {
		done(RegResult{Code: "TEMPORARILY_UNAVAILABLE", Account: account, Text: "Services are not available right now"})
		return
	}
	u := c.user
	if account == "*" {
		account = u.Nick
	}
	fail := func(code, text string) { done(RegResult{Code: code, Account: account, Text: text}) }
	switch {
	case !s.LocalPasswords():
		fail("TEMPORARILY_UNAVAILABLE", "Accounts on this network are made somewhere else")
		return
	case s.DefconLevel() <= 4:
		fail("TEMPORARILY_UNAVAILABLE", "New registrations are paused on this network")
		return
	case c.accountID != 0:
		fail("ALREADY_AUTHENTICATED", "You are logged in already")
		return
	case !irc.EqualFold(account, u.Nick):
		fail("ACCOUNT_NAME_MUST_BE_NICK", "The account name must be your current nick")
		return
	}
	if d := s.cfg.Services.RegDelayOr(); d > 0 && !c.isOper() && time.Now().Unix()-u.NickTS < int64(d.Seconds()) {
		fail("TEMPORARILY_UNAVAILABLE", "You must have been using this nick for at least "+strconv.Itoa(int(d.Seconds()))+" seconds to register")
		return
	}
	if a, err := s.store.AccountByNick(u.Nick); err != nil {
		fail("TEMPORARILY_UNAVAILABLE", "Something went wrong on the server")
		return
	} else if a != nil {
		fail("ACCOUNT_EXISTS", "This account is registered already")
		return
	}
	if why := s.registerForbidden(u.Nick); why != "" {
		fail("BAD_ACCOUNT_NAME", why)
		return
	}
	switch {
	case len(pw) < minPasswordLen:
		fail("WEAK_PASSWORD", "Passwords need at least "+strconv.Itoa(minPasswordLen)+" characters")
		return
	case irc.EqualFold(pw, u.Nick):
		fail("UNACCEPTABLE_PASSWORD", "Your password can't be your nick")
		return
	}
	if email == "*" {
		email = ""
	}
	switch {
	case email == "" && s.RegEmailRequired():
		fail("INVALID_EMAIL", "An email address is needed to register here")
		return
	case email != "" && !validEmail(email):
		fail("INVALID_EMAIL", "That doesn't look like an email address")
		return
	case email != "":
		if why := s.emailForbidden(email); why != "" {
			fail("UNACCEPTABLE_EMAIL", why)
			return
		}
	}
	if c.pwBusy {
		fail("TEMPORARILY_UNAVAILABLE", "Still busy with your last password")
		return
	}
	nick := u.Nick
	s.HashSecret(&c.pwBusy, func() bool { return c.closed }, pw, func(hash string, err error) {
		if err != nil {
			fail("TEMPORARILY_UNAVAILABLE", "Something went wrong on the server")
			return
		}
		if c.user.Nick != nick {
			fail("ACCOUNT_NAME_MUST_BE_NICK", "Your nick changed while registering")
			return
		}
		a, err := s.store.CreateAccount(nick, hash, email, time.Now())
		if errors.Is(err, store.ErrExists) {
			fail("ACCOUNT_EXISTS", "This account was just registered by someone else")
			return
		}
		if err != nil {
			fail("TEMPORARILY_UNAVAILABLE", "Something went wrong on the server")
			return
		}
		if n := s.cfg.Services.MaxMemos; n != store.DefaultMemoLimit {
			a.MemoLimit = n
			s.store.SaveAccount(a)
		}
		s.log.Printf("registered nick %s (%s)", nick, c.ip)
		c.cmdName = "REGISTER"
		s.nsLogCmd(c, "to register %s (email: %s)", nick, orNone(email))
		s.login(c, a)
		s.afterRegister(c, a)
		switch s.regMode() {
		case "mail":
			done(RegResult{Code: "VERIFICATION_REQUIRED", Account: nick, Text: "A code has been sent to " + email + ": VERIFY " + nick + " <code>"})
		case "admin":
			done(RegResult{Code: "SUCCESS", Account: nick, Text: "Registered; an administrator will confirm your account"})
		default:
			done(RegResult{Code: "SUCCESS", Account: nick, Text: "Account created"})
		}
		s.Flush()
	})
}

// VerifyAccount checks the code a registration mail gave (NickServ
// CONFIRM <passcode>) for uid's account.
func (s *Services) VerifyAccount(uid, account, code string, done func(RegResult)) {
	c := s.clients[uid]
	if c == nil || s.off {
		done(RegResult{Code: "TEMPORARILY_UNAVAILABLE", Account: account, Text: "Services are not available right now"})
		return
	}
	a, _ := s.store.AccountByID(c.accountID)
	switch {
	case a == nil || !irc.EqualFold(a.Name, account):
		done(RegResult{Code: "INVALID_CODE", Account: account, Text: "Register the account on this connection first"})
	case !a.Unconfirmed:
		done(RegResult{Code: "SUCCESS", Account: a.Name, Text: "Your account is confirmed already"})
	case a.ConfirmCode == "" || code != a.ConfirmCode:
		done(RegResult{Code: "INVALID_CODE", Account: a.Name, Text: "That code is wrong"})
	default:
		if err := s.confirmAccount(a); err != nil {
			done(RegResult{Code: "TEMPORARILY_UNAVAILABLE", Account: a.Name, Text: "Something went wrong on the server"})
			return
		}
		c.cmdName = "VERIFY"
		s.nsLogCmd(c, "to confirm their email")
		done(RegResult{Code: "SUCCESS", Account: a.Name, Text: "Your account is confirmed"})
	}
}
