package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testKey makes a key and trusts it for this test only.
func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := TestKey
	TestKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { TestKey = old })
	return priv
}

func manifestFor(t *testing.T, version string, files map[string][]byte) []byte {
	t.Helper()
	m := Manifest{Version: version, Date: "2026-11-20", Notes: "· a fix", Files: map[string]string{}}
	for n, b := range files {
		m.Files[n] = Sum(b)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestSignAndVerify(t *testing.T) {
	key := testKey(t)
	man := manifestFor(t, "1.0.1", map[string][]byte{"beeircd-linux-amd64": []byte("program")})
	sig := Sign(man, key)
	m, err := Verify(man, sig)
	if err != nil || m.Version != "1.0.1" {
		t.Fatalf("Verify = %v, %v", m, err)
	}
	// One changed byte, another key, garbage: all refused.
	bad := []byte(strings.Replace(string(man), "1.0.1", "9.0.1", 1))
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	for name, c := range map[string][2][]byte{
		"changed manifest": {bad, sig},
		"other key":        {man, Sign(man, other)},
		"garbage":          {man, []byte("not a signature")},
	} {
		if _, err := Verify(c[0], c[1]); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without the test key only the real key counts.
	TestKey = ""
	if _, err := Verify(man, sig); !errors.Is(err, ErrBadSignature) {
		t.Error("a test-key signature passed without the test key")
	}
}

func TestFetchAndDownload(t *testing.T) {
	key := testKey(t)
	prog := []byte("the real program")
	man := manifestFor(t, "1.0.1", map[string][]byte{"beeircd-linux-amd64": prog})
	files := map[string][]byte{"/rel/release.json": man, "/rel/release.json.sig": Sign(man, key), "/rel/beeircd-linux-amd64": prog}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()
	m, err := Fetch(ctx, srv.Client(), srv.URL+"/rel/")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := Download(ctx, srv.Client(), srv.URL+"/rel", m, "beeircd-linux-amd64"); err != nil || string(b) != string(prog) {
		t.Errorf("Download = %q, %v", b, err)
	}
	if _, err := Download(ctx, srv.Client(), srv.URL+"/rel", m, "beeircd-linux-arm64"); err == nil {
		t.Error("missing program accepted")
	}
	files["/rel/beeircd-linux-amd64"] = []byte("swapped by an attacker")
	if _, err := Download(ctx, srv.Client(), srv.URL+"/rel", m, "beeircd-linux-amd64"); err == nil || !strings.Contains(err.Error(), "not the signed one") {
		t.Errorf("swapped program: %v", err)
	}
	if _, err := Fetch(ctx, srv.Client(), srv.URL+"/nothing"); err == nil {
		t.Error("missing release accepted")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.0-rc1", true},
		{"1.0.0-rc2", "1.0.0-rc1", true},
		{"1.0.0-rc1", "1.0.0", false},
		{"1.10.0", "1.9.9", true},
		{"1.0.0", "0.0.0-dev", true},
		{"v1.2.0", "1.1.9", true},
		{"1.5.9", "1.5.9c-7d", true}, // test builds sort before the plain version
		{"1.0.0-rc10", "1.0.0-rc9", true},
		{"1.0.0-rc9", "1.0.0-rc10", false},
		{"1.0.0-rc1", "1.0.0-beta2", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestTestVersion(t *testing.T) {
	if !TestVersion("1.1.0-rc1") || TestVersion("1.1.0") || TestVersion("v1.0.0") {
		t.Error("TestVersion")
	}
}
