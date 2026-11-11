package link

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkCert(t *testing.T) {
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "sub", "link-cert.pem"), filepath.Join(dir, "sub", "link-key.pem")
	pair, fp, err := LoadOrCreate(cf, kf, "hub.bee.net")
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf == nil || pair.Leaf.Subject.CommonName != "hub.bee.net" || len(fp) != 44 {
		t.Fatalf("cert %v fp %q", pair.Leaf, fp)
	}
	if fi, _ := os.Stat(kf); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("key mode %v", fi.Mode())
	}
	_, fp2, err := LoadOrCreate(cf, kf, "hub.bee.net")
	if err != nil || fp2 != fp {
		t.Fatalf("second load %q %v, want %q", fp2, err, fp)
	}
	if fp3, err := FileFingerprint(cf); err != nil || fp3 != fp {
		t.Fatalf("FileFingerprint %q %v", fp3, err)
	}
	os.Remove(kf) // one file missing: refuse, never make a new identity silently
	if _, _, err := LoadOrCreate(cf, kf, "hub.bee.net"); err == nil {
		t.Fatal("loaded with the key missing")
	}
}
