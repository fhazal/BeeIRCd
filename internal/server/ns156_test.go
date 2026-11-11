package server

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// Step 1.5.6a: MONITOR, NickServ CERT and SASL EXTERNAL, AJOIN, GLIST,
// SUSPEND, LIST, GETEMAIL, UPDATE and the new SET options.

// clientCert makes a TLS client certificate and its SHA-256 fingerprint.
func clientCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "bee client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:])
}

// rawTLS connects to the TLS port showing cert (if any), without registering.
func rawTLS(t *testing.T, s *Server, cert *tls.Certificate) *tc {
	t.Helper()
	conf := &tls.Config{InsecureSkipVerify: true}
	if cert != nil {
		conf.Certificates = []tls.Certificate{*cert}
	}
	conn, err := tls.Dial("tcp", s.Addrs()[1].String(), conf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &tc{t: t, conn: conn, r: bufio.NewReader(conn)}
}

// certLogin registers nick over TLS with cert.
func certLogin(t *testing.T, s *Server, nick string, cert *tls.Certificate) *tc {
	t.Helper()
	c := rawTLS(t, s, cert)
	c.nick = nick
	c.send("NICK " + nick)
	c.send("USER " + strings.ToLower(nick) + " 0 * :" + nick + " Real")
	c.expect(":" + nick + " MODE " + nick + " +i")
	return c
}

func TestMonitor(t *testing.T) {
	s := testServer(t, nil)
	w := login(t, s, "watcher")
	w.send("VERSION")
	w.expect("MONITOR=100")
	w.send("MONITOR + Bee,ghost")
	w.expect(" 731 watcher :Bee,ghost")
	bee := login(t, s, "Bee")
	w.expect(" 730 watcher :Bee!~bee@127.0.0.1")
	w.send("MONITOR S")
	w.expect(" 730 watcher :Bee!~bee@127.0.0.1")
	w.expect(" 731 watcher :ghost")
	bee.send("NICK Bee2")
	w.expect(" 731 watcher :Bee")
	w.send("MONITOR + bee2")
	w.expect(" 730 watcher :Bee2!~bee@127.0.0.1")
	bee.send("QUIT :bye")
	w.expect(" 731 watcher :Bee2")
	w.send("MONITOR - ghost")
	w.send("MONITOR L")
	l := w.expect(" 732 watcher :")
	if strings.Contains(l, "ghost") || !strings.Contains(l, "Bee") {
		t.Errorf("MONITOR L after - ghost: %s", l)
	}
	w.expect(" 733 watcher :End of MONITOR list")
	w.send("MONITOR C")
	w.send("MONITOR L")
	w.expect(" 733 watcher :End of MONITOR list")
	var many []string
	for i := 0; i < 101; i++ {
		many = append(many, "n"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	w.send("MONITOR + " + strings.Join(many, ","))
	w.expect(" 734 watcher 100 ")
	w.send("MONITOR X")
	w.expect(" 421 watcher MONITOR :Unknown MONITOR subcommand")
}

func TestCertAndSASLExternal(t *testing.T) {
	s := testServer(t, nil)
	cert, fp := clientCert(t)
	bee := certLogin(t, s, "Bee", &cert)
	bee.send("NS REGISTER " + pw)
	bee.expect("Bee is now registered to you.")
	bee.send("WHOIS Bee")
	bee.expect(" 276 Bee Bee :has client certificate fingerprint " + fp)
	bee.send("NS CERT ADD")
	bee.expect(nsLine("Bee", fp+" added to Bee's certificate list."))
	bee.send("NS CERT ADD")
	bee.expect(nsLine("Bee", "Fingerprint "+fp+" already present on Bee's certificate list."))
	bee.send("NS CERT LIST")
	bee.expect(nsLine("Bee", "Certificate list for Bee:"))
	bee.expect(nsLine("Bee", "    "+fp))

	other := registered(t, s, "other")
	other.send("NS CERT ADD")
	other.expect(nsLine("other", "You are not using a client certificate."))
	other.send("NS CERT LIST Bee")
	other.expect(nsLine("other", "Access denied."))
	other.send("WHOIS Bee")
	other.expect(" 318 other Bee ")
	other.send("WHOIS Bee")
	other.quiet(" 276 ")

	// Connecting with the certificate identifies you, whatever your nick.
	c2 := certLogin(t, s, "Hive", &cert)
	c2.expect(" 900 Hive ")
	c2.expect(nsLine("Hive", "SSL certificate fingerprint accepted, you are now identified to Bee."))

	// SASL EXTERNAL logs in before registration.
	sx := rawTLS(t, s, &cert)
	sx.send("CAP LS 302")
	sx.expect("sasl=PLAIN,EXTERNAL")
	sx.send("CAP REQ :sasl")
	sx.expect("ACK :sasl")
	sx.send("AUTHENTICATE EXTERNAL")
	sx.expect("AUTHENTICATE +")
	sx.send("AUTHENTICATE +")
	sx.expect(" 900 * ")
	sx.expect(" 903 * :SASL authentication successful")
	sx.send("NICK Drone")
	sx.send("USER drone 0 * :Drone")
	sx.send("CAP END")
	sx.expect(" 001 Drone ")
	// ... and fails without a certificate, or with someone else's account.
	plain := raw(t, s)
	plain.send("CAP REQ :sasl")
	plain.send("AUTHENTICATE EXTERNAL")
	plain.expect("AUTHENTICATE +")
	plain.send("AUTHENTICATE +")
	plain.expect(" 904 * :SASL authentication failed")
	wrong := rawTLS(t, s, &cert)
	wrong.send("CAP REQ :sasl")
	wrong.send("AUTHENTICATE EXTERNAL")
	wrong.expect("AUTHENTICATE +")
	wrong.send("AUTHENTICATE b3RoZXI=") // authzid "other"
	wrong.expect(" 904 * :SASL authentication failed")

	bee.send("NS CERT DEL " + strings.ToUpper(fp))
	bee.expect(nsLine("Bee", fp+" deleted from Bee's certificate list."))
	c3 := certLogin(t, s, "Hive2", &cert)
	c3.send("PING :x")
	c3.expect("PONG")
	c3.quiet("certificate fingerprint accepted")
}

func TestAjoinGlistAndSetOptions(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	host := login(t, s, "host")
	host.send("JOIN #closed")
	host.expect(" 366 ")
	host.send("MODE #closed +i")
	host.expect("MODE #closed +i")

	bee.send("NS AJOIN ADD #a,#b")
	bee.expect(nsLine("Bee", "#a, #b added to Bee's auto join list."))
	bee.send("NS AJOIN ADD #a")
	bee.expect(nsLine("Bee", "#a is already on Bee's auto join list."))
	bee.send("NS AJOIN ADD #closed")
	bee.expect("added to Bee's auto join list.")
	bee.send("NS AJOIN LIST")
	bee.expect(nsLine("Bee", "Bee's auto join list:"))
	bee.expect(nsLine("Bee", "  1       #a"))
	bee.send("NS LOGOUT")
	bee.expect(" 901 ")
	bee.send("NS IDENTIFY " + pw)
	bee.expect("JOIN #a")
	bee.expect("JOIN #b")
	host.quiet("JOIN #closed") // +i and no access: left out
	bee.send("NS AJOIN DEL #b,#zz")
	bee.expect(nsLine("Bee", "#zz was not found on Bee's auto join list."))
	bee.expect(nsLine("Bee", "#b was removed from Bee's auto join list."))

	bee.send("NS GLIST")
	bee.expect(nsLine("Bee", "List of nicknames in your group:"))
	bee.expect(nsLine("Bee", "  Bee   "))
	bee.expect(nsLine("Bee", "1 nickname(s) in the group."))

	// SET AUTOOP OFF: no status on join, UP still works.
	bee.send("NS SET AUTOOP OFF")
	bee.expect(nsLine("Bee", "Services will no longer set status modes on Bee in channels."))
	host.send("JOIN #bchat")
	host.expect(" 366 host #bchat ")
	bee.send("PART #bchat")
	bee.send("JOIN #bchat")
	bee.expect(" 366 Bee #bchat ")
	host.quiet("+o Bee")
	bee.send("CS UP #bchat")
	host.expect(csSrc + "MODE #bchat +o Bee")

	// PRIVATE hides you from LIST; URL shows in INFO.
	host.send("NS LIST B*")
	host.expect(nsLine("host", "End of list - 0/0 matches shown."))
	bee.send("NS SET PRIVATE OFF")
	bee.expect(nsLine("Bee", "Private option is now off for Bee."))
	host.send("NS LIST B*")
	host.expect(nsLine("host", "  Bee   [Hostname hidden]"))
	host.expect(nsLine("host", "End of list - 1/1 matches shown."))
	bee.send("NS SET URL https://bajubesi.org")
	bee.expect(nsLine("Bee", "URL for Bee set to https://bajubesi.org."))
	host.send("NS INFO Bee")
	host.expect(nsLine("host", "  URL           https://bajubesi.org"))
	bee.send("NS SET URL")
	bee.expect(nsLine("Bee", "URL for Bee unset."))

	// DISPLAY: a grouped nick becomes the account's name.
	g := login(t, s, "BeeToo")
	g.send("NS GROUP Bee " + pw)
	g.expect("now")
	bee.send("NS SET DISPLAY BeeToo")
	bee.expect(nsLine("Bee", "The new display is now BeeToo."))
	bee.send("NS SET DISPLAY nobody")
	bee.expect(nsLine("Bee", "The new display MUST be a nickname of the nickname group BeeToo."))

	// KEEPMODES: user modes come back when identifying.
	bee.send("MODE Bee +w")
	bee.expect("MODE Bee +w")
	bee.send("NS SET KEEPMODES ON")
	bee.expect(nsLine("Bee", "Keep modes for BeeToo is now on."))
	bee.send("QUIT")
	bee.expect("ERROR")
	again := login(t, s, "Bee")
	again.send("NS IDENTIFY " + pw)
	again.expect(":NickServ!services@irc.test.local MODE Bee +w")
}

func TestRegDelayAndMaxLogins(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.Services.RegDelay = &config.Duration{Duration: 2 * time.Second}
		c.Services.MaxLogins = 1
	})
	c := login(t, s, "Bee")
	c.send("NS REGISTER " + pw)
	c.expect(nsLine("Bee", "You must have been using this nick for at least 2 seconds to register."))
	time.Sleep(2100 * time.Millisecond)
	c.send("NS REGISTER " + pw)
	c.expect("Bee is now registered to you.")
	second := login(t, s, "other")
	second.send("NS IDENTIFY Bee " + pw)
	second.expect(nsLine("other", "Account Bee has already reached the maximum number of simultaneous logins (1)."))
}

func TestSuspendListGetEmailUpdate(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bee := registered(t, s, "Bee")
	bee.send("NS SET EMAIL bee@example.org")
	bee.expect("bee@example.org")

	b.send("NS GETEMAIL *@example.org")
	b.expect(nsLine("Boss", "Email matched: Bee (bee@example.org) to *@example.org."))
	b.send("NS GETEMAIL *@nowhere")
	b.expect(nsLine("Boss", "No registrations matching *@nowhere were found."))
	bee.send("NS GETEMAIL *")
	bee.expect(nsLine("Bee", "Access denied."))
	bee.send("NS UPDATE")
	bee.expect(nsLine("Bee", "Status updated (memos, vhost, chmodes, flags)."))

	b.send("NS SASET NOEXPIRE Bee ON")
	b.expect(nsLine("Boss", "Nick Bee will not expire."))
	b.send("NS LIST * NOEXPIRE")
	b.expect(nsLine("Boss", "  !Bee "))
	b.send("NS GLIST Bee")
	b.expect(nsLine("Boss", "  Bee   Does not expire"))

	b.send("NS SUSPEND Boss")
	b.expect(nsLine("Boss", "You may not suspend other Services Operators' nicknames."))
	b.send("NS SUSPEND Bee +1d spam")
	bee.expect(" 901 ")
	bee.expect("Nick Bee is suspended.")
	b.expect(nsLine("Boss", "Nick Bee is now suspended."))
	b.send("NS SUSPEND Bee again")
	b.expect(nsLine("Boss", "Bee is already suspended."))
	x := login(t, s, "x")
	x.send("NS IDENTIFY Bee " + pw)
	x.expect(nsLine("x", "Nick Bee is suspended."))
	x.send("NICK Bee")
	x.expect(nsLine("Bee", "Nick Bee is suspended."))
	x.send("NS INFO Bee")
	x.expect(nsLine("Bee", "  Suspended     This nickname is suspended."))
	x.expect(nsLine("Bee", "  Suspended by  Boss"))
	x.expect(nsLine("Bee", "  Reason        spam"))
	b.send("NS LIST * SUSPENDED")
	b.expect(nsLine("Boss", "  !Bee  [Suspended]"))
	b.send("NS UNSUSPEND Bee")
	b.expect(nsLine("Boss", "Nick Bee is now released."))
	b.send("NS UNSUSPEND Bee")
	b.expect(nsLine("Boss", "Nick Bee is not suspended."))
	x.send("NS IDENTIFY " + pw)
	x.expect("You are identified as Bee.")
}
