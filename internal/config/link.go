package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// Link is the [link] section: this server's place in a network and the
// servers it links with (Stage 2, PLAN-2.md).
type Link struct {
	// Role is "standalone" (the default), "hub" (other servers link to
	// it) or "leaf" (it links to one hub).
	Role string `toml:"role"`
	// Port is the TLS port for server links (7000 by default for a hub or
	// a leaf; unused when standalone).
	Port int `toml:"port"`
	// Address is the IP the link port listens on; "" = all addresses.
	Address string `toml:"address"`
	// Cert and Key are this server's own link certificate. It is made on
	// first start (self-signed, ECDSA P-256) and never renewed, so the
	// fingerprint other servers pin stays the same. Default: next to
	// services.db.
	Cert string `toml:"cert"`
	Key  string `toml:"key"`
	// Servers are the [[link.server]] blocks, one per server we link with.
	Servers []LinkServer `toml:"server"`
}

// LinkServer is one [[link.server]] block, like UnrealIRCd's link { }.
type LinkServer struct {
	// Name is the other server's name, as it sends it.
	Name string `toml:"name"`
	// Host and Port are where we connect to; no host = it connects to us.
	Host string `toml:"host"`
	Port int    `toml:"port"`
	// Fingerprint is the SHA-256 of the other server's public key, base64
	// (`beeircd link-info` on BeeIRCd, `./unrealircd spkifp` on
	// UnrealIRCd). Required: it is what really proves who is linking.
	Fingerprint string `toml:"fingerprint"`
	// Password is sent with PASS and must come back the same. Optional
	// (an UnrealIRCd link using spkifp auth sends "*").
	Password string `toml:"password"`
	// Autoconnect: keep connecting to Host while the link is down.
	Autoconnect bool `toml:"autoconnect"`
	// SendQMB is how much may wait for this link before it is dropped.
	SendQMB int `toml:"sendq_mb"`
}

// LinkRoles are the roles a server may have.
var LinkRoles = []string{"standalone", "hub", "leaf"}

// Linked reports whether the server is meant to link (hub or leaf).
func (l Link) Linked() bool { return l.Role == "hub" || l.Role == "leaf" }

// Server returns the [[link.server]] block for name (case-insensitive).
func (l Link) Server(name string) *LinkServer {
	for i := range l.Servers {
		if strings.EqualFold(l.Servers[i].Name, name) {
			return &l.Servers[i]
		}
	}
	return nil
}

// LinkPort is the default port for server links.
const LinkPort = 7000

func (c *Config) setLinkDefaults() {
	if c.Link.Role == "" {
		c.Link.Role = "standalone"
	}
	if c.Link.Linked() && c.Link.Port == 0 {
		c.Link.Port = LinkPort
	}
	dir := filepath.Dir(c.Services.Database)
	if c.Link.Cert == "" && c.Link.Key == "" {
		c.Link.Cert, c.Link.Key = filepath.Join(dir, "link-cert.pem"), filepath.Join(dir, "link-key.pem")
	}
	for i := range c.Link.Servers {
		s := &c.Link.Servers[i]
		if s.Host != "" && s.Port == 0 {
			s.Port = LinkPort
		}
		if s.SendQMB == 0 {
			s.SendQMB = 20
		}
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
	if c.Link.Address != "" && net.ParseIP(c.Link.Address) == nil {
		return fmt.Errorf("[link] address must be an IP address, like 127.0.0.1")
	}
	if (c.Link.Cert == "") != (c.Link.Key == "") {
		return fmt.Errorf("[link] cert and key go together")
	}
	if len(c.Link.Servers) > 0 && !c.Link.Linked() {
		return fmt.Errorf("[[link.server]] blocks need [link] role hub or leaf")
	}
	seen := map[string]bool{}
	for _, s := range c.Link.Servers {
		if err := s.check(c.Server.Name); err != nil {
			return fmt.Errorf("[[link.server]] %s: %w", s.Name, err)
		}
		if seen[strings.ToLower(s.Name)] {
			return fmt.Errorf("[[link.server]] %s is there twice", s.Name)
		}
		seen[strings.ToLower(s.Name)] = true
	}
	return nil
}

func (s LinkServer) check(self string) error {
	switch {
	case !ValidServerName(s.Name) || !strings.Contains(s.Name, "."):
		return fmt.Errorf("name must be a server name like leaf.example.net")
	case strings.EqualFold(s.Name, self):
		return fmt.Errorf("that is this server's own name")
	case s.Port < 0 || s.Port > 65535:
		return fmt.Errorf("port must be a port number")
	case s.Autoconnect && s.Host == "":
		return fmt.Errorf("autoconnect needs a host to connect to")
	case strings.ContainsAny(s.Host, " /"):
		return fmt.Errorf("host must be a hostname or IP")
	case s.SendQMB < 1 || s.SendQMB > 1024:
		return fmt.Errorf("sendq_mb must be 1-1024")
	}
	if !ValidFingerprint(s.Fingerprint) {
		return fmt.Errorf("fingerprint must be the base64 SHA-256 of its public key (beeircd link-info or ./unrealircd spkifp)")
	}
	for _, r := range s.Password {
		if r < ' ' || r == 0x7f {
			return fmt.Errorf("password has a control character")
		}
	}
	if strings.HasPrefix(s.Password, ":") || strings.Contains(s.Password, " ") {
		return fmt.Errorf("password may not start with : or hold spaces")
	}
	return nil
}

// ValidFingerprint reports whether fp is a base64 SHA-256 (44 characters,
// like UnrealIRCd's spkifp).
func ValidFingerprint(fp string) bool {
	b, err := base64.StdEncoding.DecodeString(fp)
	return err == nil && len(b) == 32
}
