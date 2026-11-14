package spamfilter

import "testing"

func FuzzCompileAndMatch(f *testing.F) {
	f.Add("*free*nitro*", "free nitro", true)
	f.Add(`^\w+\d{3}$`, "abc123", false)
	f.Fuzz(func(t *testing.T, match, text string, simple bool) {
		typ := Regex
		if simple {
			typ = Simple
		}
		r, err := Compile(Filter{Type: typ, Match: match, Targets: "c", Action: Block})
		if err != nil {
			return
		}
		r.Catches(Clean(text))
	})
}
