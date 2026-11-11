package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// History is the [history] section: channel history for +H and IRCv3
// CHATHISTORY, with UnrealIRCd's set::history::channel defaults.
type History struct {
	// Default is the +H every new channel gets ("" = none), like UnrealIRCd's
	// modes-on-join "+H 50:1d". Example: "100:1d".
	Default string `toml:"default"`
	// Playback and PlaybackTime: what someone joining a +H channel is
	// shown when their client can't fetch history itself (15 lines, 1d;
	// playback_lines = 0 turns it off). Read PlaybackLines after defaults.
	Playback      *int     `toml:"playback_lines"`
	PlaybackLines int      `toml:"-"`
	PlaybackTime  Duration `toml:"playback_time"`
	// MaxLinesRegistered / MaxLinesUnregistered cap +H lines for channels
	// with and without ChanServ (+r): 5000 and 200. MaxTime caps the age (31d).
	MaxLinesRegistered   int      `toml:"max_lines_registered"`
	MaxLinesUnregistered int      `toml:"max_lines_unregistered"`
	MaxTime              Duration `toml:"max_time"`
	// MaxMemoryMB is the most memory all history may use (32): the oldest
	// lines go first, so a small VPS stays safe.
	MaxMemoryMB int `toml:"max_memory_mb"`
	// Secret encrypts the history file. Empty = history is kept in memory
	// only and lost on restart. Use a long random string and keep it: a
	// new secret can't read the old file.
	Secret string `toml:"secret"`
	// File is where history is saved (history.db next to the services
	// database).
	File string `toml:"file"`
}

func (h *History) setDefaults(servicesDB string) {
	h.PlaybackLines = 15
	if h.Playback != nil {
		h.PlaybackLines = *h.Playback
	}
	setDur(&h.PlaybackTime, 24*time.Hour)
	setInt(&h.MaxLinesRegistered, 5000)
	setInt(&h.MaxLinesUnregistered, 200)
	setDur(&h.MaxTime, 31*24*time.Hour)
	setInt(&h.MaxMemoryMB, 32)
	if h.File == "" {
		h.File = filepath.Join(filepath.Dir(servicesDB), "history.db")
	}
}

func (h *History) check() error {
	if h.Default != "" {
		if _, _, err := ParseHistoryMode(h.Default); err != nil {
			return fmt.Errorf("[history] default: %w", err)
		}
	}
	if h.Secret != "" && len(h.Secret) < 16 {
		return fmt.Errorf("[history] secret must be at least 16 characters")
	}
	if h.PlaybackLines < 0 || h.PlaybackLines > 1000 {
		return fmt.Errorf("[history] playback_lines must be between 0 and 1000")
	}
	return nil
}

// ParseHistoryMode reads a +H value "lines:time" like UnrealIRCd: time is
// minutes, or a number with m, h or d (50:1d, 100:90, 10:8h).
func ParseHistoryMode(v string) (lines int, age time.Duration, err error) {
	ls, ts, ok := strings.Cut(v, ":")
	if !ok {
		return 0, 0, fmt.Errorf("%q is not lines:time, like 50:1d", v)
	}
	lines, err = strconv.Atoi(ls)
	if err != nil || lines < 1 {
		return 0, 0, fmt.Errorf("%q: the number of lines must be 1 or more", v)
	}
	unit := time.Minute
	switch {
	case strings.HasSuffix(ts, "d"):
		unit, ts = 24*time.Hour, ts[:len(ts)-1]
	case strings.HasSuffix(ts, "h"):
		unit, ts = time.Hour, ts[:len(ts)-1]
	case strings.HasSuffix(ts, "m"):
		ts = ts[:len(ts)-1]
	}
	n, err := strconv.Atoi(ts)
	if err != nil || n < 1 {
		return 0, 0, fmt.Errorf("%q: the time must be 1 or more minutes, or like 8h or 1d", v)
	}
	return lines, time.Duration(n) * unit, nil
}

// FormatHistoryMode writes lines and age the way UnrealIRCd shows +H:
// in days, hours or minutes.
func FormatHistoryMode(lines int, age time.Duration) string {
	switch {
	case age%(24*time.Hour) == 0:
		return strconv.Itoa(lines) + ":" + strconv.Itoa(int(age/(24*time.Hour))) + "d"
	case age%time.Hour == 0:
		return strconv.Itoa(lines) + ":" + strconv.Itoa(int(age/time.Hour)) + "h"
	}
	return strconv.Itoa(lines) + ":" + strconv.Itoa(int(age/time.Minute)) + "m"
}
