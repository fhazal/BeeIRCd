package services

// Confirming new accounts, as Anope's ns_register does with
// registration = "mail" (REGISTER mails a passcode, CONFIRM <passcode>
// takes it, RESEND mails it again) or "admin" (an oper runs CONFIRM
// <nick>). An unconfirmed account can identify but can't register
// channels, and it is dropped after unconfirmed_expire.

import (
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

const confirmCodeLen = 9 // Anope's registration passcode

func (s *Services) regMode() string { return s.cfg.Services.RegistrationMode() }

// confirmOn reports whether new accounts need confirming.
func (s *Services) confirmOn() bool { return s.regMode() != "none" }

// operCan reports whether c is an IRC operator with flag f.
func (s *Services) operCan(c *Client, f byte) bool {
	return c.can(f)
}

// afterRegister marks a new account unconfirmed and says what to do next.
func (s *Services) afterRegister(c *Client, a *store.Account) {
	switch s.regMode() {
	case "mail":
		a.Unconfirmed, a.ConfirmCode = true, randomPasscode(confirmCodeLen)
		if err := s.store.SaveAccount(a); err != nil {
			s.log.Printf("services database: %v", err)
		}
		if s.sendRegMail(c, a) {
			s.ns(c, "A passcode has been sent to %s, please type /msg NickServ CONFIRM <passcode> to confirm your email address.", a.Email)
		}
		if d := s.cfg.Services.UnconfirmedExpireOr(); d > 0 {
			s.ns(c, "If you do not confirm your email address within %s your account will expire.", anopeDuration(d))
		}
	case "admin":
		a.Unconfirmed = true
		if err := s.store.SaveAccount(a); err != nil {
			s.log.Printf("services database: %v", err)
		}
		s.ns(c, "All new accounts must be validated by an administrator. Please wait for your registration to be confirmed.")
	}
}

func (s *Services) sendRegMail(c *Client, a *store.Account) bool {
	t := map[string]string{"%n": a.Name, "%N": s.cfg.Server.Network, "%c": a.ConfirmCode}
	return s.mailTo(c, s.nickServ, a, fill(s.cfg.Mail.RegistrationSubject, t), fill(s.cfg.Mail.RegistrationMessage, t))
}

// confirmWarning is told to someone identifying to an unconfirmed account.
func (s *Services) confirmWarning(c *Client, a *store.Account) {
	if !a.Unconfirmed {
		return
	}
	if s.regMode() == "admin" {
		s.ns(c, "Your account is not confirmed yet. An administrator will confirm it.")
		return
	}
	s.ns(c, "Your email address is not confirmed. To confirm it, follow the instructions that were emailed to you.")
}

func (s *Services) confirmAccount(a *store.Account) error {
	a.Unconfirmed, a.ConfirmCode = false, ""
	return s.store.SaveAccount(a)
}

// nsConfirm is CONFIRM <passcode> (the registration mail), CONFIRM <nick>
// (an oper confirms an account) and CONFIRM <nick> <passcode> (RESETPASS).
func nsConfirm(s *Services, c *Client, args []string) {
	if len(args) >= 2 {
		nsConfirmReset(s, c, args)
		return
	}
	if len(args) == 0 {
		s.ns(c, "Syntax: CONFIRM <passcode>")
		return
	}
	arg := args[0]
	if s.operCan(c, 's') {
		if a, err := s.store.AccountByNick(arg); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		} else if a != nil {
			if !a.Unconfirmed {
				s.ns(c, "%s is already confirmed.", a.Name)
				return
			}
			if err := s.confirmAccount(a); err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
			s.nsLogCmd(c, "to confirm nickname %s (%s)", a.Name, orNone(a.Email))
			s.ns(c, "%s has been confirmed.", a.Name)
			for _, o := range s.clients {
				if o.accountID == a.ID && !o.closed {
					s.ns(o, "Your account %s has been confirmed.", a.Name)
				}
			}
			return
		}
	}
	a, _ := s.store.AccountByID(c.accountID)
	switch {
	case a == nil || a.Unconfirmed && (a.ConfirmCode == "" || arg != a.ConfirmCode):
		s.ns(c, "Invalid passcode.")
	case !a.Unconfirmed:
		s.ns(c, "Your account is already confirmed.")
	default:
		if err := s.confirmAccount(a); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.nsLogCmd(c, "to confirm their email")
		s.ns(c, "Your email address of %s has been confirmed.", a.Email)
	}
}

// nsResend mails the registration passcode again.
func nsResend(s *Services, c *Client, args []string) {
	if s.regMode() != "mail" {
		s.ns(c, "Registration mails are off on this network.")
		return
	}
	a, _ := s.store.AccountByID(c.accountID)
	switch {
	case a == nil:
		s.ns(c, "Identify first: /ns identify <password>")
	case !a.Unconfirmed:
		s.ns(c, "Your account is already confirmed.")
	default:
		if a.ConfirmCode == "" {
			a.ConfirmCode = randomPasscode(confirmCodeLen)
			if err := s.store.SaveAccount(a); err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
		}
		if s.sendRegMail(c, a) {
			s.nsLogCmd(c, "")
			s.ns(c, "Your passcode has been re-sent to %s.", a.Email)
		}
	}
}

// expireUnconfirmed drops accounts not confirmed within unconfirmed_expire
// of registering, except those in use right now.
func (s *Services) expireUnconfirmed(now time.Time) {
	d := s.cfg.Services.UnconfirmedExpireOr()
	if d <= 0 {
		return
	}
	inUse := make(map[int64]bool)
	for _, c := range s.clients {
		inUse[c.accountID] = true
	}
	for _, a := range s.store.AllAccounts() {
		if !a.Unconfirmed || inUse[a.ID] || now.Sub(a.Registered) < d {
			continue
		}
		changes, err := s.store.DeleteAccount(a.ID)
		if err != nil {
			s.log.Printf("services database: %v", err)
			continue
		}
		s.applyFounderChanges(changes, "founder "+a.Name+" expired")
		s.log.Printf("expired unconfirmed account %s (registered %s)", a.Name, a.Registered.UTC().Format(time.DateOnly))
		s.logTo(s.nickServ, "Expiring unconfirmed nickname "+a.Name+" (group: "+a.Name+") (e-mail: "+orNone(a.Email)+")")
	}
}
