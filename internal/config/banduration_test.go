package config

import (
	"testing"
	"time"
)

func TestParseBanDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0":            0,
		"5":            5 * time.Second,
		"3600":         time.Hour,
		"5s":           5 * time.Second,
		"2m30s":        150 * time.Second,
		"1h":           time.Hour,
		"7D":           7 * 24 * time.Hour,
		"1y2w3d4h5m6s": 365*24*time.Hour + 14*24*time.Hour + 3*24*time.Hour + 4*time.Hour + 5*time.Minute + 6*time.Second,
		"1.5h":         90 * time.Minute,
	} {
		if got, err := ParseBanDuration(in); err != nil || got != want {
			t.Errorf("ParseBanDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "-5", "5x", "h", "1h-", "999999999999y"} {
		if _, err := ParseBanDuration(in); err == nil {
			t.Errorf("ParseBanDuration(%q) took it", in)
		}
	}
}

func TestDurationWords(t *testing.T) {
	for d, want := range map[time.Duration]string{
		150 * time.Second:  "2 minutes and 30 seconds",
		time.Hour:          "1 hour",
		5 * time.Second:    "5 seconds",
		8 * 24 * time.Hour: "8 days",
		0:                  "0 seconds",
	} {
		if got := DurationWords(d); got != want {
			t.Errorf("DurationWords(%v) = %q, want %q", d, got, want)
		}
	}
}
