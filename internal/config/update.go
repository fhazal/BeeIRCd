package config

import (
	"fmt"
	"strings"
)

// Update is the [update] section: where `beeircd update` looks.
type Update struct {
	// URL is the folder holding release.json (GitHub's latest release
	// when empty, once the repository is public).
	URL string `toml:"url"`
	// RCURL is where `beeircd update --rc` looks for test versions; with
	// GitHub it is found by itself.
	RCURL string `toml:"rc_url"`
	// Check: the server looks once a day for a newer release and shows it
	// in `beeircd status` and to opers. On by default; nothing about the
	// server is sent.
	Check *bool `toml:"check"`
}

// CheckOn reports whether the daily update check runs.
func (u Update) CheckOn() bool { return u.Check == nil || *u.Check }

func (c *Config) checkUpdate() error {
	for _, u := range []struct{ key, v string }{{"url", c.Update.URL}, {"rc_url", c.Update.RCURL}} {
		if u.v != "" && !strings.HasPrefix(u.v, "https://") && !strings.HasPrefix(u.v, "http://") {
			return fmt.Errorf("[update] %s must be a web address (https://...)", u.key)
		}
	}
	return nil
}
