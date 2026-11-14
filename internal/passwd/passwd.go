// Package passwd hashes and checks passwords with argon2id.
//
// Hashes are stored in the usual encoded form:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// so the cost can be raised later without breaking old hashes.
package passwd

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The cost follows the OWASP minimum for argon2id: 19 MiB, 2 passes.
const (
	memory  = 19 * 1024
	passes  = 2
	threads = 1
	keyLen  = 32
	saltLen = 16
)

var b64 = base64.RawStdEncoding

// Hash returns the encoded argon2id hash of password.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passes, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, passes, threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errFormat = errors.New("not an argon2id hash")

// Check reports whether password matches the encoded hash.
func Check(password, encoded string) bool {
	if IsAnope(encoded) {
		return checkAnope(password, encoded)
	}
	ok, err := check(password, encoded)
	return err == nil && ok
}

func check(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errFormat
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errFormat
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errFormat
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, errFormat
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
