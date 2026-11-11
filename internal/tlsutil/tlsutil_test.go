package tlsutil

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelfSigned(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls", "cert.pem"), filepath.Join(dir, "tls", "key.pem")

	if _, err := Load(cert, key, false, "irc.test"); err == nil {
		t.Fatal("Load without files and without self_signed should fail")
	}
	r1, err := NewReloader(cert, key, true, "irc.test")
	if err != nil {
		t.Fatal(err)
	}
	leaf := r1.Leaf()
	if leaf == nil || leaf.DNSNames[0] != "irc.test" {
		t.Fatalf("certificate not for irc.test: %+v", leaf)
	}
	before, _ := os.ReadFile(cert)
	if _, err := Load(cert, key, true, "irc.test"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(cert)
	if string(before) != string(after) {
		t.Error("an existing certificate was replaced")
	}
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := MakeSelfSigned(cert, key, "one.test"); err != nil {
		t.Fatal(err)
	}
	r, err := NewReloader(cert, key, false, "one.test")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := r.ReloadIfChanged(); changed || err != nil {
		t.Fatalf("nothing changed: %v %v", changed, err)
	}
	time.Sleep(20 * time.Millisecond) // a different modification time
	if err := MakeSelfSigned(cert, key, "two.test"); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.ReloadIfChanged(); !changed || err != nil {
		t.Fatalf("new files not loaded: %v %v", changed, err)
	}
	got, _ := r.Config().GetCertificate(nil)
	if r.Leaf().DNSNames[0] != "two.test" || got == nil {
		t.Errorf("still the old certificate: %v", r.Leaf().DNSNames)
	}
	os.WriteFile(cert, []byte("broken"), 0o600)
	if _, err := r.ReloadIfChanged(); err == nil || r.Leaf().DNSNames[0] != "two.test" {
		t.Error("a broken file must keep the old certificate")
	}
	if _, err := Describe(filepath.Join(dir, "none.pem"), key, "two.test", time.Now()); err == nil {
		t.Error("Describe without files")
	}
}

// TLS 1.2 offers forward secret AEAD suites only: a client with nothing
// but CBC is refused, one with AES-GCM gets in.
func TestNoCBC(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	r, err := NewReloader(cert, key, true, "ciphers.test")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", r.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	try := func(suite uint16) error {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true,
			MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{suite}})
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := try(tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA); err == nil {
		t.Error("a CBC suite was accepted")
	}
	if err := try(tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256); err != nil {
		t.Errorf("AES-GCM refused: %v", err)
	}
}
