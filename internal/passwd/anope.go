package passwd

// Passwords imported from Anope 2.0 keep Anope's own hash, marked with
// AnopePrefix ("anope:sha256:<hash>:<iv>", "anope:bcrypt:$2a$...", ...).
// Check accepts them; services swap them for an argon2id hash at the first
// right password, so nobody has to reset anything after a move.

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// AnopePrefix marks a password hash taken over from Anope.
const AnopePrefix = "anope:"

// IsAnope reports whether encoded is a hash taken over from Anope, which
// should be replaced by Hash at the next right password.
func IsAnope(encoded string) bool { return strings.HasPrefix(encoded, AnopePrefix) }

// AnopeSupported reports whether Check can verify an Anope password field
// as Anope wrote it (without AnopePrefix). Anope's enc_old (a broken MD5)
// is the one kind it can't.
func AnopeSupported(field string) bool {
	method, _, ok := strings.Cut(field, ":")
	if !ok {
		return false
	}
	switch method {
	case "sha256", "bcrypt", "md5", "sha1", "plain":
		return true
	}
	return false
}

func checkAnope(password, encoded string) bool {
	method, rest, ok := strings.Cut(strings.TrimPrefix(encoded, AnopePrefix), ":")
	if !ok {
		return false
	}
	switch method {
	case "sha256": // <hash hex>:<iv hex>, SHA-256 started from the IV
		sum, ivHex, ok := strings.Cut(rest, ":")
		if !ok {
			return false
		}
		iv, err := hex.DecodeString(ivHex)
		if err != nil || len(iv) != 32 {
			return false
		}
		var h [8]uint32
		for i := range h {
			h[i] = binary.BigEndian.Uint32(iv[i*4:])
		}
		got := sha256From(h, []byte(password))
		return equalHex(sum, got[:])
	case "bcrypt":
		return bcrypt.CompareHashAndPassword([]byte(rest), []byte(password)) == nil
	case "md5":
		got := md5.Sum([]byte(password))
		return equalHex(rest, got[:])
	case "sha1":
		got := sha1.Sum([]byte(password))
		return equalHex(rest, got[:])
	case "plain":
		want, err := base64.StdEncoding.DecodeString(rest)
		return err == nil && subtle.ConstantTimeCompare(want, []byte(password)) == 1
	}
	return false
}

func equalHex(wantHex string, got []byte) bool {
	want, err := hex.DecodeString(strings.ToLower(wantHex))
	return err == nil && subtle.ConstantTimeCompare(want, got) == 1
}

// sha256From is SHA-256 with its start state replaced by h, the way
// Anope's enc_sha256 salts passwords (Go's crypto/sha256 can't change it).
func sha256From(h [8]uint32, msg []byte) [32]byte {
	n := len(msg)
	pad := 64 - (n+9)%64
	if pad == 64 {
		pad = 0
	}
	buf := make([]byte, n+1+pad+8)
	copy(buf, msg)
	buf[n] = 0x80
	binary.BigEndian.PutUint64(buf[len(buf)-8:], uint64(n)*8)
	var w [64]uint32
	for off := 0; off < len(buf); off += 64 {
		for i := 0; i < 16; i++ {
			w[i] = binary.BigEndian.Uint32(buf[off+i*4:])
		}
		for i := 16; i < 64; i++ {
			s0 := bits.RotateLeft32(w[i-15], -7) ^ bits.RotateLeft32(w[i-15], -18) ^ (w[i-15] >> 3)
			s1 := bits.RotateLeft32(w[i-2], -17) ^ bits.RotateLeft32(w[i-2], -19) ^ (w[i-2] >> 10)
			w[i] = w[i-16] + s0 + w[i-7] + s1
		}
		a, b, c, d, e, f, g, hh := h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7]
		for i := 0; i < 64; i++ {
			t1 := hh + (bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)) + ((e & f) ^ (^e & g)) + sha256K[i] + w[i]
			t2 := (bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)) + ((a & b) ^ (a & c) ^ (b & c))
			hh, g, f, e, d, c, b, a = g, f, e, d+t1, c, b, a, t1+t2
		}
		h[0] += a
		h[1] += b
		h[2] += c
		h[3] += d
		h[4] += e
		h[5] += f
		h[6] += g
		h[7] += hh
	}
	var out [32]byte
	for i, v := range h {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return out
}

var sha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}
