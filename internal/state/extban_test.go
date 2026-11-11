package state

import "testing"

func TestMatchBan(t *testing.T) {
	bee := &User{Nick: "Bee", Ident: "~bee", Host: "cloak.example", RealHost: "home.example", IP: "10.0.0.1", Realname: "Bee Real", Account: "Bee"}
	anon := &User{Nick: "x", Ident: "~x", Host: "h", Realname: "spam bot"}
	for _, c := range []struct {
		mask       string
		u          *User
		match, mut bool
	}{
		{"Bee!*@*", bee, true, false},
		{"*!*@home.example", bee, true, false}, // the real host counts for channel bans
		{"~a:bee", bee, true, false},
		{"~a:*", bee, true, false},
		{"~a:*", anon, false, false},
		{"~a:0", anon, true, false},
		{"~a:0", bee, false, false},
		{"~r:*spam*", anon, true, false},
		{"~r:*spam*", bee, false, false},
		{"~m:*!*@h", anon, true, true},
		{"~q:~a:bee", bee, true, true},
		{"~t:30:Bee!*@*", bee, true, false},
		{"~t:30:~m:Bee!*@*", bee, true, true},
		{"~z:anything", bee, false, false},
	} {
		m, mute := MatchBan(c.mask, c.u)
		if m != c.match || mute != c.mut {
			t.Errorf("MatchBan(%q, %s) = %v, %v; want %v, %v", c.mask, c.u.Nick, m, mute, c.match, c.mut)
		}
	}
	ch := &Channel{Lists: map[byte][]ListEntry{'b': {{Mask: "~m:*!*@h"}}}}
	if ch.IsBanned(anon) || !ch.IsMuted(anon) {
		t.Error("a mute must stop talking, not joining")
	}
	ch.Lists['e'] = []ListEntry{{Mask: "~r:spam*"}}
	if ch.IsMuted(anon) {
		t.Error("an exception must lift a mute")
	}
}
