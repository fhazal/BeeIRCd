package services

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

// FuzzParseMlock: ChanServ MODE LOCK never panics, and a lock it stored
// reads back the same.
func FuzzParseMlock(f *testing.F) {
	for _, s := range []string{"+nt-s", "+k secret", "+l 50", "+fC [5j]:10", "+L #more", "-k", "+P", "+-+", "+l x", "+f"} {
		f.Add(s)
	}
	check := func(m byte, arg string) (string, string) {
		if strings.ContainsAny(arg, " \r\n\x00") || arg == "" {
			return "", "bad"
		}
		return arg, ""
	}
	f.Fuzz(func(t *testing.T, line string) {
		args := strings.Fields(line)
		reg := &store.Channel{}
		if parseMlock(reg, args, false, check) != "" {
			return
		}
		shown := mlockString(reg)
		if strings.ContainsAny(shown, "\r\n\x00") {
			t.Fatalf("mlockString holds framing bytes: %q", shown)
		}
		again := &store.Channel{}
		if why := parseMlock(again, mlockArgs(reg), false, check); why != "" {
			t.Fatalf("stored lock %q does not read back: %s", shown, why)
		}
		if mlockString(again) != shown {
			t.Fatalf("lock changed on read back: %q -> %q", shown, mlockString(again))
		}
	})
}

// mlockArgs writes reg's lock as MODE LOCK SET arguments.
func mlockArgs(reg *store.Channel) []string {
	modes := "+" + reg.MlockOn
	var args []string
	if reg.MlockKey != "" {
		modes += "k"
		args = append(args, reg.MlockKey)
	}
	if reg.MlockLimit > 0 {
		modes += "l"
		args = append(args, strconv.Itoa(reg.MlockLimit))
	}
	vals := mlockParams(reg)
	var letters []byte
	for m := range vals {
		letters = append(letters, m)
	}
	sort.Slice(letters, func(i, j int) bool { return letters[i] < letters[j] })
	for _, m := range letters {
		modes += string(m)
		args = append(args, vals[m])
	}
	if reg.MlockOff != "" {
		modes += "-" + reg.MlockOff
	}
	return append([]string{modes}, args...)
}
