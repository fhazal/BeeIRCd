package tlsutil

import (
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
