package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

// Modules is the [modules] block: Lua modules loaded into the server.
type Modules struct {
	// Load names the modules to load at start, like ["greet"]: each is
	// the file NAME.lua in Dir.
	Load []string `toml:"load"`
	// Dir is the folder the module files are in. Default: "modules" next
	// to beeircd.conf.
	Dir string `toml:"dir"`
	// TimeLimit is how long one call into a module may run before it is
	// stopped. Default 50ms.
	TimeLimit Duration `toml:"time_limit"`
}

// DirOr is the modules folder: Dir, or "modules" next to the config file.
func (m *Modules) DirOr(configPath string) string {
	if m.Dir != "" {
		return m.Dir
	}
	return filepath.Join(filepath.Dir(configPath), "modules")
}

// LimitOr is TimeLimit, 50ms when not set.
func (m *Modules) LimitOr() time.Duration {
	if m.TimeLimit.Duration > 0 {
		return m.TimeLimit.Duration
	}
	return 50 * time.Millisecond
}

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func (c *Config) checkModules() error {
	seen := map[string]bool{}
	for _, name := range c.Modules.Load {
		switch {
		case !moduleName.MatchString(name):
			return fmt.Errorf("[modules] load: %q is not a module name (small letters, digits, - and _; no .lua)", name)
		case seen[name]:
			return fmt.Errorf("[modules] load: %s is there twice", name)
		}
		seen[name] = true
	}
	if d := c.Modules.TimeLimit.Duration; d != 0 && (d < time.Millisecond || d > time.Second) {
		return fmt.Errorf("[modules] time_limit %s: use 1ms to 1s", d)
	}
	for name, block := range c.Module {
		if !moduleName.MatchString(name) {
			return fmt.Errorf("[module.%s]: not a module name", name)
		}
		if a, ok := block["allow"]; ok {
			list, isList := a.([]any)
			if !isList {
				return fmt.Errorf("[module.%s] allow: write a list, like [\"kill\"]", name)
			}
			for _, r := range list {
				if _, isText := r.(string); !isText {
					return fmt.Errorf("[module.%s] allow: write a list of words, like [\"kill\"]", name)
				}
			}
		}
	}
	return nil
}
