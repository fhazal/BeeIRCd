// Package release reads and checks BeeIRCd releases. A release is a folder
// (a GitHub release, or any web folder) holding the programs, release.json
// listing their SHA-256 sums, and release.json.sig: an ed25519 signature
// made with a key that never leaves the maintainer's machine. Only the
// public half is built into BeeIRCd, so a hacked download site or GitHub
// account can't ship a program `beeircd update` would accept.
package release

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// TrustedKeys are the public keys (base64) a release may be signed with.
// A new key is added here one release before the old one is dropped.
var TrustedKeys = []string{
	"pRAtMw63e82bKkYiU3B27vbKRPXwkBO89U0d8oIP0ng=", // 12 Nov 2026, ~/.beeircd-release/release.key on the maintainer PC
}

// TestKey is an extra trusted public key for test builds only (set with
// -ldflags "-X github.com/fhazal/beeircd/internal/release.TestKey=...").
// Release builds never set it.
var TestKey = ""

func trusted() []string {
	if TestKey != "" {
		return append([]string{TestKey}, TrustedKeys...)
	}
	return TrustedKeys
}

// DefaultURL is where releases come from once the repository is public.
const DefaultURL = "https://github.com/fhazal/BeeIRCd/releases/latest/download"

// signContext keeps these signatures from meaning anything elsewhere.
const signContext = "BeeIRCd release v1\n"

// Manifest is release.json.
type Manifest struct {
	Version string            `json:"version"`
	Date    string            `json:"date"`
	Notes   string            `json:"notes,omitempty"`
	Files   map[string]string `json:"files"` // program name -> SHA-256 hex
}

// AssetName is the program for a system, like beeircd-linux-amd64.
func AssetName(goos, goarch string) string { return "beeircd-" + goos + "-" + goarch }

// Sign signs manifest bytes; the result is the .sig file's content.
func Sign(manifest []byte, key ed25519.PrivateKey) []byte {
	sig := ed25519.Sign(key, append([]byte(signContext), manifest...))
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// ErrBadSignature means no trusted key signed the manifest.
var ErrBadSignature = errors.New("the release signature doesn't match BeeIRCd's key: not installing it")

// Verify checks sig against the trusted keys and parses the manifest.
func Verify(manifest, sig []byte) (*Manifest, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, ErrBadSignature
	}
	msg := append([]byte(signContext), manifest...)
	ok := false
	for _, k := range trusted() {
		pub, err := base64.StdEncoding.DecodeString(k)
		if err == nil && len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, msg, raw) {
			ok = true
		}
	}
	if !ok {
		return nil, ErrBadSignature
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("release.json: %w", err)
	}
	if m.Version == "" || len(m.Files) == 0 {
		return nil, errors.New("release.json has no version or no programs")
	}
	return &m, nil
}

// Fetch downloads and checks release.json at base.
func Fetch(ctx context.Context, client *http.Client, base string) (*Manifest, error) {
	base = strings.TrimSuffix(base, "/")
	manifest, err := get(ctx, client, base+"/release.json", 1<<16)
	if err != nil {
		return nil, err
	}
	sig, err := get(ctx, client, base+"/release.json.sig", 1<<10)
	if err != nil {
		return nil, err
	}
	return Verify(manifest, sig)
}

// Download fetches one program and checks its sum against the manifest.
func Download(ctx context.Context, client *http.Client, base string, m *Manifest, name string) ([]byte, error) {
	want, ok := m.Files[name]
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", m.Version, name)
	}
	b, err := get(ctx, client, strings.TrimSuffix(base, "/")+"/"+name, 200<<20)
	if err != nil {
		return nil, err
	}
	if got := Sum(b); got != strings.ToLower(want) {
		return nil, fmt.Errorf("%s is damaged or not the signed one (sum %s, want %s)", name, got[:12], want[:min(12, len(want))])
	}
	return b, nil
}

// Sum is the SHA-256 of b in hex.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", url)
	}
	return b, nil
}

// Newer reports whether version a is newer than b. Versions are
// "1.2.3" with an optional "-rc1"-style suffix that sorts before the plain
// version; anything that isn't a number counts as 0 (so "0.0.0-dev" is
// older than every release).
func Newer(a, b string) bool { return compare(a, b) > 0 }

func compare(a, b string) int {
	an, ap := split(a)
	bn, bp := split(b)
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] > bn[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "": // 1.0.0 is newer than 1.0.0-rc1
		return 1
	case bp == "":
		return -1
	}
	return strings.Compare(ap, bp)
}

func split(v string) ([3]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	core, pre, _ := strings.Cut(v, "-")
	var n [3]int
	for i, p := range strings.SplitN(core, ".", 3) {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		n[i], _ = strconv.Atoi(p[:end])
	}
	return n, pre
}
