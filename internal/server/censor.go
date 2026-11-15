package server

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/irc"
)

// censor replaces every word of text that matches one of words (whole
// words, any case, * for any letters) with replace. Words are runs of
// letters and digits, so "darn's" is "darn" and "'s"; everything between
// them, colour codes included, stays as it is.
func censor(text string, words []string, replace string) string {
	if len(words) == 0 || !utf8.ValidString(text) {
		return text // never rewrite bytes that aren't UTF-8
	}
	var b strings.Builder
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		w := text[start:end]
		if censored(w, words) {
			b.WriteString(replace)
		} else {
			b.WriteString(w)
		}
		start = -1
	}
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
		b.WriteRune(r)
	}
	flush(len(text))
	return b.String()
}

func censored(word string, words []string) bool {
	low := strings.ToLower(word)
	for _, w := range words {
		if irc.Match(strings.ToLower(w), low) {
			return true
		}
	}
	return false
}
