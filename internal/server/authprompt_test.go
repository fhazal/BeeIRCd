package server

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// saslLogin connects, logs in to account with SASL before registering,
// and registers as nick with this user name.
func saslLogin(t *testing.T, s *Server, nick, user, account string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("CAP LS 302")
	c.expect("CAP * LS")
	c.send("CAP REQ :sasl")
	c.expect("CAP * ACK :sasl")
	c.send("NICK " + nick)
	c.send("USER " + user + " 0 * :" + nick + " Real")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + base64.StdEncoding.EncodeToString([]byte(account+"\x00"+account+"\x00"+pw)))
	c.expect(" 903 ")
	c.send("CAP END")
	return c
}

// held connects as nick and waits for the login prompt.
func held(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("NICK " + nick)
	c.send("USER " + nick + " 0 * :" + nick + " Real")
	return c
}

// A soft AKILL holds people who aren't logged in and asks them to; /AUTH
// lets them in, a wrong password lets them try again, SASL users pass,
// and a hard ban still wins.
func TestSoftAkillLoginPrompt(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	ann := registered(t, s, "ann")
	guest := login(t, s, "guest")

	b.send("OS AKILL ADD %*@127.0.0.0/8 log in first")
	b.expect(osLine("Boss", "AKILL added: %*@127.0.0.0/8 for 30 days (log in first). 1 person removed."))
	guest.expect(" 465 guest :You are banned from this network: log in first")
	ann.send("PING still-here")
	ann.expect("PONG")
	b.send("OS AKILL ADD %%*@127.0.0.1 two")
	b.expect(osLine("Boss", "Give one % before the mask."))

	// Held: two notices, nothing else, then /AUTH.
	c := held(t, s, "annie")
	c.expect("*** This network asks people from your address to log in to a registered nick.")
	c.expect("*** Turn on SASL in your client, or type now: /AUTH nick:password")
	b.expect("AKILL %*@127.0.0.0/8 holds annie (~annie@127.0.0.1) for a login")
	c.send("PING x")
	c.send("AUTH ann:wrong-password")
	c.expect("*** Login failed. Check the nick and password and try /AUTH again.")
	c.send("AUTH")
	c.expect(" 461 ")
	c.send("AUTH ann")
	c.expect("*** Type /AUTH nick:password")
	c.send("AUTH ann:" + pw)
	c.expect(" 900 annie ")
	c.expect(" 001 annie ")
	c.send("AUTH ann:" + pw)
	c.expect("*** You are already connected. To log in, use /msg NickServ IDENTIFY.")

	// /AUTH from someone not held.
	r := raw(t, s)
	r.send("AUTH ann:" + pw)
	r.expect("*** /AUTH is for logging in when the server asks for it.")

	// SASL before registering: never asked.
	sl := saslLogin(t, s, "sasly", "sasly", "ann")
	sl.expect(" 001 sasly ")

	// A hard ban comes first, even for someone logged in.
	b.send("OS AKILL ADD ~hard@127.0.0.1 no way")
	b.expect(osLine("Boss", "AKILL added: ~hard@127.0.0.1"))
	hd := saslLogin(t, s, "hardy", "hard", "ann")
	hd.expect(" 465 hardy :You are banned from this network: no way")

	// The AKILL list shows the % mask.
	b.send("OS AKILL LIST")
	b.expect("%*@127.0.0.0/8")
}

// The prompt runs out, its texts and time change with AdminServ SET, and
// with it off a soft ban refuses at once.
func TestLoginPromptTimeoutAndSettings(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("OS AKILL ADD %*@127.0.0.0/8 log in first")
	b.expect("AKILL added: %*@127.0.0.0/8")

	c := held(t, s, "late")
	c.expect("/AUTH nick:password")
	s.onLoop(func() {
		for _, x := range s.clients {
			if x.authHeld {
				x.authUntil = time.Now().Add(-time.Second)
			}
		}
		s.checkTimeouts(time.Now())
	})
	c.expect(" 465 late :You are banned from this network: log in first")
	c.expect("ERROR :Closing Link")

	b.send("AS SET auth-prompt-text Please log in to your Hive account.")
	b.expect(asLine("Boss", "auth-prompt-text"))
	b.send("AS SET auth-prompt-fail Nope, try again.")
	b.expect(asLine("Boss", "auth-prompt-fail"))
	b.send("AS SET auth-prompt-time 2m")
	b.expect(asLine("Boss", "auth-prompt-time"))
	b.send("AS SET auth-prompt-time 5s")
	b.expect(asLine("Boss", "auth-prompt-time"))
	c = held(t, s, "again")
	c.expect("*** Please log in to your Hive account.")
	c.expect("/AUTH nick:password")
	s.onLoop(func() {
		for _, x := range s.clients {
			if x.authHeld {
				if d := time.Until(x.authUntil); d < 110*time.Second || d > 2*time.Minute {
					t.Errorf("hold runs %s, want 2m", d)
				}
			}
		}
	})
	c.send("AUTH nobody:" + pw)
	c.expect("*** Nope, try again.")

	b.send("AS SET auth-prompt-text default")
	b.expect(asLine("Boss", "auth-prompt-text"))
	s.onLoop(func() {
		if got := s.cfg.AuthPrompt.Text(); !strings.HasPrefix(got, "This network asks") {
			t.Errorf("default text: %q", got)
		}
	})

	b.send("AS SET auth-prompt off")
	b.expect(asLine("Boss", "auth-prompt"))
	off := held(t, s, "nope")
	off.expect(" 465 nope :You are banned from this network: log in first (log in with SASL to connect)")
	b.send("AS HISTORY 1")
	b.expect("auth-prompt")
}

// A soft K-line spares people who are logged in, at once and at connect.
func TestSoftKline(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	ann := registered(t, s, "ann")
	guest := login(t, s, "guest")
	b.send("KLINE +1h %*@127.0.0.1 log in first")
	guest.expect(" 465 guest ")
	ann.send("PING still-here")
	ann.expect("PONG")
	c := held(t, s, "kay")
	c.expect("/AUTH nick:password")
	c.send("AUTH ann:" + pw)
	c.expect(" 001 kay ")
}

const softDNSBLConf = `
[[dnsbl]]
name = "bad.example"
action = "soft-reject"
reason = "Your IP %i is listed in %b"
`

// A DNS blocklist with action soft-reject asks for a login instead.
func TestDNSBLSoftReject(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"1.0.0.127.bad.example": {"127.0.0.2"}}}
	s, _, _ := fileServerWith(t, softDNSBLConf, func(s *Server) { s.lookupA = dns.lookup })
	registered(t, s, "ann").send("QUIT")
	s.onLoop(func() { s.dnsblSkip = func(string) bool { return false } })
	c := held(t, s, "listed")
	c.expect("*** This network asks people from your address to log in")
	c.send("AUTH ann:" + pw)
	c.expect(" 001 listed ")

	sl := saslLogin(t, s, "sasly", "sasly", "ann")
	sl.expect(" 001 sasly ")

	s.onLoop(func() { off := false; s.cfg.AuthPrompt.Enabled = &off })
	n := held(t, s, "nope")
	n.expect(" 465 nope :You are banned from this network: Your IP 127.0.0.1 is listed in bad.example (log in with SASL to connect)")
}
