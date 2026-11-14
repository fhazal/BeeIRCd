package server

import (
	"strings"
	"testing"
)

// An oper entry with a certificate fingerprint: /oper needs that TLS
// certificate; with the password turned off the certificate alone opers
// (the password typed is ignored, as with UnrealIRCd's certfp blocks).
func TestOperByCertificate(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	registered(t, s, "guard")
	cert, fp := clientCert(t)
	other, _ := clientCert(t)

	b.send("AS OPER guard ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("guard is an oper now")
	b.send("AS OPER guard SET PASSWORD OFF")
	b.expect("Give guard a certificate first")
	b.send("AS OPER guard SET CERT nothex")
	b.expect("SHA-256 in hex")
	colons := ""
	for i := 0; i < len(fp); i += 2 {
		colons += strings.ToUpper(fp[i:i+2]) + ":"
	}
	b.send("AS OPER guard SET CERT " + strings.TrimSuffix(colons, ":"))
	b.expect("guard: class globop")

	// Certificate and password both set: both are needed.
	plain := login(t, s, "plainbot")
	plain.send("OPER guard " + emberPW)
	plain.expect(" 491 plainbot :No oper entry for you from this host")
	wrong := certLogin(t, s, "wrongbot", &other)
	wrong.send("OPER guard " + emberPW)
	wrong.expect(" 491 wrongbot :No oper entry for you from this host")
	right := certLogin(t, s, "rightbot", &cert)
	right.send("OPER guard wrong-password")
	right.expect(" 464 rightbot :Password incorrect")
	right.send("OPER guard " + emberPW)
	right.expect(" 381 rightbot :You are now an IRC operator")

	// Password off: the certificate alone, whatever is typed.
	b.send("AS OPER guard SET PASSWORD OFF")
	b.expect("guard now opers with their certificate alone.")
	bot := certLogin(t, s, "guardbot", &cert)
	bot.send("OPER guard x")
	bot.expect(" 381 guardbot :You are now an IRC operator")
	plain.send("OPER guard x")
	plain.expect(" 491 plainbot :No oper entry for you from this host")

	// CERT OFF needs a password first.
	b.send("AS OPER guard SET CERT OFF")
	b.expect("guard has no oper password")
}

func TestCertFPs(t *testing.T) {
	ok := strings.Repeat("ab", 32)
	if fps, good := certFPs([]string{strings.ToUpper(ok)}); !good || fps[0] != ok {
		t.Errorf("upper case: %v %v", fps, good)
	}
	for _, bad := range [][]string{{"abc"}, {strings.Repeat("zz", 32)}, {ok, ok, ok, ok, ok, ok}, nil} {
		if _, good := certFPs(bad); good {
			t.Errorf("certFPs(%v) took it", bad)
		}
	}
	if !certMatches([]string{ok}, strings.ToUpper(ok)) || certMatches([]string{ok}, "") || certMatches(nil, ok) {
		t.Error("certMatches")
	}
}
