package server

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// Step 1.5.6b: MemoServ CANCEL, CHECK, INFO, SENDALL, STAFF and SET
// NOTIFY; mail for RESETPASS and NOTIFY MAIL; NickServ BUDDY.

type sentMail struct{ name, addr, subject, body string }

// fakeMail makes s's services hand mail to the returned channel.
func fakeMail(s *Server) chan sentMail {
	got := make(chan sentMail, 10)
	done := make(chan struct{})
	s.post(func() {
		s.svc.SetMailer(func(name, addr, subject, body string) error {
			got <- sentMail{name, addr, subject, body}
			return nil
		})
		close(done)
	})
	<-done
	return got
}

func nextMail(t *testing.T, got chan sentMail) sentMail {
	t.Helper()
	select {
	case m := <-got:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("no mail was sent")
	}
	return sentMail{}
}

func TestMemoServCancelCheckInfoNotify(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bee := registered(t, s, "Bee")
	kes := registered(t, s, "kes")

	kes.send("MS SEND Bee first")
	kes.expect("Memo sent to Bee")
	kes.send("MS SEND Bee second")
	kes.expect("Memo sent to Bee")
	kes.send("MS CHECK Bee")
	if l := kes.expect(msLine("kes", "The last memo you sent to Bee (sent on ")); !strings.HasSuffix(l, ") has not yet been read.") {
		t.Errorf("CHECK = %q", l)
	}
	kes.send("MS CANCEL Bee")
	kes.expect(msLine("kes", "Last memo to Bee has been cancelled."))
	bee.send("MS READ 1")
	bee.expect("  first")
	kes.send("MS CHECK Bee")
	kes.expect(") has been read.")
	kes.send("MS CANCEL Bee")
	kes.expect(msLine("kes", "No memo was cancelable."))
	b.send("MS CHECK Bee")
	b.expect(msLine("Boss", "Nick Bee doesn't have a memo from you."))

	bee.send("MS INFO")
	bee.expect(msLine("Bee", "You currently have 1 memo."))
	bee.expect(msLine("Bee", "Your memo limit is 20."))
	bee.expect(msLine("Bee", "You will be notified of new memos at logon and when they arrive."))
	kes.send("MS INFO Bee")
	kes.expect(msLine("kes", "Access denied."))
	b.send("MS INFO Bee")
	b.expect(msLine("Boss", "Bee currently has 1 memo."))
	b.expect(msLine("Boss", "Bee's memo limit is 20."))
	b.expect(msLine("Boss", "Bee is notified of new memos at logon and when they arrive."))

	// SET NOTIFY: NEW, LOGON (also when coming back from /AWAY), OFF.
	bee.send("MS SET NOTIFY LOGON")
	bee.expect(msLine("Bee", "MemoServ will now notify you of memos when you log on or unset /AWAY."))
	bee.send("AWAY :lunch")
	bee.expect(" 306 ")
	kes.send("MS SEND Bee while you were out")
	kes.expect("Memo sent to Bee")
	bee.quiet("new memo from kes")
	bee.send("AWAY")
	bee.expect(msLine("Bee", "You have 1 new memo. Read it: /ms read 2"))
	bee.send("MS SET NOTIFY NEW")
	bee.expect(msLine("Bee", "MemoServ will now notify you of memos when they are sent to you."))
	bee.send("MS SET NOTIFY MAIL")
	bee.expect(msLine("Bee", "Syntax: SET NOTIFY {ON | LOGON | NEW | OFF}")) // no mail here
	bee.send("MS SET NOTIFY OFF")
	bee.expect(msLine("Bee", "MemoServ will not send you any notification of memos."))
	bee.send("MS INFO")
	bee.expect(msLine("Bee", "You will not be notified of new memos."))

	// SENDALL and STAFF.
	b.send("MS SENDALL Maintenance tonight")
	b.expect(msLine("Boss", "A massmemo has been sent to all registered users."))
	kes.expect(msLine("kes", "You have a new memo from Boss."))
	kes.send("MS SENDALL nope")
	kes.expect(msLine("kes", "You need oper flag g"))
	b.send("MS STAFF opers only")
	b.expect(msLine("Boss", "A memo has been sent to all services staff (0 accounts)."))
	kes.send("MS HELP")
	kes.expect(msLine("kes", "MemoServ keeps messages"))
	kes.quiet("SENDALL")
}

func TestMemoSendDelay(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Services.MemoSendDelay = &config.Duration{Duration: time.Minute} })
	registered(t, s, "Bee")
	kes := registered(t, s, "kes")
	kes.send("MS SEND Bee one")
	kes.expect("Memo sent to Bee")
	kes.send("MS SEND Bee two")
	kes.expect(msLine("kes", "Please wait 60 seconds before using the SEND command again."))
}

const mailConf = `
[mail]
use_mail = true
send_from = "services@beenet.example"
`

func TestMailResetPassAndNotifyMail(t *testing.T) {
	s, _, _ := fileServer(t, mailConf)
	got := fakeMail(s)
	bee := login(t, s, "Bee")
	bee.send("NS REGISTER " + pw + " bee@example.org")
	bee.expect("Bee is now registered to you.")
	x := login(t, s, "x")

	x.send("NS RESETPASS Bee wrong@example.org")
	x.expect(nsLine("x", "Incorrect email address."))
	x.send("NS RESETPASS Bee BEE@example.org")
	x.expect(nsLine("x", "Password reset email for Bee has been sent."))
	m := nextMail(t, got)
	if m.addr != "bee@example.org" || m.subject != "Reset password request for Bee" {
		t.Fatalf("reset mail = %+v", m)
	}
	code := regexp.MustCompile(`CONFIRM Bee (\S+) `).FindStringSubmatch(m.body)
	if code == nil || !strings.Contains(m.body, "BeeNet administrators.") {
		t.Fatalf("reset mail body = %q", m.body)
	}
	x.send("NS RESETPASS Bee bee@example.org")
	x.expect(nsLine("x", "Please wait 300 seconds and retry."))
	x.send("NS CONFIRM Bee nottheone")
	x.expect(nsLine("x", "Invalid passcode."))
	x.send("NS CONFIRM Bee " + code[1])
	x.expect(" 900 x ")
	x.expect(nsLine("x", "You are now identified for your nick. Change your password now."))
	x.send("NS CONFIRM Bee " + code[1])
	x.expect(nsLine("x", "Invalid passcode.")) // a code works once

	bee.send("MS SET NOTIFY MAIL")
	bee.expect(msLine("Bee", "You will now be informed about new memos via email."))
	kes := registered(t, s, "kes")
	kes.send("MS SEND Bee hello by mail")
	kes.expect("Memo sent to Bee")
	m = nextMail(t, got)
	if m.subject != "New memo" || !strings.Contains(m.body, "You've just received a new memo from kes. This is memo number 1.") || !strings.Contains(m.body, "hello by mail") {
		t.Fatalf("memo mail = %+v", m)
	}
	bee.send("MS SET NOTIFY NOMAIL")
	bee.expect(msLine("Bee", "You will no longer be informed via email."))
	bee.send("NS HELP")
	bee.expect("RESETPASS")
}

func TestNoMailAndForceEmail(t *testing.T) {
	s := testServer(t, func(c *config.Config) { on := true; c.Services.ForceEmail = &on })
	c := login(t, s, "Bee")
	c.send("NS REGISTER " + pw)
	c.expect(nsLine("Bee", "Syntax: REGISTER <password> <email>"))
	c.send("NS REGISTER " + pw + " bee@example.org")
	c.expect("Bee is now registered to you.")
	x := login(t, s, "x")
	x.send("NS RESETPASS Bee bee@example.org")
	x.expect(nsLine("x", "Services have been configured to not send mail."))
	x.send("NS HELP")
	x.expect("More on one command")
	x.send("NS HELP RESETPASS")
	x.expect(nsLine("x", "No help for RESETPASS."))
}

func TestBuddy(t *testing.T) {
	s := testServer(t, nil)
	bee := registered(t, s, "Bee")
	kes := registered(t, s, "kes")
	registered(t, s, "wren")

	bee.send("NS BUDDY ADD kes")
	bee.expect(nsLine("Bee", "kes added to your buddy list."))
	bee.send("NS BUDDY ADD kes")
	bee.expect(nsLine("Bee", "kes is on your buddy list already."))
	bee.send("NS BUDDY ADD nobody")
	bee.expect(nsLine("Bee", "Nick nobody isn't registered."))
	bee.send("NS BUDDY ADD Bee")
	bee.expect(nsLine("Bee", "Bee is one of your own nicks."))
	bee.send("NS BUDDY ADD wren")
	bee.expect("wren added")
	bee.send("NS BUDDY LIST")
	bee.expect(nsLine("Bee", "Your buddies:"))
	bee.expect(nsLine("Bee", "  kes                  online"))

	kes.send("NS LOGOUT")
	bee.expect(nsLine("Bee", "Your buddy kes has gone offline."))
	kes.send("NS IDENTIFY " + pw)
	bee.expect(nsLine("Bee", "Your buddy kes is now online."))
	bee.send("NS LOGOUT")
	bee.send("NS IDENTIFY " + pw)
	bee.expect(nsLine("Bee", "Buddies online: kes, wren"))
	kes.send("QUIT")
	bee.expect(nsLine("Bee", "Your buddy kes has gone offline."))
	bee.send("NS BUDDY DEL kes")
	bee.expect(nsLine("Bee", "kes removed from your buddy list."))
	bee.send("NS BUDDY DEL kes")
	bee.expect(nsLine("Bee", "kes isn't on your buddy list."))
}
