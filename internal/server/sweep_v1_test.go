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
