package tlsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelfSigned(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "tls", "cert.pem"), filepath.Join(dir, "tls", "key.pem")

	if _, err := Load(cert, key, false, "irc.test"); err == nil {
		t.Fatal("Load without files and without self_signed should fail")
	}
	c1, err := Load(cert, key, true, "irc.test")
	if err != nil {
		t.Fatal(err)
	}
	leaf := c1.Certificates[0].Leaf
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
