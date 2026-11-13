package server

import (
	"encoding/base64"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

func saslPlain(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
}

// saslTry connects as nick and tries SASL PLAIN for account, returning the
// raw client before CAP END.
func saslTry(t *testing.T, s *Server, nick, account string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("CAP REQ :sasl")
	c.expect("CAP * ACK :sasl")
	c.send("NICK " + nick)
	c.send("USER x 0 * :x")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + saslPlain(account, pw))
	return c
}

// A suspended account stays shut over SASL and NS RECOVER, as it does
// for NS IDENTIFY (v1.0 bug sweep).
func TestSuspendedAccountSASLAndRecover(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bee := registered(t, s, "Bee")
	b.send("NS SUSPEND Bee +1d spam")
	b.expect(nsLine("Boss", "Nick Bee is now suspended."))
	bee.send("QUIT")
	bee.expect("ERROR :Closing Link")

	c := saslTry(t, s, "x1", "Bee")
	c.expect(" 904 x1 :SASL authentication failed: account Bee is suspended")

	r := login(t, s, "x2")
	r.send("NS RECOVER Bee " + pw)
	r.expect(nsLine("x2", "Nick Bee is suspended."))
}

// max_logins counts SASL and NS RECOVER logins too.
func TestMaxLoginsSASLAndRecover(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Services.MaxLogins = 1 })
	bee := registered(t, s, "Bee")
	bee.send("NICK Bee2")
	bee.expect("NICK")

	c := saslTry(t, s, "y1", "Bee")
	c.expect(" 904 y1 :SASL authentication failed: account Bee has reached the maximum number of simultaneous logins")

	r := login(t, s, "y2")
	r.send("NS RECOVER Bee " + pw)
	r.expect(nsLine("y2", "Account Bee has already reached the maximum number of simultaneous logins (1)."))
}

// INVITE to a name that can't be a channel is refused, not passed on.
func TestInviteBadChannelName(t *testing.T) {
	s := testServer(t, nil)
	a := login(t, s, "Ann")
	b := login(t, s, "Bob")
	a.send("INVITE Bob nochan")
	a.expect(" 403 Ann nochan :No such channel")
	a.send("INVITE Bob #fine")
	a.expect(" 341 Ann Bob #fine")
	b.expect("INVITE Bob #fine")
}

// An oper with flag a can't take over an oper entry that has flags it
// lacks: no new password, host or DISABLE, and no UNDO bringing one back.
func TestOperCantTouchHigherEntry(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")
	registered(t, s, "Bee")
	b.send("AS OPER ember ADD FLAGS a HOST *@* PASSWORD " + emberPW)
	b.expect("ember is an oper now")
	b.send("AS OPER Bee ADD CLASS netadmin HOST *@* PASSWORD " + emberPW + "-bee")
	b.expect("Bee is an oper now")
	e.send("OPER ember " + emberPW)
	e.expect(" 381 ember ")

	for _, cmd := range []string{"SET PASSWORD Taken-Over-2026", "SET HOST *@10.9.9.9", "DISABLE bye", "SET TITLE x", "DEL"} {
		e.send("AS OPER Bee " + cmd)
		e.expect(asLine("ember", "You can't change Bee: they have flag b and you don't."))
	}
	// UNDO of the admin's own add would delete Bee: out of reach too.
	n := lastChange(t, b)
	e.send("AS UNDO " + n)
	e.expect(asLine("ember", "You can't change Bee: they have flag b and you don't."))
	b.send("AS OPER Bee SET TITLE still mine")
	b.expect("Bee")
}
