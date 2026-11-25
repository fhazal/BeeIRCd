package services

import (
	"testing"
	"time"
)

// IGNORE's time: a huge number of seconds (or days) overflowed into a
// wrong, even negative, time; longer than a ban may last is refused now.
func TestIgnoreTime(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want time.Duration
		ok   bool
	}{
		{"0", 0, true},
		{"90", 90 * time.Second, true},
		{"30m", 30 * time.Minute, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"3153600000", 3153600000 * time.Second, true}, // 100 years
		{"3153600001", 0, false},
		{"10000000000", 0, false},
		{"20000000000", 0, false},
		{"36501d", 0, false},
		{"1000000000000d", 0, false},
		{"900000h", 0, false},
		{"-5", 0, false},
		{"soon", 0, false},
	} {
		d, ok := ignoreTime(tc.arg)
		if ok != tc.ok || ok && d != tc.want {
			t.Errorf("ignoreTime(%q) = %v, %v; want %v, %v", tc.arg, d, ok, tc.want, tc.ok)
		}
	}
}
