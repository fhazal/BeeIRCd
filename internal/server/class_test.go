package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/geoip/geoiptest"
)

// clientNamed finds the local connection of nick (on the loop).
func clientNamed(t *testing.T, s *Server, nick string) *Client {
	t.Helper()
	var out *Client
	onLoop(t, s, func() {
		for _, c := range s.clients {
			if c.user != nil && strings.EqualFold(c.user.Nick, nick) {
				out = c
			}
		}
	})
	if out == nil {
		t.Fatalf("no local client %s", nick)
	}
	return out
}

// registerRaw sends NICK and USER and leaves the reading to the caller.
func registerRaw(t *testing.T, s *Server, nick string) *tc {
	t.Helper()
	c := raw(t, s)
	c.nick = nick
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick + " Real")
	return c
}

// Without [[class]] blocks nothing changes: nobody gets a class, WHOIS
// has no class line, and STATS Y shows only the default class.
func TestClassNoneUnchanged(t *testing.T) {
	s, _, _ := fileServerWith(t, "", nil)
	b := boss(t, s)
	x := registerRaw(t, s, "x")
	x.expect("CHANLIMIT=#:30")
	x.expect("MODE x +i")
	b.send("WHOIS x")
	b.quiet("is in class")
	onLoop(t, s, func() {
		for _, c := range s.clients {
			if c.class != nil || c.floodClass.Load() != nil {
				t.Errorf("%s has a class", c.nick)
			}
		}
		if got := s.sessionMax("127.0.0.1"); got != 100 {
			t.Errorf("session max %d, want [limits] 100", got)
		}
	})
	b.send("STATS Y")
	b.expect(" 218 Boss Y default :2 in class; session_limit 100,")
	b.expect(" 219 Boss Y :End of /STATS report")
}

const classConf = `
[[security_group]]
name = "blocked"
mask = ["bad*!*@*"]

[[security_group]]
name = "few"
mask = ["s*!*@*"]

[[class]]
name = "nope"
security_group = ["blocked"]
deny = true

[[class]]
name = "small"
security_group = ["few"]
max_users = 1

[[class]]
name = "guests"
security_group = ["unknown-users"]
session_limit = 3
max_channels = 2
sendq = 8192
ping_interval = "30s"
flood_burst = 50
flood_rate = 20
`

// A class's deny, max_users, session_limit and limits apply when someone
// registers; opers see the class in WHOIS and STATS Y.
func TestClassLimits(t *testing.T) {
	s, _, _ := fileServerWith(t, classConf, nil)
	// Boss identifies after registering: like everyone without SASL,
	// unknown-users when the class is picked, so a guest.
	b := boss(t, s)

	// deny: refused like a ban
	d := registerRaw(t, s, "bad1")
	d.expect(" 465 bad1 :You are banned from this network: Your connection class (nope) may not connect here")
	d.expect("ERROR :Closing Link")

	// max_users: the second one is refused, the first stays
	s1 := login(t, s, "s1")
	s2 := registerRaw(t, s, "s2")
	s2.expect("ERROR :Closing Link: 127.0.0.1 (Too many connections in your class (small))")
	s1.sync()

	// guests: session_limit 3 counts everyone from the IP (Boss, s1, g1)
	g := registerRaw(t, s, "g1")
	g.expect("CHANLIMIT=#:2")
	g.expect("MODE g1 +i")
	g2 := registerRaw(t, s, "g2")
	g2.expect("ERROR :Closing Link: 127.0.0.1 (Too many connections from your IP)")
	// an OperServ session exception still wins over the class
	b.send("OS EXCEPTION ADD 127.0.0.1 10 testing")
	b.expect("127.0.0.1 may now have 10 connections.")
	g3 := login(t, s, "g3")

	// max_channels
	g.send("JOIN #a,#b,#c")
	g.expect(" 405 g1 #c :You have joined too many channels")

	// sendq, ping and flood come from the class
	c := clientNamed(t, s, "g1")
	bc := clientNamed(t, s, "Boss")
	onLoop(t, s, func() {
		l := s.limitsOf(c)
		if c.class == nil || c.class.Name != "guests" || l.SendQ != 8192 || l.PingInterval != 30*time.Second || l.MaxChannels != 2 {
			t.Errorf("g1 limits: %+v", l)
		}
		if p := c.floodClass.Load(); p == nil || p.burst != 50 || p.rate != 20 {
			t.Errorf("g1 flood pace: %+v", p)
		}
		if bc.class == nil || bc.class.Name != "guests" {
			t.Errorf("Boss is in %v", bc.class)
		}
		if got := s.sessionMax("127.0.0.1"); got != 10 {
			t.Errorf("session max with the exception: %d", got)
		}
	})

	b.send("WHOIS g3")
	b.expect(" 320 Boss g3 :is in class guests")
	b.send("WHOIS Boss")
	b.expect(" 320 Boss Boss :is in class guests")
	g3.send("WHOIS g1")
	g3.quiet("is in class") // opers only

	b.send("STATS Y")
	b.expect(" 218 Boss Y nope :0 in class; session_limit 100, max_users 5000")
	b.expect(" 218 Boss Y small :1 in class; session_limit 100, max_users 1, ")
	b.expect(" 218 Boss Y guests :3 in class; session_limit 3, max_users 5000, sendq 8192, ping 30s/")
	b.expect(" 218 Boss Y default :0 in class")
	b.expect(" 219 Boss Y :End of /STATS report")
	g3.send("STATS Y")
	g3.quiet(" 218 ")
}

// A class with a looser session_limit than [limits] lets its people in:
// the connect-time check waits for the class.
func TestClassLooserSessionLimit(t *testing.T) {
	s, _, _ := fileServerWith(t, `
[[class]]
name = "loose"
security_group = ["unknown-users"]
session_limit = 5
`, func(s *Server) { s.cfg.Limits.SessionLimit = 2 })
	login(t, s, "l1")
	login(t, s, "l2")
	login(t, s, "l3")
	login(t, s, "l4").sync()
}

// A rehash works out everyone's class again, and nobody is dropped when
// their new class is already over max_users.
func TestClassRehash(t *testing.T) {
	const block = `
[[security_group]]
name = "p-nicks"
mask = ["p*!*@*"]

[[class]]
name = "first"
security_group = ["p-nicks"]
max_channels = 3
`
	s, path, _ := fileServerWith(t, block, nil)
	b := boss(t, s)
	p1 := login(t, s, "p1")
	p2 := login(t, s, "p2")
	b.send("WHOIS p1")
	b.expect(" 320 Boss p1 :is in class first")

	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(text), `name = "first"`, "name = \"second\"\nmax_users = 1\nflood_burst = 7\nflood_rate = 3", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	b.send("REHASH")
	b.expect(" 382 Boss ")
	b.sync()
	p1.sync()
	p2.sync()
	b.send("WHOIS p2")
	b.expect(" 320 Boss p2 :is in class second")
	b.send("STATS Y")
	b.expect(" 218 Boss Y second :2 in class; session_limit 100, max_users 1,")
	c := clientNamed(t, s, "p1")
	onLoop(t, s, func() {
		if p := c.floodClass.Load(); p == nil || p.burst != 7 || p.rate != 3 {
			t.Errorf("p1 flood pace after rehash: %+v", p)
		}
		if l := s.limitsOf(c); l.MaxChannels != 3 {
			t.Errorf("p1 max_channels after rehash: %d", l.MaxChannels)
		}
	})
	p3 := registerRaw(t, s, "p3")
	p3.expect("(Too many connections in your class (second))")

	// Classes gone: back to the default class and [limits].
	if !strings.Contains(string(text), block) {
		t.Fatalf("the class block isn't in the file as written:\n%s", text)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(text), block, "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	b.send("REHASH")
	b.expect(" 382 Boss ")
	b.sync()
	onLoop(t, s, func() {
		if c.class != nil {
			t.Errorf("p1 still in %s", c.class.Name)
		}
		if p := c.floodClass.Load(); p == nil || p.burst != 10000 {
			t.Errorf("p1 flood pace back in the default class: %+v", p)
		}
	})
	b.send("WHOIS p1")
	b.quiet("is in class")
}

// Classes by country, and STATS G counting people per country.
func TestClassByCountry(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "country.mmdb")
	if err := os.WriteFile(db, geoiptest.Build(24, 6, []geoiptest.Net{{P: "127.0.0.0/8", Data: geoiptest.Country("NL", "Netherlands")}}), 0o644); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	s, _, _ := fileServerWith(t, `
[[security_group]]
name = "from-nl"
mask = ["~C:NL"]

[[class]]
name = "dutch"
security_group = ["from-nl"]
max_channels = 1
`, func(s *Server) {
		s.cfg.GeoIP = config.GeoIP{Enabled: &on, Download: &off, CountryDB: db, ASNDB: filepath.Join(dir, "asn.mmdb")}
	})
	b := boss(t, s)
	n := login(t, s, "n1")
	n.send("JOIN #one,#two")
	n.expect(" 405 n1 #two :You have joined too many channels")
	b.send("WHOIS n1")
	b.expect(" 320 Boss n1 :is in class dutch")
	b.send("STATS G")
	b.expect(" 801 Boss 2 NL :Netherlands")
	b.expect(" 219 Boss G :End of /STATS report")
	n.send("STATS G")
	n.quiet(" 801 ")
}
