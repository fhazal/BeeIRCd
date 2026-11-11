package server

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/acmecert"
	"github.com/fhazal/beeircd/internal/config"
)

// writeCASigned writes a pair signed by a throwaway CA, like Let's Encrypt's.
func writeCASigned(certFile, keyFile string, names []string) (*x509.Certificate, error) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake LE"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0)}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(0, 0, 90), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

func acmeTweak(c *config.Config) {
	c.TLS.SelfSigned, c.TLS.ACME, c.TLS.ACMEDomains = false, true, []string{"irc.example.org"}
}

// capLine sends CAP LS 302 on c and returns the (last) LS line.
func capLS302(t *testing.T, c *tc) string {
	t.Helper()
	c.send("CAP LS 302")
	return c.expect(" CAP * LS ")
}

func waitFor(t *testing.T, s *Server, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		var ok bool
		s.onLoop(func() { ok = f() })
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestACMERenewAndSTS(t *testing.T) {
	calls := 0
	s := testServerWith(t, acmeTweak, func(s *Server) {
		s.acme.obtain = func(ctx context.Context, cfg acmecert.Config) (*x509.Certificate, error) {
			calls++
			if cfg.Domains[0] != "irc.example.org" || !strings.HasSuffix(cfg.AccountKeyFile, "acme-account.key") {
				t.Errorf("config %+v", cfg)
			}
			return writeCASigned(cfg.CertFile, cfg.KeyFile, cfg.Domains)
		}
	})
	waitFor(t, s, "the Let's Encrypt certificate", s.realCert)

	plain := raw(t, s)
	_, port, _ := strings.Cut(s.Addrs()[1].String(), "127.0.0.1:")
	if got := capLS302(t, plain); !strings.Contains(got, " sts=port="+port+",duration=2592000") {
		t.Errorf("plain CAP LS: %s", got)
	}
	plain.send("CAP REQ :sts")
	plain.expect(" CAP * NAK :sts")

	conn := tlsLoginAny(t, s)
	sc := &tc{t: t, conn: conn, r: bufio.NewReader(conn)}
	if got := capLS302(t, sc); !strings.Contains(got, " sts=duration=2592000") || strings.Contains(got, "port=") {
		t.Errorf("TLS CAP LS: %s", got)
	}
	if got := conn.ConnectionState().PeerCertificates[0].DNSNames[0]; got != "irc.example.org" {
		t.Errorf("TLS port still serves %s", got)
	}
	old := raw(t, s)
	old.send("CAP LS")
	if got := old.expect(" CAP * LS "); strings.Contains(got, "sts") {
		t.Errorf("sts offered without CAP 302: %s", got)
	}
	var st string
	s.onLoop(func() {
		c := s.statusInfo().Cert
		st = c.Source
		if c.RenewAt.IsZero() || c.RenewFailures != 0 {
			t.Errorf("status cert %+v", c)
		}
	})
	if st != "Let's Encrypt" || calls != 1 {
		t.Errorf("source %q, %d calls", st, calls)
	}
}

func TestACMEFailureKeepsStandIn(t *testing.T) {
	s := testServerWith(t, acmeTweak, func(s *Server) {
		s.acme.obtain = func(context.Context, acmecert.Config) (*x509.Certificate, error) {
			return nil, errors.New("irc.example.org: connection refused on port 80")
		}
	})
	waitFor(t, s, "the failure", func() bool { return s.acme.fails == 1 })
	var c = s.statusInfo().Cert
	s.onLoop(func() { c = s.statusInfo().Cert })
	if !strings.Contains(c.Source, "stand-in") || !strings.Contains(c.RenewError, "port 80") || s.realCert() {
		t.Errorf("status cert %+v", c)
	}
	p := raw(t, s)
	if got := capLS302(t, p); strings.Contains(got, "sts") {
		t.Errorf("sts offered with a self-signed stand-in: %s", got)
	}
	// TLS still works on the stand-in.
	tlsLoginAny(t, s)
}

func TestSTSOwnFilesAndSelfSigned(t *testing.T) {
	// The test servers are self-signed: no sts.
	s := testServer(t, nil)
	if got := capLS302(t, raw(t, s)); strings.Contains(got, "sts") {
		t.Errorf("self-signed: %s", got)
	}
	stopServer(s)

	// Own CA-signed files and sts_port for a tunnel: offered.
	dir := t.TempDir()
	s = testServer(t, func(c *config.Config) {
		c.TLS.SelfSigned, c.TLS.STSPort = false, 6697
		c.TLS.Cert, c.TLS.Key = dir+"/c.pem", dir+"/k.pem"
		if _, err := writeCASigned(c.TLS.Cert, c.TLS.Key, []string{"irc.test.local"}); err != nil {
			t.Fatal(err)
		}
	})
	if got := capLS302(t, raw(t, s)); !strings.Contains(got, " sts=port=6697,duration=2592000") {
		t.Errorf("own files: %s", got)
	}
	stopServer(s)

	// sts = false turns it off.
	off := false
	dir2 := t.TempDir()
	s = testServer(t, func(c *config.Config) {
		c.TLS.SelfSigned, c.TLS.STS = false, &off
		c.TLS.Cert, c.TLS.Key = dir2+"/c.pem", dir2+"/k.pem"
		writeCASigned(c.TLS.Cert, c.TLS.Key, []string{"irc.test.local"})
	})
	if got := capLS302(t, raw(t, s)); strings.Contains(got, "sts") {
		t.Errorf("sts = false: %s", got)
	}
	_ = tls.VersionTLS12
}
