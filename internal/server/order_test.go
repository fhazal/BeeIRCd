package server

import "testing"

// O1 regression tests (audit 11 Nov 2026): a client's commands run in the
// order it sent them, even while one of its passwords is checked off the
// loop. Each test sends the password command and what follows in ONE write,
// so the follow-up is already waiting when the check starts.

func TestOperThenCommandKeepsOrder(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	joinAll("#keep", b)
	b.send("MODE Boss -o")
	b.expect("MODE Boss -o")
	b.sendRaw("OPER Boss " + operPW + "\r\nMODE #keep +P\r\nPING :after-oper\r\n")
	b.expect(" 381 Boss :You are now an IRC operator")
	// +P is oper-only: it only works because OPER finished first.
	b.expect(":Boss!~boss@127.0.0.1 MODE #keep +P")
	b.expect("PONG irc.test.local :after-oper")
}

func TestIdentifyThenJoinKeepsOrder(t *testing.T) {
	s := testServer(t, nil)
	host := login(t, s, "host")
	joinAll("#reg", host)
	host.send("MODE #reg +R")
	host.expect("MODE #reg +R")
	bee := registered(t, s, "Bee")
	bee.send("NS LOGOUT")
	bee.expect(" 901 ")
	other := login(t, s, "other")

	bee.sendRaw("NS IDENTIFY " + pw + "\r\nJOIN #reg\r\nPING :after-identify\r\n")
	// Someone else is never held up by Bee's check.
	other.send("PING :other")
	other.expect("PONG irc.test.local :other")
	// expect() skips lines it doesn't want, so each step below fails if
	// its line came before the one in the step above.
	bee.expect(nsLine("Bee", "Password accepted. You are identified as Bee."))
	bee.expect(" 366 Bee #reg ") // +R: only possible after IDENTIFY
	bee.expect("PONG irc.test.local :after-identify")
}

func TestWrongPasswordReleasesHeldLines(t *testing.T) {
	s := testServer(t, nil)
	registered(t, s, "Bee").send("QUIT")
	c := login(t, s, "guest")
	c.sendRaw("NS IDENTIFY Bee wrong-password\r\nPING :after-wrong\r\n")
	c.expect("Wrong password for Bee.")
	c.expect("PONG irc.test.local :after-wrong")
}
