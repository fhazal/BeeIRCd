package passwd

import (
	"strings"
	"testing"
)

func TestHashAndCheck(t *testing.T) {
	h, err := Hash("honey pot 42")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected format %s", h)
	}
	if !Check("honey pot 42", h) {
		t.Error("right password refused")
	}
	if Check("honey pot 43", h) || Check("", h) {
		t.Error("wrong password accepted")
	}
	h2, _ := Hash("honey pot 42")
	if h == h2 {
		t.Error("two hashes of one password are equal (no salt?)")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=x$AA$AA"} {
		if Check("x", bad) {
			t.Errorf("Check accepted a broken hash %q", bad)
		}
	}
}
