package server

import (
	"bufio"
	"context"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// smtpSink answers SMTP for any number of mails; AUTH with the password
// "wrong" is refused as Gmail does. It returns the address and the mails.
func smtpSink(t *testing.T) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 10)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				say := func(s string) { conn.Write([]byte(s + "\r\n")) }
				say("220 sink ESMTP")
				var data strings.Builder
				inData := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if inData {
						if line == ".\r\n" {
							inData = false
							got <- data.String()
							say("250 ok")
						} else {
							data.WriteString(line)
						}
						continue
					}
					cmd := strings.TrimSpace(line)
					switch up := strings.ToUpper(cmd); {
					case strings.HasPrefix(up, "EHLO"):
						say("250-sink")
						say("250 AUTH PLAIN")
					case strings.HasPrefix(up, "AUTH PLAIN"):
						if strings.Contains(cmd, "AHVzZXIAd3Jvbmc=") { // \0user\0wrong
							say("535 5.7.8 Username and Password not accepted.")
						} else {
							say("235 ok")
						}
					case strings.HasPrefix(up, "MAIL"), strings.HasPrefix(up, "RCPT"):
						say("250 ok")
					case up == "DATA":
						inData = true
						say("354 go ahead")
					case up == "QUIT":
						say("221 bye")
						return
					default:
						say("502 no")
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), got
}

// The Mail card: save a provider (the password stays when left empty),
// send a test, and Confirm by email only after a test went through with
// the saved settings.
func TestPanelMail(t *testing.T) {
	old := mailLookup
	mailLookup = func(ctx context.Context, name string) ([]string, error) {
		if name == "example.org" {
			return []string{"v=spf1 mx ~all"}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	t.Cleanup(func() { mailLookup = old })
	s, path, _ := fileServer(t, "")
	boss(t, s)
	ask := func(r control.Request) *control.Response {
		r.As, r.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, r)
	}
	if m := ask(control.Request{Cmd: control.CmdMail}).Mail; m == nil || m.Provider != "off" || len(m.Presets) != 3 || m.Registration != "none" {
		t.Fatalf("mail: %+v", m)
	}
	gmail := &control.MailForm{Provider: "gmail", From: "bee@gmail.com", Name: "BeeNet Services"}
	if r := ask(control.Request{Cmd: control.CmdMailSave, Mail: gmail}); r.Error != "Type the app password." {
		t.Errorf("no password: %+v", r)
	}
	gmail.Password = "abcd efgh ijkl mnop"
	m := ask(control.Request{Cmd: control.CmdMailSave, Mail: gmail}).Mail
	if m == nil || m.Provider != "gmail" || m.Server != "smtp.gmail.com:587" || m.User != "bee@gmail.com" || !m.HasPassword || m.Tested != 0 {
		t.Fatalf("saved gmail: %+v", m)
	}
	gmail.Password = ""
	if m := ask(control.Request{Cmd: control.CmdMailSave, Mail: gmail}).Mail; m == nil || !m.HasPassword {
		t.Errorf("password kept: %+v", m)
	}
	conf, _ := os.ReadFile(path)
	for _, want := range []string{`smtp_password = "abcdefghijklmnop"`, `smtp_server = "smtp.gmail.com:587"`, `from_name = "BeeNet Services"`, "use_mail = true"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("beeircd.conf lacks %s:\n%s", want, conf)
		}
	}

	// Not tested yet: Confirm by email is refused, the other two are fine.
	if r := ask(control.Request{Cmd: control.CmdMailReg, Value: "mail"}); r.Error != "Send a test mail that goes through first." {
		t.Errorf("reg mail untested: %+v", r)
	}
	if m := ask(control.Request{Cmd: control.CmdMailReg, Value: "admin"}).Mail; m == nil || m.Registration != "admin" {
		t.Errorf("reg admin: %+v", m)
	}

	// A test with unsaved settings (an own server) goes out but doesn't
	// count for the saved ones.
	addr, got := smtpSink(t)
	own := &control.MailForm{Provider: "own", From: "services@example.org", Server: addr, User: "user", Password: "right"}
	tr := ask(control.Request{Cmd: control.CmdMailTest, Mail: own, Value: "me@example.net"}).MailTest
	if tr == nil || !tr.OK || tr.To != "me@example.net" || tr.Domain != "example.org" {
		t.Fatalf("test: %+v", tr)
	}
	if mail := <-got; !strings.Contains(mail, "Subject: Test mail from") || !strings.Contains(mail, "From: services@example.org") {
		t.Errorf("test mail:\n%s", mail)
	}
	states := map[string]string{}
	for _, c := range tr.Checks {
		states[c.Name] = c.State
	}
	if states["SPF"] != "ok" || states["DKIM"] != "bad" || states["DMARC"] != "bad" {
		t.Errorf("checks: %+v", tr.Checks)
	}
	if r := ask(control.Request{Cmd: control.CmdMailReg, Value: "mail"}); r.Error == "" {
		t.Errorf("reg mail after a test of other settings: %+v", r)
	}

	// A wrong password: the server's words and what they mean.
	own.Password = "wrong"
	tr = ask(control.Request{Cmd: control.CmdMailTest, Mail: own, Value: "me@example.net"}).MailTest
	if tr == nil || tr.OK || !strings.Contains(tr.Raw, "535") || tr.Explain != "The mail server said no to the login or password." {
		t.Errorf("wrong password: %+v", tr)
	}

	// Saving the settings that were tested counts the test; Confirm by
	// email opens.
	own.Password = "right"
	if m := ask(control.Request{Cmd: control.CmdMailSave, Mail: own}).Mail; m == nil || m.Provider != "own" || m.Tested == 0 {
		t.Fatalf("saved own: %+v", m)
	}
	if tr := ask(control.Request{Cmd: control.CmdMailTest, Mail: &control.MailForm{Provider: "own", From: "services@example.org", Server: addr, User: "user"}, Value: "me@example.net"}).MailTest; tr == nil || !tr.OK {
		t.Fatalf("test with the saved password: %+v", tr)
	}
	<-got
	if m := ask(control.Request{Cmd: control.CmdMail}).Mail; m.Tested == 0 {
		t.Errorf("tested not noted: %+v", m)
	}
	if m := ask(control.Request{Cmd: control.CmdMailReg, Value: "mail"}).Mail; m == nil || m.Registration != "mail" {
		t.Fatalf("reg mail: %+v", m)
	}
	if r := ask(control.Request{Cmd: control.CmdMailSave, Mail: &control.MailForm{Provider: "off"}}); !strings.Contains(r.Error, "Pick another way") {
		t.Errorf("mail off while confirming by mail: %+v", r)
	}
	if d := ask(control.Request{Cmd: control.CmdMailDNS}).MailTest; d == nil || d.Domain != "example.org" || len(d.Checks) != 3 {
		t.Errorf("dns: %+v", d)
	}

	// Nothing answering: said plainly.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	tr = ask(control.Request{Cmd: control.CmdMailTest, Mail: &control.MailForm{Provider: "own", From: "services@example.org", Server: dead}, Value: "me@example.net"}).MailTest
	if tr == nil || tr.OK || tr.Explain != "Nothing answers at "+dead+". Check the host and port." {
		t.Errorf("dead server: %+v", tr)
	}
	ev := ask(control.Request{Cmd: control.CmdPanelLog}).Events
	if !strings.Contains(ev[0].What, "test mail to me@example.net failed") {
		t.Errorf("events: %+v", ev[:2])
	}
}
