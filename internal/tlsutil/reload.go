package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Reloader serves the certificate through GetCertificate, so a renewed
// certificate (Let's Encrypt, certbot, a new file) is used for new
// connections without a restart.
type Reloader struct {
	certFile, keyFile string
	mu                sync.RWMutex
	cert              *tls.Certificate
	leaf              *x509.Certificate
	stamp             string // file sizes and times when loaded
}

// NewReloader loads the files (making a self-signed pair first when asked
// to and either is missing).
func NewReloader(certFile, keyFile string, selfSigned bool, host string) (*Reloader, error) {
	if selfSigned && (!exists(certFile) || !exists(keyFile)) {
		if err := MakeSelfSigned(certFile, keyFile, host); err != nil {
			return nil, err
		}
	}
	r := &Reloader{certFile: certFile, keyFile: keyFile}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// Config is a server TLS config that always uses r's current certificate.
func (r *Reloader) Config() *tls.Config {
	// Clients may show a certificate (NickServ CERT, SASL EXTERNAL); it is
	// only fingerprinted, never checked against a CA.
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			r.mu.RLock()
			defer r.mu.RUnlock()
			return r.cert, nil
		},
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequestClientCert,
	}
}

func (r *Reloader) fileStamp() string {
	var s string
	for _, p := range []string{r.certFile, r.keyFile} {
		if fi, err := os.Stat(p); err == nil {
			s += fmt.Sprintf("%d/%d;", fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return s
}

// Reload reads the files again. On error the old certificate stays.
func (r *Reloader) Reload() error {
	stamp := r.fileStamp()
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("read certificate: %w", err)
	}
	r.mu.Lock()
	r.cert, r.leaf, r.stamp = &cert, leaf, stamp
	r.mu.Unlock()
	return nil
}

// ReloadIfChanged reloads when either file changed on disk since the last
// load; changed reports a successful reload.
func (r *Reloader) ReloadIfChanged() (changed bool, err error) {
	r.mu.RLock()
	same := r.fileStamp() == r.stamp
	r.mu.RUnlock()
	if same {
		return false, nil
	}
	if err := r.Reload(); err != nil {
		return false, err
	}
	return true, nil
}

// Leaf is the certificate in use.
func (r *Reloader) Leaf() *x509.Certificate {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.leaf
}

// ErrOtherName means the certificate is fine but made for other names than
// the server name. Clients often connect by a DNS name that isn't the
// server name (test.beeircd.org for test.beenet.local), so it is a warning.
var ErrOtherName = errors.New("certificate is for other names")

// Describe checks a certificate pair the way `beeircd check` reports it:
// the names, the expiry and whether host is covered (ErrOtherName if not).
func Describe(certFile, keyFile, host string, now time.Time) (*x509.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("certificate and key don't load: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	if now.After(leaf.NotAfter) {
		return leaf, fmt.Errorf("certificate expired on %s", leaf.NotAfter.Format("2 Jan 2006"))
	}
	if err := leaf.VerifyHostname(host); err != nil {
		return leaf, ErrOtherName
	}
	return leaf, nil
}
