package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// banUnits are the letters a ban duration may use, as on InspIRCd and
// UnrealIRCd: 1y2w3d4h5m6s.
var banUnits = map[byte]time.Duration{
	'y': 365 * 24 * time.Hour,
	'w': 7 * 24 * time.Hour,
	'd': 24 * time.Hour,
	'h': time.Hour,
	'm': time.Minute,
	's': time.Second,
}

// maxBanDuration keeps a typo from overflowing.
const maxBanDuration = 100 * 365 * 24 * time.Hour

// ParseBanDuration reads how long a ban (KLINE, GLINE, AKILL, SHUN,
// ZLINE, ...) lasts: plain seconds ("90"), or units together like
// "1y2w3d4h5m6s", "2m30s" or "5s". "0" is 0, meaning until removed.
func ParseBanDuration(v string) (time.Duration, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	bad := fmt.Errorf("%q is not a time like 30m, 2h30m, 7d or 90 (seconds)", v)
	if v == "" {
		return 0, bad
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 || time.Duration(n) > maxBanDuration/time.Second {
			return 0, bad
		}
		return time.Duration(n) * time.Second, nil
	}
	var total time.Duration
	for i := 0; i < len(v); {
		j := i
		for j < len(v) && v[j] >= '0' && v[j] <= '9' {
			j++
		}
		if j == i || j == len(v) {
			return parseGoDuration(v, bad)
		}
		unit, ok := banUnits[v[j]]
		if !ok {
			return parseGoDuration(v, bad)
		}
		n, err := strconv.ParseInt(v[i:j], 10, 64)
		if err != nil || time.Duration(n) > maxBanDuration/unit {
			return 0, bad
		}
		total += time.Duration(n) * unit
		if total > maxBanDuration {
			return 0, bad
		}
		i = j + 1
	}
	return total, nil
}

// parseGoDuration takes what Go's own form allows too ("1.5h", "500ms").
func parseGoDuration(v string, bad error) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || d > maxBanDuration {
		return 0, bad
	}
	return d, nil
}

// JoinWords joins duration parts the InspIRCd 4 way: the last two with
// " and ", the ones before with ", " ("1 hour, 2 minutes and 3 seconds").
func JoinWords(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// DurationWords writes a duration with every unit it has, like
// "2 minutes and 30 seconds" or "30 days and 12 hours" (whole seconds).
func DurationWords(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs <= 0 {
		return "0 seconds"
	}
	units := []struct {
		n    int64
		name string
	}{{24 * 3600, "day"}, {3600, "hour"}, {60, "minute"}, {1, "second"}}
	var parts []string
	for _, u := range units {
		if n := secs / u.n; n > 0 {
			w := strconv.FormatInt(n, 10) + " " + u.name
			if n != 1 {
				w += "s"
			}
			parts = append(parts, w)
			secs -= n * u.n
		}
	}
	return JoinWords(parts)
}
