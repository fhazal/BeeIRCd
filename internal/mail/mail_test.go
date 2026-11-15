package mail

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

func TestMessage(t *testing.T) {
	m := config.Mail{SendFrom: "services@beenet.example", FromName: "BeeNet Services"}
	text := Message(m, "Bee", "bee@example.org", "Héllo", "line one\nline two", time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC))
	for _, want := range []string{"From: \"BeeNet Services\" <services@beenet.example>\r\n", "To: \"Bee\" <bee@example.org>\r\n",
		"Subject: =?utf-8?q?H=C3=A9llo?=\r\n", "Date: Mon, 16 Nov 2026 04:00:00 +0000\r\n", "@beenet.example>\r\n", "MIME-Version: 1.0\r\n",
		"\r\n\r\nline one\r\nline two\r\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	m.FromName = "Bée"
	if text := Message(m, "Bee", "bee@example.org", "x", "y", time.Now()); !strings.Contains(text, "From: =?utf-8?q?B=C3=A9e?= <services@beenet.example>\r\n") {
		t.Errorf("encoded name:\n%s", text)
	}
}

func TestPresetsAndAddresses(t *testing.T) {
	for server, want := range map[string]string{"smtp.gmail.com:587": "gmail", "SMTP.GMAIL.COM:465": "gmail", "smtp.zoho.eu:587": "zoho", "mail.example.org:587": "own", "": "sendmail"} {
		if got := ProviderOf(config.Mail{UseMail: true, SMTPServer: server}); got != want {
			t.Errorf("ProviderOf(%q) = %s, want %s", server, got, want)
		}
	}
	if ProviderOf(config.Mail{SMTPServer: "smtp.gmail.com:587"}) != "off" {
		t.Error("off")
	}
	for addr, ok := range map[string]bool{"bee@example.org": true, "bee@localhost": false, "b ee@example.org": false, "@example.org": false, "bee@example.": false, "bee@@example.org": false} {
		if ValidAddress(addr) != ok {
			t.Errorf("ValidAddress(%q) != %v", addr, ok)
		}
	}
	if Domain("Bee <bee@Example.ORG>") != "example.org" {
		t.Error("Domain")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestExplain(t *testing.T) {
	gmail := config.Mail{SMTPServer: "smtp.gmail.com:587", SendFrom: "bee@gmail.com"}
	own := config.Mail{SMTPServer: "mail.example.org:587", SendFrom: "services@example.org"}
	for _, c := range []struct {
		m    config.Mail
		err  error
		want string
	}{
		{gmail, errors.New("login: 535 5.7.8 Username and Password not accepted."), "Gmail said no to the password"},
		{gmail, errors.New("login: 534 5.7.9 Application-specific password required."), "Gmail wants an app password"},
		{own, errors.New("login: 535 Authentication failed"), "The mail server said no to the login or password."},
		{own, &net.OpError{Op: "dial", Err: timeoutErr{}}, "No answer from mail.example.org:587."},
		{own, &net.DNSError{Name: "mail.example.org", IsNotFound: true}, "There is no mail server called mail.example.org."},
		{own, errors.New("dial tcp: connect: connection refused"), "Nothing answers at mail.example.org:587."},
		{own, errors.New("recipient: 550 5.1.1 user unknown"), "The address you sent the test to was refused."},
		{gmail, errors.New("sender: 553 not owned by user"), "Gmail won't send as bee@gmail.com."},
		{own, errors.New("tls: first record does not look like a TLS handshake"), "TLS didn't work."},
		{own, errors.New("x509: certificate is valid for a.example, not mail.example.org"), "certificate isn't valid"},
		{own, errors.New("smtp: unencrypted connection"), "didn't offer TLS"},
		{config.Mail{}, errors.New("exit status 1"), "The sendmail program failed."},
	} {
		if got := Explain(c.m, c.err); !strings.Contains(got, c.want) {
			t.Errorf("Explain(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestCheckDomain(t *testing.T) {
	dns := map[string][]string{
		"gmail.com":                           {"v=spf1 redirect=_spf.google.com"},
		"_dmarc.gmail.com":                    {"v=DMARC1; p=none; sp=quarantine"},
		"bajubesi.org":                        {"google-site-verification=x", "v=spf1 mx a ~all"},
		"workspace.example":                   {"v=spf1 mx ~all"},
		"_dmarc.workspace.example":            {"v=DMARC1; p=quarantine"},
		"google._domainkey.workspace.example": {"v=DKIM1; k=rsa; p=MIIB"},
		"locked.example":                      {"v=spf1 -all"},
		"two.example":                         {"v=spf1 mx ~all", "v=spf1 a ~all"},
	}
	lookup := func(ctx context.Context, name string) ([]string, error) {
		if r, ok := dns[name]; ok {
			return r, nil
		}
		return nil, &net.DNSError{Name: name, IsNotFound: true}
	}
	states := func(m config.Mail) (map[string]Check, string) {
		out := map[string]Check{}
		var order []string
		for _, c := range CheckDomain(context.Background(), m, lookup) {
			out[c.Name] = c
			order = append(order, c.Name+"="+c.State)
		}
		return out, strings.Join(order, " ")
	}
	if _, got := states(config.Mail{SMTPServer: "smtp.gmail.com:587", SendFrom: "bee@gmail.com"}); got != "SPF=ok DKIM=ok DMARC=ok" {
		t.Errorf("gmail: %s", got)
	}
	c, got := states(config.Mail{SMTPServer: "mail.bajubesi.org:587", SendFrom: "services@bajubesi.org"})
	if got != "SPF=ok DKIM=bad DMARC=bad" || c["DMARC"].Fix != `_dmarc.bajubesi.org  TXT  "v=DMARC1; p=none; rua=mailto:postmaster@bajubesi.org"` {
		t.Errorf("own domain: %s %+v", got, c)
	}
	// Google Workspace on an own domain: the SPF must include Google.
	c, got = states(config.Mail{SMTPServer: "smtp.gmail.com:587", SendFrom: "irc@workspace.example"})
	if got != "SPF=bad DKIM=ok DMARC=ok" || c["SPF"].Fix != `workspace.example  TXT  "v=spf1 mx include:_spf.google.com ~all"` {
		t.Errorf("workspace: %s %+v", got, c)
	}
	// A gmail.com address through another server.
	if _, got := states(config.Mail{SMTPServer: "mail.bajubesi.org:587", SendFrom: "bee@gmail.com"}); !strings.HasPrefix(got, "Sender=bad ") {
		t.Errorf("gmail address elsewhere: %s", got)
	}
	c, got = states(config.Mail{SMTPServer: "mail.two.example:25", SendFrom: "x@two.example"})
	if !strings.HasPrefix(got, "SPF=bad") || !strings.Contains(c["SPF"].Text, "2 SPF records") {
		t.Errorf("two SPF: %s", got)
	}
	c, got = states(config.Mail{SMTPServer: "mail.locked.example:587", SendFrom: "x@locked.example"})
	if !strings.HasPrefix(got, "SPF=bad") || c["SPF"].Fix != `locked.example  TXT  "v=spf1 mx a:mail.locked.example ~all"` {
		t.Errorf("-all only: %s %+v", got, c["SPF"])
	}
	c, _ = states(config.Mail{SMTPServer: "mail.none.example:587", SendFrom: "x@none.example"})
	if c["SPF"].Fix != `none.example  TXT  "v=spf1 mx a:mail.none.example ~all"` {
		t.Errorf("no SPF fix: %+v", c["SPF"])
	}
}
