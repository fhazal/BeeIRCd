package acmecert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cert(t *testing.T, names []string, from, to time.Time, selfSigned bool) *x509.Certificate {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: from, NotAfter: to.AddDate(1, 0, 0)}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: from, NotAfter: to}
	parent, signer := ca, caKey
	if selfSigned {
		parent, signer = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return leaf
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	names := []string{"irc.example.org"}
	fresh := cert(t, names, now.AddDate(0, 0, -10), now.AddDate(0, 0, 80), false)
	old := cert(t, names, now.AddDate(0, 0, -70), now.AddDate(0, 0, 20), false)
	for _, tt := range []struct {
		leaf *x509.Certificate
		due  bool
		why  string
	}{
		{nil, true, "no certificate yet"},
		{fresh, false, ""},
		{old, true, "runs out on 2 Dec 2026"},
		{cert(t, names, now, now.AddDate(10, 0, 0), true), true, "self-signed"},
		{cert(t, []string{"other.example.org"}, now, now.AddDate(0, 0, 90), false), true, "doesn't cover irc.example.org"},
	} {
		due, why := Due(tt.leaf, names, now)
		if due != tt.due || !strings.Contains(why, tt.why) {
			t.Errorf("Due = %v %q, want %v %q", due, why, tt.due, tt.why)
		}
	}
	if got := RenewAt(fresh); !got.Equal(now.AddDate(0, 0, 50)) {
		t.Errorf("RenewAt = %v, want 30 days before the end", got)
	}
}

func TestValidDomain(t *testing.T) {
	for name, bad := range map[string]bool{
		"irc.example.org": false, "test.beeircd.org.": false,
		"test.beenet.local": true, "localhost": true, "10.0.0.1": true, "*.example.org": true, "irc.home": true,
	} {
		if got := ValidDomain(name) != ""; got != bad {
			t.Errorf("ValidDomain(%q) = %q", name, ValidDomain(name))
		}
	}
}

func TestPortBusy(t *testing.T) {
	dir := t.TempDir()
	_, err := Obtain(context.Background(), Config{Domains: []string{"x.example.org"}, HTTPAddr: "256.0.0.1:1", AccountKeyFile: filepath.Join(dir, "a.key")})
	if err == nil || !strings.Contains(err.Error(), "can't open port") {
		t.Errorf("err = %v", err)
	}
}

// TestPebble asks a real ACME test CA. Run Pebble first, then:
//
//	BEEIRCD_PEBBLE=https://127.0.0.1:14000/dir BEEIRCD_PEBBLE_HTTP=:5002 go test ./internal/acmecert -run Pebble
func TestPebble(t *testing.T) {
	dir := os.Getenv("BEEIRCD_PEBBLE")
	if dir == "" {
		t.Skip("set BEEIRCD_PEBBLE to a Pebble directory URL")
	}
	tmp := t.TempDir()
	cfg := Config{
		Directory:      dir,
		Email:          "admin@example.org",
		Domains:        []string{"localhost"},
		CertFile:       filepath.Join(tmp, "tls", "cert.pem"),
		KeyFile:        filepath.Join(tmp, "tls", "key.pem"),
		AccountKeyFile: filepath.Join(tmp, "tls", "acme-account.key"),
		HTTPAddr:       os.Getenv("BEEIRCD_PEBBLE_HTTP"),
		// Pebble's own directory certificate is a test one.
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	leaf, err := Obtain(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.VerifyHostname("localhost") != nil || SelfSigned(leaf) {
		t.Errorf("bad certificate: %v", leaf.DNSNames)
	}
	if _, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(cfg.KeyFile); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("key file mode %v", fi.Mode())
	}
	// A second run reuses the account key and replaces the pair.
	if _, err := Obtain(ctx, cfg); err != nil {
		t.Fatalf("renewal: %v", err)
	}
	t.Logf("got %v, valid until %s", leaf.DNSNames, leaf.NotAfter)
}
