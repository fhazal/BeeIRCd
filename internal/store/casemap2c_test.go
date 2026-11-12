package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// A database made with rfc1459 keys gets ascii keys on the next start:
// "Nick[a]" stays findable as itself, "nick{a}" becomes a different nick,
// and its vhost moves along with it.
func TestCasemappingMove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.db")
	irc.SetCasemapping(irc.CaseRFC1459)
	defer irc.SetCasemapping(irc.CaseASCII)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a, err := st.CreateAccount("Nick[a]", "x", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetVHost("Nick[a]", "", "vh.example", "admin", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount("Belon", "x", "", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.AccountByNick("nick{A}"); got == nil || got.ID != a.ID {
		t.Fatal("rfc1459: nick{A} should be Nick[a]")
	}
	st.Close()

	irc.SetCasemapping(irc.CaseASCII)
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := st.Notes(); len(n) != 1 || !strings.Contains(n[0], "rfc1459 to ascii: 1 ") {
		t.Errorf("notes %q", n)
	}
	if got, _ := st.AccountByNick("nick[A]"); got == nil || got.ID != a.ID {
		t.Error("ascii: nick[A] should be Nick[a]")
	}
	if got, _ := st.AccountByNick("nick{a}"); got != nil {
		t.Error("ascii: nick{a} is another nick")
	}
	if got, _ := st.AccountByNick("BELON"); got == nil {
		t.Error("Belon lost")
	}
	if v, ok := st.VHostOf("Nick[a]"); !ok || v.Host != "vh.example" {
		t.Errorf("vhost lost: %+v %v", v, ok)
	}
	if err := st.Check(); err != nil {
		t.Error(err)
	}
	st.Close()

	// The next start has nothing to move.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := st.Notes(); len(n) != 0 {
		t.Errorf("second start notes %q", n)
	}
	st.Close()
}

// Going back to rfc1459 when two names would become one is refused.
func TestCasemappingClash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st.CreateAccount("a[b", "x", "", now)
	if _, err := st.CreateAccount("a{b", "x", "", now); err != nil {
		t.Fatal(err)
	}
	st.Close()
	irc.SetCasemapping(irc.CaseRFC1459)
	defer irc.SetCasemapping(irc.CaseASCII)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "same name") {
		t.Fatalf("want a clash error, got %v", err)
	}
}
