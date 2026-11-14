package passwd

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// With SHA-256's own start state, sha256From must be plain SHA-256, at
// every length around the padding edges.
func TestSHA256FromMatchesStdlib(t *testing.T) {
	std := [8]uint32{0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19}
	for _, n := range []int{0, 1, 3, 55, 56, 57, 63, 64, 65, 119, 120, 128, 1000} {
		msg := []byte(strings.Repeat("b", n))
		if got, want := sha256From(std, msg), sha256.Sum256(msg); got != want {
			t.Errorf("length %d: %x, want %x", n, got, want)
		}
	}
}

func TestCheckAnope(t *testing.T) {
	md := md5.Sum([]byte("hunter2"))
	cases := []struct {
		hash, pw string
		ok       bool
	}{
		// Anope enc_bcrypt's own self-test hash.
		{"anope:bcrypt:$2a$10$x9AQFAQScY0v9KF2suqkEOepsHFrG.CXHbIXI.1F28SfSUb56A/7K", "Test!", true},
		{"anope:bcrypt:$2a$10$x9AQFAQScY0v9KF2suqkEOepsHFrG.CXHbIXI.1F28SfSUb56A/7K", "Test?", false},
		{"anope:md5:" + hex.EncodeToString(md[:]), "hunter2", true},
		{"anope:md5:" + hex.EncodeToString(md[:]), "hunter3", false},
		{"anope:sha1:f3bbbd66a63d4bf1747940578ec3d0103530e21d", "hunter2", true},
		{"anope:plain:" + base64.StdEncoding.EncodeToString([]byte("hunter2")), "hunter2", true},
		{"anope:plain:" + base64.StdEncoding.EncodeToString([]byte("hunter2")), "hunter", false},
		{"anope:old:abc", "hunter2", false},
		{"anope:sha256:00:zz", "hunter2", false},
	}
	for _, c := range cases {
		if got := Check(c.pw, c.hash); got != c.ok {
			t.Errorf("Check(%q, %q) = %v", c.pw, c.hash, got)
		}
	}
	if !AnopeSupported("sha256:aa:bb") || AnopeSupported("old:aa") || AnopeSupported("nocolon") {
		t.Error("AnopeSupported")
	}
	if !IsAnope("anope:md5:x") || IsAnope("$argon2id$v=19$") {
		t.Error("IsAnope")
	}
}
