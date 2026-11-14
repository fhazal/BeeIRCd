// Package link is server-to-server linking: the link
// certificate, fingerprints and the wire protocols.
//
// The network logic in package server never depends on one protocol's
// command names: each protocol is a Handshake (and, from 2b on, a codec)
// behind the interfaces here. UnrealIRCd 6 is the first; an InspIRCd
// module can be added the same way later.
package link

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Fingerprint is the SHA-256 of a certificate's public key (its
// SubjectPublicKeyInfo), base64: the same as UnrealIRCd's spkifp. It stays
// the same when a certificate is renewed with the same key.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// LoadOrCreate loads the link certificate, making a self-signed one (ECDSA
// P-256, valid 20 years) when neither file exists. It returns the
// certificate and its fingerprint.
func LoadOrCreate(certFile, keyFile, name string) (tls.Certificate, string, error) {
	_, cErr := os.Stat(certFile)
	_, kErr := os.Stat(keyFile)
	if errors.Is(cErr, os.ErrNotExist) && errors.Is(kErr, os.ErrNotExist) {
		if err := create(certFile, keyFile, name); err != nil {
			return tls.Certificate{}, "", fmt.Errorf("making the link certificate: %w", err)
		}
	}
	return Load(certFile, keyFile)
}

// Load reads the link certificate and returns it with its fingerprint.
func Load(certFile, keyFile string) (tls.Certificate, string, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("link certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("link certificate: %w", err)
	}
	pair.Leaf = leaf
	return pair, Fingerprint(leaf), nil
}

// FileFingerprint reads only the certificate file (no key needed), for
// `beeircd link-info`.
func FileFingerprint(certFile string) (string, error) {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return "", fmt.Errorf("%s: no PEM certificate", certFile)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return "", err
	}
	return Fingerprint(c), nil
}

func create(certFile, keyFile, name string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name, Organization: []string{"BeeIRCd server link"}},
		DNSNames:     []string{name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o750); err != nil {
		return err
	}
	if err := writeNew(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o750); err != nil {
		return err
	}
	return writeNew(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// writeNew writes a file that must not exist yet.
func writeNew(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
