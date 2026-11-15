package server

import "testing"

func TestMemTotalMB(t *testing.T) {
	in := "MemTotal:        4028520 kB\nMemFree:          512000 kB\n"
	if got := memTotalMB(in); got < 3934 || got > 3935 {
		t.Fatalf("memTotalMB = %v, want about 3934", got)
	}
	for _, bad := range []string{"", "MemFree: 1 kB\n", "MemTotal: x kB\n"} {
		if got := memTotalMB(bad); got != 0 {
			t.Fatalf("memTotalMB(%q) = %v, want 0", bad, got)
		}
	}
}
