package services

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// An SNLINE /regex/ from another server keeps its case: lowercased, \W
// turned into \w and the ban hit the opposite set of people. Its removal
// still finds it, and one an older version kept lowercased.
func TestRemoteSNLineKeepsCase(t *testing.T) {
	s, _ := newServices(t)
	b := store.NetBan{Kind: store.BanSNLine, Mask: `/^\W+$/`, Reason: "symbols only", SetBy: "op", SetAt: time.Now()}
	if changed, _ := s.RemoteBan(b, true); !changed {
		t.Fatal("remote SNLINE not added")
	}
	if live := s.liveBans(store.BanSNLine); len(live) != 1 || live[0].Mask != b.Mask {
		t.Fatalf("stored %+v, want mask %s", live, b.Mask)
	}
	if got := s.connectBan("bob", "leaf.example", "192.0.2.1", "Bob", false); got != nil {
		t.Errorf("realname Bob refused by %s", got.Mask)
	}
	if got := s.connectBan("x", "leaf.example", "192.0.2.2", "***", false); got == nil {
		t.Errorf("realname *** not refused by %s", b.Mask)
	}
	if changed, _ := s.RemoteBan(b, false); !changed || len(s.liveBans(store.BanSNLine)) != 0 {
		t.Errorf("remote SNLINE removal: changed %v, left %+v", changed, s.liveBans(store.BanSNLine))
	}

	old := store.NetBan{Kind: store.BanSNLine, Mask: "*spam?bot*", Reason: "old", SetBy: "op", SetAt: time.Now()}
	if err := s.store.AddNetBan(old); err != nil {
		t.Fatal(err)
	}
	s.reloadOperData()
	old.Mask = "*Spam?Bot*"
	if changed, _ := s.RemoteBan(old, false); !changed || len(s.liveBans(store.BanSNLine)) != 0 {
		t.Errorf("removal of *Spam?Bot* left the lowercased one: %+v", s.liveBans(store.BanSNLine))
	}
}

func TestMatchesEveryone(t *testing.T) {
	for _, tc := range []struct {
		kind, mask string
		want       bool
	}{
		{store.BanAkill, "*@*", true}, {store.BanAkill, "*@*.*", true}, {store.BanAkill, "~*@*", true},
		{store.BanAkill, "*@0.0.0.0/0", true}, {store.BanAkill, "*@203.0.113.*", false}, {store.BanAkill, "bot@*", false},
		{store.BanZline, "*", true}, {store.BanZline, "*.*.*.*", true}, {store.BanZline, "::/0", true}, {store.BanZline, "203.0.113.0/24", false},
		{store.BanSNLine, "*", true}, {store.BanSNLine, "*spam*", false},
	} {
		if got := matchesEveryone(tc.kind, tc.mask); got != tc.want {
			t.Errorf("%s %s: %v", tc.kind, tc.mask, got)
		}
	}
}
