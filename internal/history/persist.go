package history

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// The history file is one AES-256-GCM sealed, gzipped gob of every
// channel's history. The key comes from [history] secret, like UnrealIRCd's
// db-secret: without the secret the file is unreadable.

const fileMagic = "BEEHIST1"

type savedLog struct {
	Name  string
	Lines int
	Age   time.Duration
	Log   []Line
}

// Snapshot is a copy of the history that can be saved off the loop.
type Snapshot []savedLog

// Snapshot copies the store for Save.
func (s *Store) Snapshot() Snapshot {
	out := make(Snapshot, 0, len(s.logs))
	for _, l := range s.logs {
		if len(l.lines) == 0 {
			continue
		}
		out = append(out, savedLog{Name: l.name, Lines: l.limit.Lines, Age: l.limit.Age, Log: append([]Line(nil), l.lines...)})
	}
	return out
}

func key(secret string) []byte {
	k := sha256.Sum256([]byte("BeeIRCd channel history v1\x00" + secret))
	return k[:]
}

// Save writes snap to path, encrypted with secret. The old file is only
// replaced once the new one is fully written.
func Save(path, secret string, snap Snapshot) error {
	var plain bytes.Buffer
	zw := gzip.NewWriter(&plain)
	if err := gob.NewEncoder(zw).Encode(snap); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	block, err := aes.NewCipher(key(secret))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := append([]byte(fileMagic), nonce...)
	out = gcm.Seal(out, nonce, plain.Bytes(), []byte(fileMagic))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrWrongSecret means the file was written with another secret (or is
// damaged).
var ErrWrongSecret = errors.New("the history file can't be opened with this secret")

// Load reads path into s; a missing file is not an error. Lines older than
// their channel's age limit are dropped on the way in.
func (s *Store) Load(path, secret string, now time.Time) (channels, lines int, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	block, err := aes.NewCipher(key(secret))
	if err != nil {
		return 0, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, 0, err
	}
	head := len(fileMagic) + gcm.NonceSize()
	if len(data) < head || string(data[:len(fileMagic)]) != fileMagic {
		return 0, 0, fmt.Errorf("%s is not a BeeIRCd history file", path)
	}
	plain, err := gcm.Open(nil, data[len(fileMagic):head], data[head:], []byte(fileMagic))
	if err != nil {
		return 0, 0, ErrWrongSecret
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return 0, 0, err
	}
	var snap Snapshot
	if err := gob.NewDecoder(io.LimitReader(zr, 1<<30)).Decode(&snap); err != nil {
		return 0, 0, err
	}
	for _, sl := range snap {
		lim := Limit{Lines: sl.Lines, Age: sl.Age}
		for _, line := range sl.Log {
			s.Add(sl.Name, lim, line)
		}
	}
	s.Expire(now)
	for _, l := range s.logs {
		channels++
		lines += len(l.lines)
	}
	return channels, lines, nil
}
