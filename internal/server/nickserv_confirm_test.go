package server

import (
	"regexp"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// registration = "mail": REGISTER mails a passcode, the account can't
// register channels until CONFIRM <passcode>, RESEND mails it again, and
// LIST UNCONFIRMED finds it.
func TestRegistrationMail(t *testing.T) {
	s, _, _ := fileServerWith(t, mailConf, func(s *Server) {
		s.cfg.Mail.Delay = &config.Duration{Duration: time.Second}
	})
	got := fakeMail(s)
	b := boss(t, s)
	s.onLoop(func() { s.cfg.Services.Registration = "mail" })
	bee := login(t, s, "Bee")
	bee.send("NS REGISTER " + pw)
	bee.expect(nsLine("Bee", "Syntax: REGISTER <password> <email>"))
	bee.send("NS REGISTER " + pw + " bee@example.org")
	bee.expect(nsLine("Bee", "Bee is now registered to you."))
	bee.expect(nsLine("Bee", "A passcode has been sent to bee@example.org, please type /msg NickServ CONFIRM <passcode> to confirm your email address."))
	bee.expect(nsLine("Bee", "If you do not confirm your email address within 1 day your account will expire."))
	m := nextMail(t, got)
	code := regexp.MustCompile(`CONFIRM (\S+) `).FindStringSubmatch(m.body)
	if m.subject != "Nickname registration for Bee" || code == nil || len(code[1]) != 9 {
		t.Fatalf("registration mail = %+v", m)
	}

	bee.send("CS REGISTER #bee")
	bee.expect(csLine("Bee", "You must confirm your account before you can register a channel."))
	b.send("NS LIST * UNCONFIRMED")
	b.expect("  Bee ")
	bee.send("NS INFO Bee")
	bee.expect("unconfirmed")

	// Identifying again reminds them.
	bee.send("NS LOGOUT")
	bee.expect("You are no longer identified as Bee.")
	bee.send("NS IDENTIFY " + pw)
	bee.expect(nsLine("Bee", "Your email address is not confirmed. To confirm it, follow the instructions that were emailed to you."))

	bee.send("NS CONFIRM wrongcode")
	bee.expect(nsLine("Bee", "Invalid passcode."))
	bee.send("NS CONFIRM " + code[1])
	bee.expect(nsLine("Bee", "Your email address of bee@example.org has been confirmed."))
	bee.send("NS CONFIRM " + code[1])
	bee.expect(nsLine("Bee", "Your account is already confirmed."))
	bee.send("NS RESEND")
	bee.expect(nsLine("Bee", "Your account is already confirmed."))
	bee.send("JOIN #bee")
	bee.expect(" 366 Bee #bee ")
	bee.send("CS REGISTER #bee")
	bee.expect("You are its Founder.")

	// RESEND for a new unconfirmed account; the mail delay applies.
	kes := login(t, s, "kes")
	kes.send("NS REGISTER " + pw + " kes@example.org")
	kes.expect("A passcode has been sent to kes@example.org")
	first := nextMail(t, got)
	kes.send("NS RESEND")
	kes.expect(nsLine("kes", "Please wait 1 seconds and retry."))
	time.Sleep(1100 * time.Millisecond)
	kes.send("NS RESEND")
	kes.expect(nsLine("kes", "Your passcode has been re-sent to kes@example.org."))
	if again := nextMail(t, got); again.body != first.body {
		t.Errorf("RESEND sent another code:\n%s\n%s", first.body, again.body)
	}

	// An oper confirms by nick; the person hears about it.
	b.send("NS CONFIRM kes")
	b.expect(nsLine("Boss", "kes has been confirmed."))
	kes.expect(nsLine("kes", "Your account kes has been confirmed."))
	b.send("NS CONFIRM kes")
	b.expect(nsLine("Boss", "kes is already confirmed."))
}

// registration = "admin": no mail, an oper confirms; unconfirmed accounts
// go after unconfirmed_expire.
func TestRegistrationAdmin(t *testing.T) {
	s, _, _ := fileServerWith(t, "", func(s *Server) {
		s.cfg.Services.UnconfirmedExpire = &config.Duration{Duration: time.Hour}
	})
	b := boss(t, s)
	s.onLoop(func() { s.cfg.Services.Registration = "admin" })
	ann := login(t, s, "ann")
	ann.send("NS REGISTER " + pw + " ann@example.org")
	ann.expect(nsLine("ann", "All new accounts must be validated by an administrator. Please wait for your registration to be confirmed."))
	ann.send("NS CONFIRM 123456789")
	ann.expect(nsLine("ann", "Invalid passcode."))
	ann.send("QUIT")
	ann.expect("ERROR")

	s.onLoop(func() { s.svc.Expire(time.Now().Add(2 * time.Hour)) })
	b.send("NS INFO ann")
	b.expect("ann isn't registered")
}
