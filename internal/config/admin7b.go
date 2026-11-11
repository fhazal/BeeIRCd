package config

import "path/filepath"

// Control is the [control] section: the socket the `beeircd` admin
// commands use to reach the running server.
type Control struct {
	// Socket defaults to beeircd.sock next to the services database; the
	// installer uses /run/beeircd/control.
	Socket string `toml:"socket"`
}

// Backup is the [backup] section.
type Backup struct {
	// Dir holds the backups (backups/ next to the services database).
	Dir string `toml:"dir"`
	// Keep is how many dated backups stay (7); older ones are removed.
	Keep int `toml:"keep"`
}

func (c *Config) setAdminDefaults() {
	dir := filepath.Dir(c.Services.Database)
	if c.Control.Socket == "" {
		c.Control.Socket = filepath.Join(dir, "beeircd.sock")
	}
	if c.Backup.Dir == "" {
		c.Backup.Dir = filepath.Join(dir, "backups")
	}
	setInt(&c.Backup.Keep, 7)
}
