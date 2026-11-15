package server

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

func TestAccountRegistration(t *testing.T) {
	s := testServer(t, draftsOn)
	c := raw(t, s)
	c.nick = "Bee"
	c.send("CAP LS 302")
	if l := c.expect("CAP * LS"); !strings.Contains(l, accountRegCap) || strings.Contains(l, accountRegCap+"=email-required") {
		t.Errorf("CAP LS: %s", l)
	}
	c.send("CAP REQ " + accountRegCap)
	c.expect("CAP * ACK")
	c.send("REGISTER * * " + pw)
	c.expect("FAIL REGISTER COMPLETE_CONNECTION_REQUIRED * ")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("CAP END")
	c.expect(" 001 Bee ")

	c.send("REGISTER Somebody * " + pw)
	c.expect("FAIL REGISTER ACCOUNT_NAME_MUST_BE_NICK Somebody ")
	c.send("REGISTER * * short")
	c.expect("FAIL REGISTER WEAK_PASSWORD Bee ")
	c.send("REGISTER * not-an-email " + pw)
	c.expect("FAIL REGISTER INVALID_EMAIL Bee ")
	c.send("REGISTER *")
	c.expect("FAIL REGISTER NEED_MORE_PARAMS ")
	c.send("REGISTER * * " + pw)
	c.expect(" 900 Bee ")
	c.expect("REGISTER SUCCESS Bee :Account created")
	c.send("REGISTER * * " + pw)
	c.expect("FAIL REGISTER ALREADY_AUTHENTICATED Bee ")

	other := capLogin(t, s, "Bee2", accountRegCap)
	other.send("NICK Bee")
	other.expect(" 433 ") // in use; register the taken name another way
	other.send("REGISTER Bee2 * " + pw)
	other.expect("REGISTER SUCCESS Bee2 ")
}

// registration = "mail": REGISTER needs an email and VERIFY takes the code.
func TestAccountRegistrationMail(t *testing.T) {
	s, _, _ := fileServerWith(t, mailConf, func(s *Server) {
		s.cfg.Mail.Delay = &config.Duration{Duration: time.Second}
		s.cfg.Server.IRCv3Drafts = true
	})
	got := fakeMail(s)
	s.onLoop(func() { s.cfg.Services.Registration = "mail" })
	c := raw(t, s)
	c.nick = "Bee"
	c.send("CAP LS 302")
	if l := c.expect("CAP * LS"); !strings.Contains(l, accountRegCap+"=email-required") {
		t.Errorf("CAP LS: %s", l)
	}
	c.send("CAP REQ " + accountRegCap)
	c.expect("CAP * ACK")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("CAP END")
	c.expect(" 001 Bee ")
	c.send("REGISTER * * " + pw)
	c.expect("FAIL REGISTER INVALID_EMAIL Bee ")
	c.send("REGISTER * bee@example.org " + pw)
	c.expect("REGISTER VERIFICATION_REQUIRED Bee ")
	m := nextMail(t, got)
	code := regexp.MustCompile(`CONFIRM (\S+) `).FindStringSubmatch(m.body)
	if code == nil {
		t.Fatalf("mail: %+v", m)
	}
	c.send("VERIFY Bee wrong")
	c.expect("FAIL VERIFY INVALID_CODE Bee ")
	c.send("VERIFY Bee " + code[1])
	c.expect("VERIFY SUCCESS Bee ")
}
