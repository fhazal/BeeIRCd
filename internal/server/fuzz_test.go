package server

import (
	"bufio"
	"strings"
	"testing"
)

// Fuzz targets. Longer runs:
//   go test -run '^$' -fuzz=FuzzParseFlood -fuzztime=5m ./internal/server/

func FuzzParseFlood(f *testing.F) {
	for _, s := range []string{"[10j#R10,40m#M10,7c#C15,5n#N15,10k#K15]:15", "[3t]:30", "[5r#b2]:10", "[1j]:1", "[]:5", "[10j", "10j]:15", "[999m#M60]:999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, arg string) {
		fs, why := parseFlood(arg, false)
		if why != "" {
			return
		}
		out := fs.String()
		if strings.ContainsAny(out, " \r\n\x00") {
			t.Fatalf("parseFlood(%q).String() = %q", arg, out)
		}
		// What the server stores must read back to the same thing.
		fs2, why := parseFlood(out, false)
		if why != "" || fs2.String() != out {
			t.Fatalf("stored +f %q does not read back: %q (%s)", out, fs2.String(), why)
		}
	})
}

func FuzzStripColour(f *testing.F) {
	for _, s := range []string{"\x034red", "\x0304,01x", "\x04ff0000hex\x04", "\x03", "\x03,", "\x0312,", "plain"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if out := stripColour(text); hasColour(out) {
			t.Fatalf("stripColour(%q) = %q still has colour", text, out)
		}
	})
}

func FuzzReadLine(f *testing.F) {
	for _, s := range []string{"NICK a\r\nUSER a 0 * :a\r\n", "x\n", strings.Repeat("y", 3000) + "\n", "\r\n\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		r := bufio.NewReaderSize(strings.NewReader(input), readBufSize)
		for i := 0; i < 100; i++ {
			line, err := readLine(r)
			if err != nil {
				return
			}
			if len(line) > maxInput || strings.HasSuffix(line, "\n") {
				t.Fatalf("readLine gave %d bytes ending %q", len(line), line[max(0, len(line)-2):])
			}
		}
	})
}
