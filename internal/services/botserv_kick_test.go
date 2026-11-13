package services

import (
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

func TestBadWordMatch(t *testing.T) {
	w := func(word string, typ int) []store.BadWord { return []store.BadWord{{Word: word, Type: typ}} }
	cases := []struct {
		text  string
		words []store.BadWord
		want  bool
	}{
		{"a waspish day", w("wasp", store.BadWordAny), true},
		{"WASP", w("wasp", store.BadWordAny), true},
		{"a waspish day", w("wasp", store.BadWordSingle), false},
		{"wasp", w("wasp", store.BadWordSingle), true},
		{"wasp here", w("wasp", store.BadWordSingle), true},
		{"a wasp", w("wasp", store.BadWordSingle), true},
		{"a wasp here", w("wasp", store.BadWordSingle), true},
		{"a waspish day", w("wasp", store.BadWordStart), true},
		{"a yellowwasp", w("wasp", store.BadWordStart), false},
		{"a yellowwasp", w("wasp", store.BadWordEnd), true},
		{"a yellowwasp here", w("wasp", store.BadWordEnd), true},
		{"a waspish day", w("wasp", store.BadWordEnd), false},
		{"hi", w("hello", store.BadWordAny), false},
		{"", w("x", store.BadWordAny), false},
	}
	for _, c := range cases {
		if got := bufHasBadWord(normalizeBuffer(c.text), c.words); got != c.want {
			t.Errorf("%q with %+v = %v, want %v", c.text, c.words, got, c.want)
		}
	}
}

func TestNormalizeBuffer(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                   "plain",
		"\x02bo\x02ld":            "bold",
		"\x0304,12red\x03 x":      "red x",
		"\x034red":                "red",
		"\x03,5comma":             ",5comma",
		"\x1d\x1f\x16\x0fall\x1e": "all",
		"w\x02a\x02sp":            "wasp",
	} {
		if got := normalizeBuffer(in); got != want {
			t.Errorf("normalizeBuffer(%q) = %q, want %q", in, got, want)
		}
	}
}
