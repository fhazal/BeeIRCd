// Command release makes and signs BeeIRCd releases on the maintainer's
// machine (see RELEASING.md). The signing key never goes to GitHub.
//
//	go run ./tools/release keygen   [-key ~/.beeircd-release/release.key]
//	go run ./tools/release protect  (puts a passphrase on the key)
//	go run ./tools/release manifest -version 1.0.0 -dir dist [-notes notes.txt]
//	go run ./tools/release sign     -in dist/release.json [-key ...]
//	go run ./tools/release verify   -dir dist
package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"

	"github.com/fhazal/beeircd/internal/release"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: release keygen | protect | manifest | sign | verify  (see RELEASING.md)")
		os.Exit(2)
	}
	home, _ := os.UserHomeDir()
	defKey := filepath.Join(home, ".beeircd-release", "release.key")
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	key := fs.String("key", defKey, "signing key file")
	ver := fs.String("version", "", "release version, like 1.0.0")
	dir := fs.String("dir", "dist", "folder with the programs")
	notes := fs.String("notes", "", "file with the release notes (optional)")
	in := fs.String("in", "", "release.json to sign")
	fs.Parse(os.Args[2:])
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(*key)
	case "manifest":
		err = manifest(*ver, *dir, *notes)
	case "protect":
		err = protect(*key)
	case "sign":
		err = sign(*key, *in)
	case "verify":
		err = verify(*dir)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

// keygen makes a new signing key. It never overwrites one: losing the key
// means no more signed updates, overwriting it would be the same.
func keygen(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists; not overwriting a signing key", path)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	pubText := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(path+".pub", []byte(pubText+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("key written to %s (keep it secret, back it up offline)\npublic key: %s\n(add it to TrustedKeys in internal/release/release.go)\n", path, pubText)
	return nil
}

// readKey reads the signing key, asking for its passphrase when it has one.
func readKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(b))
	if strings.HasPrefix(text, lockedPrefix) {
		pass, err := askPass("Passphrase for " + path + ": ")
		if err != nil {
			return nil, err
		}
		seed, err := unlock(text, pass)
		if err != nil {
			return nil, err
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	seed, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s isn't a release key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// protect puts a passphrase on the key file, so a copy in the cloud or on
// a USB stick is useless without it.
func protect(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), lockedPrefix) {
		return fmt.Errorf("%s has a passphrase already", path)
	}
	key, err := readKey(path)
	if err != nil {
		return err
	}
	pass, err := askPass("New passphrase (12+ characters): ")
	if err != nil {
		return err
	}
	if len(pass) < 12 {
		return errors.New("use at least 12 characters")
	}
	again, err := askPass("Again: ")
	if err != nil {
		return err
	}
	if pass != again {
		return errors.New("the two didn't match; nothing changed")
	}
	locked, err := lock(key.Seed(), pass)
	if err != nil {
		return err
	}
	if seed, err := unlock(locked, pass); err != nil || !bytes.Equal(seed, key.Seed()) {
		return errors.New("self-check failed; nothing changed")
	}
	if err := os.WriteFile(path+".new", []byte(locked+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".new", path); err != nil {
		return err
	}
	fmt.Printf("%s now needs the passphrase. Back up this file; it is safe to keep in the cloud.\nForgetting the passphrase is the same as losing the key.\n", path)
	return nil
}

const lockedPrefix = "beeircd-release-key-v1:"

// lock encrypts seed with scrypt + AES-256-GCM.
func lock(seed []byte, pass string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	gcm, err := keyCipher(pass, salt)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, seed, []byte(lockedPrefix))
	enc := base64.StdEncoding.EncodeToString
	return lockedPrefix + enc(salt) + ":" + enc(nonce) + ":" + enc(ct), nil
}

func unlock(text, pass string) ([]byte, error) {
	parts := strings.Split(strings.TrimPrefix(text, lockedPrefix), ":")
	if len(parts) != 3 {
		return nil, errors.New("the key file is damaged")
	}
	var raw [3][]byte
	for i, p := range parts {
		b, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			return nil, errors.New("the key file is damaged")
		}
		raw[i] = b
	}
	gcm, err := keyCipher(pass, raw[0])
	if err != nil {
		return nil, err
	}
	seed, err := gcm.Open(nil, raw[1], raw[2], []byte(lockedPrefix))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("wrong passphrase")
	}
	return seed, nil
}

func keyCipher(pass string, salt []byte) (cipher.AEAD, error) {
	k, err := scrypt.Key([]byte(pass), salt, 1<<17, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// askPass reads a passphrase without showing it (from stdin when it isn't
// a terminal, for tests).
func askPass(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no passphrase given")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

var stdin = bufio.NewReader(os.Stdin)

// manifest writes release.json for the beeircd-* programs in dir.
func manifest(version, dir, notesFile string) error {
	if version == "" {
		return errors.New("-version is needed")
	}
	m := release.Manifest{Version: strings.TrimPrefix(version, "v"), Date: time.Now().UTC().Format("2006-01-02"), Files: map[string]string{}}
	if notesFile != "" {
		b, err := os.ReadFile(notesFile)
		if err != nil {
			return err
		}
		m.Notes = strings.TrimSpace(string(b))
	}
	names, _ := filepath.Glob(filepath.Join(dir, "beeircd-*"))
	sort.Strings(names)
	for _, n := range names {
		if strings.HasSuffix(n, ".sig") || strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			return err
		}
		m.Files[filepath.Base(n)] = release.Sum(b)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("no beeircd-* programs in %s", dir)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	out := filepath.Join(dir, "release.json")
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %s, %d program(s)\n", out, m.Version, len(m.Files))
	return nil
}

// sign writes release.json.sig and checks it against the built-in keys,
// so a key the program doesn't trust is caught before anything ships.
func sign(keyFile, in string) error {
	if in == "" {
		return errors.New("-in release.json is needed")
	}
	key, err := readKey(keyFile)
	if err != nil {
		return err
	}
	m, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	sig := release.Sign(m, key)
	if _, err := release.Verify(m, sig); err != nil {
		return fmt.Errorf("signed, but BeeIRCd wouldn't trust it (is this key's public half in TrustedKeys?): %w", err)
	}
	if err := os.WriteFile(in+".sig", sig, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s.sig written and verified\n", in)
	return nil
}

// verify checks a signed release folder the way `beeircd update` will.
func verify(dir string) error {
	m, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "release.json.sig"))
	if err != nil {
		return err
	}
	man, err := release.Verify(m, sig)
	if err != nil {
		return err
	}
	for name, sum := range man.Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if release.Sum(b) != sum {
			return fmt.Errorf("%s: sum differs from release.json", name)
		}
		fmt.Printf("OK %s\n", name)
	}
	fmt.Printf("release %s: signature and %d program(s) OK\n", man.Version, len(man.Files))
	return nil
}
