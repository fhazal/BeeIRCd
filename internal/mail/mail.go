// Package mail sends services' mails (password resets, memo mails,
// registration passcodes) and helps an owner set mail up: the usual mail
// services as presets, a test that says in plain words why sending failed,
// and a check of the sender domain's SPF, DKIM and DMARC records, which
// decide whether a mail lands in the inbox or in spam.
package mail

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// A Preset is a mail service whose server, port and login are known.
type Preset struct {
	ID     string // gmail, outlook, zoho
	Name   string
	Server string // host:port
	// Domains are its own free addresses: they sign every mail.
	Domains []string
	// SPF is what a custom domain's SPF record includes for it; DKIM the
	// usual selectors it publishes a key under.
	SPF  string
	DKIM []string
}

// Presets are the mail services the Hive Panel offers.
var Presets = []Preset{
	{ID: "gmail", Name: "Gmail / Google Workspace", Server: "smtp.gmail.com:587",
		Domains: []string{"gmail.com", "googlemail.com"}, SPF: "_spf.google.com", DKIM: []string{"google"}},
	{ID: "outlook", Name: "Outlook / Microsoft 365", Server: "smtp.office365.com:587",
		Domains: []string{"outlook.com", "hotmail.com", "live.com", "msn.com"}, SPF: "spf.protection.outlook.com", DKIM: []string{"selector1", "selector2"}},
	{ID: "zoho", Name: "Zoho Mail", Server: "smtp.zoho.com:587",
		Domains: []string{"zoho.com", "zohomail.com"}, SPF: "zoho.com", DKIM: []string{"zmail", "zoho"}},
}

// PresetFor finds the preset of an SMTP server ("" none, or "own").
func PresetFor(server string) *Preset {
	host := hostOf(server)
	for i := range Presets {
		if strings.EqualFold(hostOf(Presets[i].Server), host) {
			return &Presets[i]
		}
	}
	// Zoho has regional servers: smtp.zoho.eu, smtp.zoho.in ...
	if strings.HasPrefix(strings.ToLower(host), "smtp.zoho.") || strings.HasPrefix(strings.ToLower(host), "smtppro.zoho.") {
		return &Presets[2]
	}
	return nil
}

// ProviderOf names the panel's choice for m: off, sendmail, a preset's ID,
// or own.
func ProviderOf(m config.Mail) string {
	switch {
	case !m.UseMail:
		return "off"
	case m.SMTPServer == "":
		return "sendmail"
	}
	if p := PresetFor(m.SMTPServer); p != nil {
		return p.ID
	}
	return "own"
}

func hostOf(server string) string {
	if h, _, err := net.SplitHostPort(server); err == nil {
		return h
	}
	return server
}

// Domain is the part of an address after the @.
func Domain(addr string) string {
	if _, after, ok := strings.Cut(addressOf(addr), "@"); ok {
		return strings.ToLower(strings.TrimSuffix(after, "."))
	}
	return ""
}

// addressOf takes the address out of `Name <addr>`.
func addressOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '<'); i >= 0 {
		s = strings.TrimSuffix(s[i+1:], ">")
	}
	return s
}

// ValidAddress is a plain check of an email address.
func ValidAddress(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, " \t\r\n<>\",;") {
		return false
	}
	local, dom, ok := strings.Cut(s, "@")
	return ok && local != "" && strings.Contains(dom, ".") && !strings.ContainsRune(dom, '@') &&
		!strings.HasPrefix(dom, ".") && !strings.HasSuffix(dom, ".")
}

// Message builds the whole mail: Anope's headers plus Date, Message-ID and
// MIME-Version (mails without them score as spam), and the sender's name.
func Message(m config.Mail, name, addr, subject, body string, now time.Time) string {
	to := "\"" + strings.ReplaceAll(name, "\\", "\\\\") + "\" <" + addr + ">"
	if m.DontQuoteAddresses {
		to = name + " <" + addr + ">"
	}
	from := m.SendFrom
	if m.FromName != "" && !strings.Contains(from, "<") {
		from = mime.QEncoding.Encode("utf-8", m.FromName) + " <" + from + ">"
		if !strings.HasPrefix(from, "=?") {
			from = "\"" + strings.ReplaceAll(m.FromName, "\"", "") + "\" <" + m.SendFrom + ">"
		}
	}
	dom := Domain(m.SendFrom)
	if dom == "" {
		dom = "localhost"
	}
	var id [12]byte
	rand.Read(id[:])
	return "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", subject) +
		"\r\nDate: " + now.Format(time.RFC1123Z) +
		"\r\nMessage-ID: <" + hex.EncodeToString(id[:]) + "@" + dom + ">" +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
}

// Timeout is how long one mail may take to send.
var Timeout = 30 * time.Second

// Send delivers one mail with the settings m: through the SMTP server
// (TLS from the start on port 465, STARTTLS when the server offers it),
// or the sendmail program. It blocks; call it off the loop.
func Send(m config.Mail, name, addr, subject, body string) error {
	text := Message(m, name, addr, subject, body, time.Now())
	if m.SMTPServer == "" {
		f := strings.Fields(m.SendmailPath)
		if len(f) == 0 {
			return fmt.Errorf("no sendmail_path")
		}
		cmd := exec.Command(f[0], f[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	host, port, err := net.SplitHostPort(m.SMTPServer)
	if err != nil {
		return err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	implicit := port == "465"
	if implicit {
		conn, err = tls.DialWithDialer(&d, "tcp", m.SMTPServer, &tls.Config{ServerName: host})
	} else {
		conn, err = d.Dial("tcp", m.SMTPServer)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(Timeout))
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if me, err := os.Hostname(); err == nil && strings.Contains(me, ".") {
		if err := cl.Hello(me); err != nil {
			return err
		}
	}
	if ok, _ := cl.Extension("STARTTLS"); ok && !implicit {
		if err := cl.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if m.SMTPUser != "" {
		if err := cl.Auth(smtp.PlainAuth("", m.SMTPUser, m.SMTPPassword, host)); err != nil {
			return fmt.Errorf("login: %w", err)
		}
	}
	if err := cl.Mail(addressOf(m.SendFrom)); err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	if err := cl.Rcpt(addr); err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(text)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// Explain says in plain words what a failed Send means, for the settings
// m. The mail server's own words stay in err.
func Explain(m config.Mail, err error) string {
	if err == nil {
		return ""
	}
	e := strings.ToLower(err.Error())
	p := PresetFor(m.SMTPServer)
	who := "The mail server"
	if p != nil {
		who = strings.SplitN(p.Name, " /", 2)[0]
	}
	server := m.SMTPServer
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(e, w) {
				return true
			}
		}
		return false
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "There is no mail server called " + hostOf(server) + ". Check the name."
	case errors.Is(err, syscall.ECONNREFUSED) || has("connection refused", "actively refused"):
		return "Nothing answers at " + server + ". Check the host and port."
	case errors.As(err, &netErr) && netErr.Timeout():
		return "No answer from " + server + ". Many hosting companies block outgoing mail ports (25, 465, 587) on new servers; ask them to open it, or try the other port."
	case strings.HasPrefix(e, "recipient: "):
		return "The address you sent the test to was refused. Check it."
	case strings.HasPrefix(e, "sender: ") && !has("5.7.0 authentication required", "530"):
		return who + " won't send as " + addressOf(m.SendFrom) + ". Send from the address you log in with (or one it lets you use)."
	case m.SMTPServer == "":
		return "The sendmail program failed. Is a mail server (Postfix, Exim) set up on this machine? Or pick a mail service instead."
	case has("application-specific password", "534 5.7.9"):
		return who + " wants an app password, not your normal password."
	case has("535", "username and password not accepted", "authentication unsuccessful", "authentication failed", "invalid credentials", "auth failed"):
		switch p.idOr() {
		case "gmail":
			return "Gmail said no to the password. Use an app password (myaccount.google.com/apppasswords), not your normal Gmail password."
		case "outlook":
			return "Microsoft said no to the login. Microsoft is turning off password sign-in for sending mail; if an app password doesn't work either, use Gmail or Zoho."
		case "zoho":
			return "Zoho said no to the password. Make an app password in Zoho's security settings, and check the region (smtp.zoho.eu for EU accounts)."
		}
		return "The mail server said no to the login or password."
	case has("smtp auth", "5.7.139", "5.7.57", "basic authentication is disabled"):
		return who + " has sending by password turned off for this account."
	case has("unencrypted connection"):
		return "The mail server didn't offer TLS, so the password wasn't sent. Use port 587 (STARTTLS) or 465 (TLS)."
	case has("first record does not look like a tls handshake", "tls: ", "handshake"):
		if has("x509", "certificate") {
			return "The mail server's TLS certificate isn't valid for " + hostOf(server) + "."
		}
		return "TLS didn't work. Port 465 is TLS from the start, port 587 is STARTTLS: check the port matches."
	case has("x509", "certificate"):
		return "The mail server's TLS certificate isn't valid for " + hostOf(server) + "."
	case has("no such host"):
		return "There is no mail server called " + hostOf(server) + ". Check the name."
	case has("connection refused"):
		return "Nothing answers at " + server + ". Check the host and port."
	case has("i/o timeout", "deadline exceeded", "timed out"):
		return "No answer from " + server + ". Many hosting companies block outgoing mail ports (25, 465, 587) on new servers; ask them to open it, or try the other port."
	case has("530", "authentication required"):
		return who + " wants a login: fill in the login and password."
	}
	return "Sending failed."
}

func (p *Preset) idOr() string {
	if p == nil {
		return ""
	}
	return p.ID
}
