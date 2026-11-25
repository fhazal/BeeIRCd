package services

import (
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

// A ZLINE is kept one way however it was typed, and matches the IP the
// server checks: by the nick of someone on ::1 it was 0::1 and matched
// nobody; a full-form IPv6 typed by hand matched only that text.
func TestZlineOneForm(t *testing.T) {
	svc, h := newServices(t)
	u := helpUser(t, h, "1LFAAAAAE", "Loopy")
	u.IP = "::1"
	for _, tc := range []struct{ arg, want, ip string }{
		{"Loopy", "0::1", "::1"},
		{"2001:DB8:0:0:0:0:0:1", "2001:db8::1", "2001:db8::1"},
		{"0:0:0:0:0:0:0:1", "0::1", "::1"},
		{"::ffff:192.0.2.7", "192.0.2.7", "192.0.2.7"},
		{"2001:db8:0:0::/32", "2001:db8::/32", "2001:db8::5"},
		{"192.0.2.*", "192.0.2.*", "192.0.2.9"},
	} {
		mask, problem := svc.netbanMask(store.BanZline, tc.arg)
		if problem != "" || mask != tc.want {
			t.Errorf("ZLINE %s kept as %q (%s), want %q", tc.arg, mask, problem, tc.want)
			continue
		}
		b := store.NetBan{Kind: store.BanZline, Mask: mask}
		if !svc.banMatches(&b, "", "", tc.ip, "") {
			t.Errorf("ZLINE %s (%s) doesn't match %s", tc.arg, mask, tc.ip)
		}
	}
	// One kept in another form before still matches the same IP.
	old := store.NetBan{Kind: store.BanZline, Mask: "2001:db8:0:0:0:0:0:1"}
	if !svc.banMatches(&old, "", "", "2001:db8::1", "") || svc.banMatches(&old, "", "", "2001:db8::2", "") {
		t.Error("full-form ZLINE matched wrong")
	}
	// The panel removes one by the form it shows, the old one as typed.
	for _, m := range []string{"0::1", "2001:db8:0:0:0:0:0:1"} {
		if err := svc.store.AddNetBan(store.NetBan{Kind: store.BanZline, Mask: m, Reason: "r", SetBy: "Boss"}); err != nil {
			t.Fatal(err)
		}
	}
	svc.reloadOperData()
	if got, p := svc.BanDelVia("Boss", "panel", store.BanZline, "::1"); p != "" || got != "0::1" {
		t.Errorf("panel DEL ::1 = %q, %q", got, p)
	}
	if got, p := svc.BanDelVia("Boss", "panel", store.BanZline, "2001:db8:0:0:0:0:0:1"); p != "" || got != "2001:db8:0:0:0:0:0:1" {
		t.Errorf("panel DEL of the old full form = %q, %q", got, p)
	}
}
