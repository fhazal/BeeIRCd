package services

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// fakeSMTP answers just enough SMTP for one mail and returns what it got.
func fakeSMTP(t *testing.T) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { conn.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
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
					say("250 ok")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 ok")
			case cmd == "DATA":
				inData = true
				say("354 go ahead")
			case cmd == "QUIT":
				say("221 bye")
				got <- data.String()
				return
			default:
				say("502 no")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSendMailSMTP(t *testing.T) {
	addr, got := fakeSMTP(t)
	cfg := &config.Config{}
	cfg.SetDefaults()
	cfg.Mail.UseMail, cfg.Mail.SendFrom, cfg.Mail.SMTPServer = true, "services@beenet.example", addr
	s := &Services{cfg: cfg}
	if err := s.sendMail("Bee", "bee@example.org", "New memo", "Hi Bee,\nline two"); err != nil {
		t.Fatal(err)
	}
	text := <-got
	for _, want := range []string{"From: services@beenet.example\r\n", "To: \"Bee\" <bee@example.org>\r\n", "Subject: New memo\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n", "\r\n\r\nHi Bee,\r\nline two\r\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("mail lacks %q:\n%s", want, text)
		}
	}
	if fill("Hi %n, memo %d from %s: %t (%N) 100%", map[string]string{"%n": "Bee", "%d": "2", "%s": "kes", "%t": "hey", "%N": "BeeNet"}) != "Hi Bee, memo 2 from kes: hey (BeeNet) 100%" {
		t.Error("fill")
	}
}
