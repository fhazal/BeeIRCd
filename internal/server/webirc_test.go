package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// WEBIRC (BeeRoom / The Lounge): the person behind a trusted gateway is
// the one that counts. Tests connect from 127.0.0.1 = the gateway.

const webircPW = "gateway-secret-0123456789"

func webircGW(hosts ...string) func(*config.Config) {
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	return func(c *config.Config) {
		c.WebIRC = []config.WebIRC{{Name: "beeroom", Password: webircPW, Hosts: hosts}}
	}
}

// viaGateway sends WEBIRC for ip and registers as nick.
func viaGateway(t *testing.T, s *Server, nick, ip string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	// One write, as a gateway sends it: when the server refuses the person
	// (limit, ban) it closes right after WEBIRC, and separate writes of
	// NICK and USER could then hit a closed socket (seen once in 3 -race runs).
	lines := "WEBIRC " + webircPW + " beeroom user.example.net " + ip + " :secure\r\n" +
		"NICK " + nick + "\r\nUSER " + strings.ToLower(nick) + " 0 * :" + nick + "\r\n"
	if _, err := c.conn.Write([]byte(lines)); err != nil {
		t.Fatalf("%s: %v", nick, err)
	}
	return c
}

func TestWebircRealIPAndHost(t *testing.T) {
	s := testServer(t, webircGW())
	w := viaGateway(t, s, "Web", "203.0.113.7")
	w.expect(" 001 Web ")
	w.send("WHOIS Web")
	w.expect(" 311 Web Web ~web user.example.net * :Web")
	w.expect(" 320 Web Web :is connecting through WEBIRC gateway beeroom")
	if u := gwUser(s, "Web"); u == nil || u.IP != "203.0.113.7" || u.RealHost != "user.example.net" {
		t.Fatalf("user = %+v", u)
	}
	// Someone else (no flag h) doesn't see the gateway line.
	o := login(t, s, "Other")
	o.send("WHOIS Web")
	o.quiet("WEBIRC gateway")
}

func TestWebircBadHostnameUsesIP(t *testing.T) {
	s := testServer(t, webircGW())
	c := raw(t, s)
	c.nick = "Odd"
	c.send("WEBIRC " + webircPW + " beeroom 10.0.0.1 2001:db8::5")
	c.send("NICK Odd")
	c.send("USER odd 0 * :Odd")
	c.expect(" 001 Odd ")
	c.send("WHOIS Odd")
	c.expect(" 311 Odd Odd ~odd 2001:db8::5 * :Odd") // a claimed IP (10.0.0.1) is not a host: the real IP shows
}

func TestWebircRefusals(t *testing.T) {
	s := testServer(t, webircGW())
	c := raw(t, s)
	c.send("WEBIRC wrong-password-0123456789 beeroom h.example.net 203.0.113.7")
	c.expect("ERROR :Closing Link: 127.0.0.1 (WEBIRC: wrong password)")
	c = raw(t, s)
	c.send("WEBIRC " + webircPW + " beeroom h.example.net not-an-ip")
	c.expect("WEBIRC: the IP is not valid")
	c = raw(t, s)
	c.send("WEBIRC " + webircPW + " beeroom h.example.net 0.0.0.0")
	c.expect("WEBIRC: the IP is not valid")

	other := testServer(t, webircGW("192.0.2.10")) // the gateway lives elsewhere
	c = raw(t, other)
	c.send("WEBIRC " + webircPW + " beeroom h.example.net 203.0.113.7")
	c.expect("WEBIRC: this address is not a trusted gateway")
}

func TestWebircAfterRegistrationIgnored(t *testing.T) {
	s := testServer(t, webircGW())
	c := login(t, s, "Early")
	c.send("WEBIRC " + webircPW + " beeroom h.example.net 203.0.113.7")
	c.send("PING :still")
	c.expect("PONG irc.test.local :still")
	if u := gwUser(s, "Early"); u.IP != "127.0.0.1" {
		t.Fatalf("IP changed after registration: %s", u.IP)
	}
}

func TestWebircSecondWebircIgnored(t *testing.T) {
	s := testServer(t, webircGW())
	c := raw(t, s)
	c.nick = "Twice"
	c.send("WEBIRC " + webircPW + " beeroom a.example.net 203.0.113.7")
	c.send("WEBIRC " + webircPW + " beeroom b.example.net 203.0.113.8")
	c.send("NICK Twice")
	c.send("USER twice 0 * :Twice")
	c.expect(" 001 Twice ")
	if u := gwUser(s, "Twice"); u.IP != "203.0.113.7" {
		t.Fatalf("second WEBIRC won: %s", u.IP)
	}
}

// The session limit counts the person, not the gateway.
func TestWebircSessionLimitPerPerson(t *testing.T) {
	s := testServer(t, func(c *config.Config) { webircGW()(c); c.Limits.SessionLimit = 2 })
	viaGateway(t, s, "A1", "203.0.113.7").expect(" 001 A1 ")
	viaGateway(t, s, "A2", "203.0.113.7").expect(" 001 A2 ")
	viaGateway(t, s, "A3", "203.0.113.7").expect("Too many connections from your IP")
	// The gateway itself carries more than 2: other people still get in.
	viaGateway(t, s, "B1", "203.0.113.9").expect(" 001 B1 ")
	viaGateway(t, s, "B2", "198.51.100.4").expect(" 001 B2 ")
}

// Z-lines and network bans hit the person's real IP.
func TestWebircBansUseRealIP(t *testing.T) {
	s := testServer(t, webircGW())
	addBan(s, store.NetBan{Kind: store.BanZline, Mask: "203.0.113.0/24", Reason: "spam"})
	viaGateway(t, s, "Spam", "203.0.113.7").expect("Z-lined: spam")
	viaGateway(t, s, "Fine", "198.51.100.4").expect(" 001 Fine ") // the gateway isn't banned
	addBan(s, store.NetBan{Kind: store.BanAkill, Mask: "*@198.51.100.0/24", Reason: "go away"})
	viaGateway(t, s, "Kill", "198.51.100.5").expect("Banned: go away")
}

// A hashed gateway password works too (checked off the loop).
func TestWebircHashedPassword(t *testing.T) {
	h, err := passwd.Hash(webircPW)
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t, func(c *config.Config) {
		c.WebIRC = []config.WebIRC{{Name: "beeroom", Password: h, Hosts: []string{"127.0.0.0/8"}}}
	})
	viaGateway(t, s, "Hashed", "203.0.113.7").expect(" 001 Hashed ")
	c := raw(t, s)
	c.send("WEBIRC not-the-password-at-all beeroom h.example.net 203.0.113.7")
	c.expect("WEBIRC: wrong password")
}

// A slow host lookup for the gateway's IP must not overwrite the person's.
func TestWebircStaleLookupDropped(t *testing.T) {
	on := true
	s := testServerWith(t, func(c *config.Config) { webircGW()(c); c.Server.ResolveHosts = &on }, func(s *Server) {
		s.resolve = func(ctx context.Context, ip string) (string, bool) {
			time.Sleep(300 * time.Millisecond)
			return "gateway.example.org", true
		}
	})
	w := viaGateway(t, s, "Stale", "203.0.113.7")
	w.expect(" 001 Stale ")
	time.Sleep(500 * time.Millisecond) // the lookup answers now, and is dropped
	w.send("WHOIS Stale")
	w.expect(" 311 Stale Stale ~stale user.example.net * :Stale")
}

// The secure flag counts only when the gateway link itself is safe
// (loopback here), and never makes a plain user +z by itself.
func TestWebircSecureFlag(t *testing.T) {
	s := testServer(t, webircGW())
	viaGateway(t, s, "Safe", "203.0.113.7").expect(" 001 Safe ")
	if u := gwUser(s, "Safe"); !u.TLS {
		t.Fatal("secure over loopback should count as TLS")
	}
	c := raw(t, s)
	c.nick = "Plain"
	c.send("WEBIRC " + webircPW + " beeroom h.example.net 203.0.113.8")
	c.send("NICK Plain")
	c.send("USER plain 0 * :Plain")
	c.expect(" 001 Plain ")
	if u := gwUser(s, "Plain"); u.TLS {
		t.Fatal("no secure flag: not TLS")
	}
}

// gwUser copies nick's user on the server loop, so the test never reads
// the network while the server changes it (race detector).
func gwUser(s *Server, nick string) *state.User {
	var u *state.User
	s.onLoop(func() {
		if p := s.net.UserByNick(nick); p != nil {
			c := *p
			u = &c
		}
	})
	return u
}
