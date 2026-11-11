package services

import (
	"sort"
	"strconv"
	"strings"
)

// numList reads entry numbers as Anope takes them: "3", "2-4" or
// "1,3,5-7". It returns the numbers (1-based, each once, in order, only
// those up to max) and whether spec was a number list at all.
func numList(spec string, max int) ([]int, bool) {
	if spec == "" || strings.Trim(spec, "0123456789,-") != "" {
		return nil, false
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, false
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, false
			}
		}
		for n := max0(a); n <= b && n <= max; n++ {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out, true
}

func max0(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
