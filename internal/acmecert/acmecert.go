// Package acmecert gets and renews a certificate from Let's Encrypt (or any
// ACME CA) with the HTTP-01 challenge. Port 80 is opened only while a
// certificate is being asked for, and the result is written to the same
// cert and key files a hand-made certificate would use, so the server's
// file reloader picks it up and `beeircd check` reads it like any other.
package acmecert

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// LetsEncrypt is the default directory.
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// Config says what to ask for and where to keep it.
type Config struct {
	Directory      string   // ACME directory URL; "" is Let's Encrypt
	Email          string   // contact for expiry mails; may be ""
	Domains        []string // names on the certificate, first one is the subject
	CertFile       string
	KeyFile        string
	AccountKeyFile string       // made on first use
	HTTPAddr       string       // challenge port, ":80" when ""
	HTTPClient     *http.Client // nil for the default (tests use their own CA)
	UserAgent      string
}

// Obtain asks for a new certificate and writes it to CertFile and KeyFile.
// The files are only replaced once the new pair is complete.
func Obtain(ctx context.Context, cfg Config) (*x509.Certificate, error) {
	if len(cfg.Domains) == 0 {
		return nil, errors.New("no domain to ask a certificate for")
	}
	addr := cfg.HTTPAddr
	if addr == "" {
		addr = ":80"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("can't open port %s for the Let's Encrypt check: %w (another web server there, or the server lacks CAP_NET_BIND_SERVICE)", strings.TrimPrefix(addr, ":"), err)
	}
	ch := &challenges{}
	srv := &http.Server{Handler: ch, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	key, err := accountKey(cfg.AccountKeyFile)
	if err != nil {
		return nil, err
	}
	dir := cfg.Directory
	if dir == "" {
		dir = LetsEncrypt
	}
	cl := &acme.Client{Key: key, DirectoryURL: dir, HTTPClient: cfg.HTTPClient, UserAgent: cfg.UserAgent}
	acct := &acme.Account{}
	if cfg.Email != "" {
		acct.Contact = []string{"mailto:" + cfg.Email}
	}
	if _, err := cl.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("account: %w", err)
	}
	order, err := cl.AuthorizeOrder(ctx, acme.DomainIDs(cfg.Domains...))
	if err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}
	for _, u := range order.AuthzURLs {
		z, err := cl.GetAuthorization(ctx, u)
		if err != nil {
			return nil, err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, c := range z.Challenges {
			if c.Type == "http-01" {
				chal = c
			}
		}
		if chal == nil {
			return nil, fmt.Errorf("%s: the CA offers no http-01 check", z.Identifier.Value)
		}
		resp, err := cl.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return nil, err
		}
		ch.set(cl.HTTP01ChallengePath(chal.Token), resp)
		if _, err := cl.Accept(ctx, chal); err != nil {
			return nil, named(z.Identifier.Value, err)
		}
		if _, err := cl.WaitAuthorization(ctx, z.URI); err != nil {
			return nil, named(z.Identifier.Value, err)
		}
	}
	if order, err = cl.WaitOrder(ctx, order.URI); err != nil {
		return nil, fmt.Errorf("order: %w", err)
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cfg.Domains[0]},
		DNSNames: cfg.Domains,
	}, certKey)
	if err != nil {
		return nil, err
	}
	der, _, err := cl.CreateCertFromOrder(ctx, order, csr, true)
	if err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, err
	}
	for _, d := range cfg.Domains {
		if err := leaf.VerifyHostname(d); err != nil {
			return nil, fmt.Errorf("the CA's certificate doesn't cover %s", d)
		}
	}
	var chain []byte
	for _, b := range der {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return nil, err
	}
	if err := writePair(cfg.CertFile, chain, cfg.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return nil, err
	}
	return leaf, nil
}

// Due says whether leaf should be replaced, and why: none yet, self-signed,
// missing a name, or in the last third of its life (30 days of a 90-day
// Let's Encrypt certificate; shorter ones scale with it).
func Due(leaf *x509.Certificate, domains []string, now time.Time) (bool, string) {
	if leaf == nil {
		return true, "no certificate yet"
	}
	if SelfSigned(leaf) {
		return true, "the current one is self-signed"
	}
	for _, d := range domains {
		if leaf.VerifyHostname(d) != nil {
			return true, "the current one doesn't cover " + d
		}
	}
	if now.After(RenewAt(leaf)) {
		return true, "the current one runs out on " + leaf.NotAfter.Format("2 Jan 2006")
	}
	return false, ""
}

// SelfSigned says whether leaf signed itself (CheckSignatureFrom won't
// tell: it wants the parent to be a CA, which our self-signed ones aren't).
func SelfSigned(leaf *x509.Certificate) bool {
	return bytes.Equal(leaf.RawIssuer, leaf.RawSubject) &&
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

// RenewAt is when Due starts saying yes for a CA certificate.
func RenewAt(leaf *x509.Certificate) time.Time {
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	return leaf.NotAfter.Add(-life / 3)
}

// ValidDomain says why name can't be on a public certificate ("" if it can).
func ValidDomain(name string) string {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	switch {
	case net.ParseIP(n) != nil:
		return "is an IP address"
	case !strings.Contains(n, "."):
		return "isn't a full DNS name"
	case strings.HasPrefix(n, "*."):
		return "is a wildcard (needs a DNS check, not supported)"
	}
	for _, s := range []string{".local", ".lan", ".internal", ".localhost", ".home", ".test", ".invalid", ".example"} {
		if strings.HasSuffix(n, s) {
			return "is a private name (" + s + ")"
		}
	}
	return ""
}

type challenges struct{ m sync.Map }

func (c *challenges) set(path, resp string) { c.m.Store(path, resp) }

func (c *challenges) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v, ok := c.m.Load(r.URL.Path)
	if r.Method != http.MethodGet || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(v.(string)))
}

func accountKey(file string) (*ecdsa.PrivateKey, error) {
	if b, err := os.ReadFile(file); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("%s isn't a PEM key", file)
		}
		return x509.ParseECPrivateKey(blk.Bytes)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := writeFile(file, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// writePair writes both files to temporary names first, then renames them
// into place, so a crash never leaves half a certificate.
func writePair(certFile string, cert []byte, keyFile string, key []byte) error {
	if err := writeFile(keyFile+".new", key, 0o600); err != nil {
		return err
	}
	if err := writeFile(certFile+".new", cert, 0o644); err != nil {
		os.Remove(keyFile + ".new")
		return err
	}
	if err := os.Rename(keyFile+".new", keyFile); err != nil {
		return err
	}
	return os.Rename(certFile+".new", certFile)
}

func writeFile(name string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return err
	}
	return os.WriteFile(name, b, mode)
}

// named puts the domain in front of err unless the CA's text already has it.
func named(domain string, err error) error {
	if strings.Contains(err.Error(), domain) {
		return err
	}
	return fmt.Errorf("%s: %w", domain, err)
}
