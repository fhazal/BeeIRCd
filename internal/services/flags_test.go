package services

import "testing"

// The Anope importer keeps the letters in this list (anopedb.flagLetters).
func TestFlagLetters(t *testing.T) {
	if got := flagLetters(); got != "ABFGHIKNOQVabcfghiklmoqstuv" {
		t.Errorf("flag letters %q: update anopedb.flagLetters too", got)
	}
}
