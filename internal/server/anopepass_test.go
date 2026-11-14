package server

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/passwd"
)

// An account brought over from Anope keeps Anope's hash; the first right
// password (IDENTIFY or SASL) swaps it for BeeIRCd's own, a wrong one
// doesn't.
func TestAnopePasswordUpgrade(t *testing.T) {
	sum := md5.Sum([]byte("old-anope-pw"))
	anope := passwd.AnopePrefix + "md5:" + hex.EncodeToString(sum[:])
	s := testServerWith(t, nil, func(s *Server) {
		for _, n := range []string{"moved", "saslmoved"} {
			if _, err := s.store.CreateAccount(n, anope, "", time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	})
	hashOf := func(nick string) string {
		var h string
		done := make(chan struct{})
		s.post(func() {
			a, _ := s.store.AccountByNick(nick)
			h = a.PassHash
			close(done)
		})
		<-done
		return h
	}
	c := login(t, s, "moved")
	c.send("NS IDENTIFY wrong")
	c.expect("Wrong password")
	if h := hashOf("moved"); h != anope {
		t.Fatalf("a wrong password changed the hash: %s", h)
	}
	c.send("NS IDENTIFY old-anope-pw")
	c.expect("Password accepted")
	if h := hashOf("moved"); !strings.HasPrefix(h, "$argon2id$") || !passwd.Check("old-anope-pw", h) {
		t.Errorf("IDENTIFY didn't upgrade the hash: %s", h)
	}
	if got := saslWith(t, s, "x1", "saslmoved", "old-anope-pw"); got != "903" {
		t.Fatalf("SASL with the Anope password: %s", got)
	}
	if h := hashOf("saslmoved"); !strings.HasPrefix(h, "$argon2id$") {
		t.Errorf("SASL didn't upgrade the hash: %s", h)
	}
}
