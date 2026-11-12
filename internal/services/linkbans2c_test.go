package services

import (
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

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
