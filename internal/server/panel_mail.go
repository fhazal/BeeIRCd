package server

// The Mail card on the Hive Panel's Settings page (owner only): pick a
// mail service, send a test mail, see whether mails will land in the
// inbox, and choose how new NickServ accounts are confirmed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/mail"
)

// mailTested is the last test mail that went through: which settings
// (mailKey) and when.
type mailTested struct {
	key string
	at  time.Time
}

// mailKey stands for the settings that decide whether a mail goes out.
func mailKey(m config.Mail) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{m.SMTPServer, m.SMTPUser, m.SMTPPassword, m.SendFrom, m.SendmailPath}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// mailLookup reads TXT records for the spam check (tests replace it).
var mailLookup mail.Lookup

// panelMail is the Mail card (on the loop).
func (s *Server) panelMail() *control.Mail {
	m := s.cfg.Mail
	out := &control.Mail{
		Provider: mail.ProviderOf(m), From: m.SendFrom, Name: m.FromName, Server: m.SMTPServer, User: m.SMTPUser,
		HasPassword: m.SMTPPassword != "", Registration: s.cfg.Services.RegistrationMode(),
		Expire: config.FormatDuration(s.cfg.Services.UnconfirmedExpireOr()),
	}
	if m.UseMail && s.mailOK.key == mailKey(m) {
		out.Tested = s.mailOK.at.Unix()
	}
	for _, p := range mail.Presets {
		out.Presets = append(out.Presets, control.Preset{ID: p.ID, Name: p.Name, Server: p.Server})
	}
	if m.UseMail && m.SendFrom == "" {
		out.Problems = append(out.Problems, "use_mail is on but send_from is empty, so no mail goes out.")
	}
	return out
}

// mailFromForm is the saved mail settings changed as f says (on the loop).
func (s *Server) mailFromForm(f *control.MailForm) (config.Mail, string) {
	m := s.cfg.Mail
	if f == nil {
		return m, "Bad request."
	}
	if f.Provider == "off" {
		m.UseMail = false
		return m, ""
	}
	from := strings.TrimSpace(f.From)
	if !mail.ValidAddress(from) {
		return m, "Type the address mails are sent from, like services@example.org."
	}
	name := strings.TrimSpace(f.Name)
	if len(name) > 60 || strings.ContainsAny(name, "\r\n<>\"\\") {
		return m, "The name people see is up to 60 characters, without < > \" or \\."
	}
	pw := f.Password
	if strings.ContainsAny(pw, "\r\n") || len(pw) > 200 {
		return m, "That password can't be right."
	}
	switch f.Provider {
	case "sendmail":
		m.SMTPServer, m.SMTPUser, m.SMTPPassword = "", "", ""
	case "own":
		srv := strings.TrimSpace(f.Server)
		if _, _, err := net.SplitHostPort(srv); err != nil {
			srv = net.JoinHostPort(srv, "587")
		}
		host, port, err := net.SplitHostPort(srv)
		if err != nil || host == "" || strings.ContainsAny(host, " /@") || !numeric(port) {
			return m, "Type the mail server like mail.example.org or mail.example.org:465."
		}
		user := strings.TrimSpace(f.User)
		if strings.ContainsAny(user, " \r\n") || len(user) > 200 {
			return m, "The login has no spaces."
		}
		if pw == "" && srv == m.SMTPServer && user == m.SMTPUser {
			pw = m.SMTPPassword
		}
		if user != "" && pw == "" {
			return m, "Type the password for " + user + "."
		}
		m.SMTPServer, m.SMTPUser, m.SMTPPassword = srv, user, pw
	default:
		var p *mail.Preset
		for i := range mail.Presets {
			if mail.Presets[i].ID == f.Provider {
				p = &mail.Presets[i]
			}
		}
		if p == nil {
			return m, "Pick how mail is sent."
		}
		if pw == "" && strings.EqualFold(from, m.SMTPUser) && mail.PresetFor(m.SMTPServer) == p {
			pw = m.SMTPPassword
		}
		if pw == "" {
			return m, "Type the app password."
		}
		pw = strings.ReplaceAll(pw, " ", "") // Google shows app passwords in groups of four
		srv := p.Server
		if q := mail.PresetFor(m.SMTPServer); q == p {
			srv = m.SMTPServer // keep a regional server (smtp.zoho.eu) or port 465
		}
		m.SMTPServer, m.SMTPUser, m.SMTPPassword = srv, from, pw
	}
	m.UseMail, m.SendFrom, m.FromName = true, from, name
	return m, ""
}

func numeric(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// panelMailSave saves the Mail card to beeircd.conf (on the loop).
func (s *Server) panelMailSave(po *control.PanelOper, ip string, f *control.MailForm) control.Response {
	m, problem := s.mailFromForm(f)
	if problem != "" {
		return control.Response{Error: problem}
	}
	if !m.UseMail && s.cfg.Services.RegistrationMode() == "mail" {
		return control.Response{Error: "New accounts are confirmed by email. Pick another way for them first, then turn mail off."}
	}
	if s.cfg.Path == "" {
		return control.Response{Error: "This server wasn't started from a config file, so settings can't be saved."}
	}
	before := describeMail(s.cfg.Mail)
	fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		d.Set("mail", "use_mail", fmt.Sprint(m.UseMail))
		if m.UseMail {
			d.Set("mail", "send_from", config.Quote(m.SendFrom))
			d.Set("mail", "from_name", config.Quote(m.FromName))
			d.Set("mail", "smtp_server", config.Quote(m.SMTPServer))
			d.Set("mail", "smtp_user", config.Quote(m.SMTPUser))
			d.Set("mail", "smtp_password", config.Quote(m.SMTPPassword))
		}
		return nil
	})
	if err != nil {
		s.log.Printf("Hive Panel: saving mail: %v", err)
		return control.Response{Error: "Not saved: " + err.Error()}
	}
	s.cfg.Mail = fresh.Mail
	now := describeMail(s.cfg.Mail)
	if now != before {
		s.panelEvent(po.Account, ip, "changed mail to "+now+" (was "+before+")")
		s.sno('a', "Hive Panel: %s changed mail to %s (was %s)", po.Account, now, before)
	}
	return control.Response{Mail: s.panelMail()}
}

// describeMail is the mail settings in a few words, no password.
func describeMail(m config.Mail) string {
	switch p := mail.ProviderOf(m); p {
	case "off":
		return "off"
	case "sendmail":
		return "sendmail, from " + m.SendFrom
	case "own":
		return m.SMTPServer + ", from " + m.SendFrom
	default:
		return mail.PresetFor(m.SMTPServer).Name + ", from " + m.SendFrom
	}
}

// panelMailReg sets how new NickServ accounts are confirmed (on the loop).
// Confirm by email needs a test mail that went through with the saved
// settings, so nobody turns it on while mail is broken.
func (s *Server) panelMailReg(po *control.PanelOper, ip, value string) control.Response {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "mail" && s.cfg.Services.RegistrationMode() != "mail" {
		switch {
		case !s.cfg.Mail.On():
			return control.Response{Error: "Set up mail first."}
		case s.mailOK.key != mailKey(s.cfg.Mail):
			return control.Response{Error: "Send a test mail that goes through first."}
		}
	}
	st, _ := findSetting("registration")
	if r := s.panelSet(po, ip, st.name, value); r.Error != "" {
		return r
	}
	return control.Response{Mail: s.panelMail()}
}

// mailTest sends a test mail with m and runs the spam check. It blocks;
// call it off the loop.
func mailTest(m config.Mail, to, network string) *control.MailTest {
	t := &control.MailTest{To: to}
	start := time.Now()
	body := "Hi,\n\nThis is a test mail from the Hive Panel of " + network + ".\nIf you can read it, services can send mail.\n\nIf it landed in spam, mark it \"not spam\" once, and look at the\nspam check on the Mail card.\n\n" + network + " administrators."
	err := mail.Send(m, to, to, "Test mail from "+network, body)
	t.Took = time.Since(start).Milliseconds()
	if err != nil {
		t.Explain, t.Raw = mail.Explain(m, err), err.Error()
	} else {
		t.OK = true
	}
	t.Domain, t.Checks = mailChecks(m)
	return t
}

// mailChecks is the spam check of m's sender domain (off the loop).
func mailChecks(m config.Mail) (string, []control.MailCheck) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var out []control.MailCheck
	for _, c := range mail.CheckDomain(ctx, m, mailLookup) {
		out = append(out, control.MailCheck{Name: c.Name, State: c.State, Text: c.Text, Fix: c.Fix})
	}
	return mail.Domain(m.SendFrom), out
}

// panelMailTest is mail-test: off the loop but for reading the settings
// and noting the result.
func (s *Server) panelMailTest(req control.Request, run func(byte, func(*control.PanelOper)) control.Response) control.Response {
	var (
		po      *control.PanelOper
		m       config.Mail
		problem string
		network string
	)
	if r := run('O', func(p *control.PanelOper) {
		po, network = p, s.cfg.Server.Network
		m, problem = s.mailFromForm(req.Mail)
	}); r.Error != "" {
		return r
	}
	to := strings.TrimSpace(req.Value)
	switch {
	case problem != "":
		return control.Response{Error: problem}
	case !m.UseMail:
		return control.Response{Error: "Mail is off."}
	case !mail.ValidAddress(to):
		return control.Response{Error: "Type the address to send the test mail to."}
	}
	t := mailTest(m, to, network)
	s.onLoop(func() {
		if t.OK {
			s.mailOK = mailTested{key: mailKey(m), at: time.Now()}
			s.panelEvent(po.Account, req.IP, "sent a test mail to "+to+" ("+describeMail(m)+")")
		} else {
			s.panelEvent(po.Account, req.IP, "test mail to "+to+" failed: "+t.Raw)
		}
	})
	return control.Response{MailTest: t}
}

// panelMailDNS is mail-dns: the spam check of the saved settings.
func (s *Server) panelMailDNS(run func(byte, func(*control.PanelOper)) control.Response) control.Response {
	var m config.Mail
	if r := run('O', func(*control.PanelOper) { m = s.cfg.Mail }); r.Error != "" {
		return r
	}
	t := &control.MailTest{}
	if m.UseMail {
		t.Domain, t.Checks = mailChecks(m)
	}
	return control.Response{MailTest: t}
}
