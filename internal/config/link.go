package config

import "fmt"

// Link is the [link] section: this server's place in a network. Server
// linking arrives in Stage 2; until then the values are stored and checked
// so an installed server already says what it will be.
type Link struct {
	// Role is "standalone" (the default), "hub" (other servers link to
	// it) or "leaf" (it links to a hub).
	Role string `toml:"role"`
	// Port is the TLS port for server links (7000 by default for a hub or
	// a leaf; unused when standalone).
	Port int `toml:"port"`
}

// LinkRoles are the roles a server may have.
var LinkRoles = []string{"standalone", "hub", "leaf"}

// Linked reports whether the server is meant to link (hub or leaf).
func (l Link) Linked() bool { return l.Role == "hub" || l.Role == "leaf" }

func (c *Config) setLinkDefaults() {
	if c.Link.Role == "" {
		c.Link.Role = "standalone"
	}
	if c.Link.Linked() && c.Link.Port == 0 {
		c.Link.Port = 7000
	}
}

func (c *Config) checkLink() error {
	ok := false
	for _, r := range LinkRoles {
		ok = ok || c.Link.Role == r
	}
	if !ok {
		return fmt.Errorf("[link] role %q must be standalone, hub or leaf", c.Link.Role)
	}
	if c.Link.Port < 0 || c.Link.Port > 65535 {
		return fmt.Errorf("[link] port must be a port number")
	}
	if c.Link.Linked() {
		for _, l := range c.Listen {
			if p, err := portOf(l.Address); err == nil && p == c.Link.Port {
				return fmt.Errorf("[link] port %d is already a [[listen]] port for clients", p)
			}
		}
	}
	return nil
}
