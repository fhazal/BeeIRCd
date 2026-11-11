package store

import (
	"testing"
	"time"
)

func TestCertsAjoinAndAccountColumns(t *testing.T) {
	s, path := open(t)
	now := time.Unix(1_800_000_000, 0)
	ok := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.VerifyCache(); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateAccount("Bee", "h", "bee@example.org", now)
	ok(err)
	b, err := s.CreateAccount("Wasp", "h", "wasp@other.example", now)
	ok(err)
	if !a.AutoOp || !a.Private || a.KeepModes || a.Suspended(now) {
		t.Errorf("new account defaults: %+v", a)
	}
	added, err := s.AddCert(a, "AB:CD:EF", now)
	ok(err)
	if !added {
		t.Fatal("AddCert")
	}
	if again, _ := s.AddCert(b, "abcdef", now); again {
		t.Error("one fingerprint on two accounts")
	}
	if got := s.AccountByCert("ABCDEF"); got == nil || got.ID != a.ID {
		t.Errorf("AccountByCert = %+v", got)
	}
	ok(s.AddAjoin(a, "#bchat", ""))
	ok(s.AddAjoin(a, "#ops", "key"))
	gone, err := s.DelAjoin(a, "#BCHAT")
	ok(err)
	if !gone || len(s.Ajoins(a)) != 1 || s.Ajoins(a)[0].Key != "key" {
		t.Errorf("ajoin = %+v", s.Ajoins(a))
	}
	a.AutoOp, a.KeepModes, a.LastModes, a.Private, a.URL, a.NoExpire = false, true, "+iw", false, "https://x", true
	a.SuspendedBy, a.SuspendReason, a.SuspendedAt, a.SuspendUntil = "Boss", "spam", now, now.Add(time.Hour)
	ok(s.SaveAccount(a))
	if got := s.AccountsByEmail("*@example.org"); len(got) != 1 || got[0].Name != "Bee" {
		t.Errorf("AccountsByEmail = %+v", got)
	}
	if got := s.SuspendedAccounts(); len(got) != 1 || !got[0].Suspended(now) || got[0].Suspended(now.Add(2*time.Hour)) {
		t.Errorf("SuspendedAccounts = %+v", got)
	}
	gone, err = s.DelCert(a, "abcdef")
	ok(err)
	if !gone || s.AccountByCert("abcdef") != nil {
		t.Error("DelCert")
	}
	s.AddCert(a, "0011", now)
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.VerifyCache(); err != nil {
		t.Fatal(err)
	}
	got, _ := s2.AccountByNick("bee")
	if got.AutoOp || !got.KeepModes || got.LastModes != "+iw" || got.URL != "https://x" || !got.NoExpire || got.SuspendedBy != "Boss" {
		t.Errorf("after reopen: %+v", got)
	}
	if _, err := s2.DeleteAccount(got.ID); err != nil {
		t.Fatal(err)
	}
	if s2.AccountByCert("0011") != nil || s2.VerifyCache() != nil {
		t.Error("certs left after DeleteAccount")
	}
}
