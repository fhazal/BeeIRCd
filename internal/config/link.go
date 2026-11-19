package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// Link is the [link] section: this server's place in a network and the
// servers it links with.
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
	// RemoteAdmin is the old on/off for AdminServ from other servers. It
	// is still read so old configs load, but it no longer restricts
	// anything: OutsideAdmins replaced it.
	RemoteAdmin bool `toml:"remote_admin"`
	// OutsideAdmins says whether admins from other servers may run
	// AdminServ here: "network" (the default) lets in admins whose reach
	// is network (given by the network owner), "none" takes no admin
	// commands from other servers. Either way an outside admin can never
	// change this setting, the link blocks or the config admin's entry.
	OutsideAdmins string `toml:"outside_admins"`
	// HideSplit shows non-opers "*.net *.split" as the quit reason of a
	// netsplit instead of the two server names (L8).
	HideSplit bool `toml:"hide_split"`
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
	// Protocol is what the other server speaks when we connect to it:
	// "unrealircd" (BeeIRCd and UnrealIRCd 6, the default) or "inspircd"
	// (InspIRCd 4). Incoming links are recognised by their first line;
	// "inspircd" also makes the password required, as InspIRCd needs one.
	Protocol string `toml:"protocol"`
}

// Insp reports whether the block is for an InspIRCd 4 server.
func (s LinkServer) Insp() bool { return strings.EqualFold(s.Protocol, "inspircd") }

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

// [link] outside_admins values.
const (
	OutsideNetwork = "network"
	OutsideNone    = "none"
)

func (c *Config) setLinkDefaults() {
	if c.Link.Role == "" {
		c.Link.Role = "standalone"
	}
	if c.Link.Linked() && c.Link.Port == 0 {
		c.Link.Port = LinkPort
	}
	if c.Link.OutsideAdmins == "" {
		c.Link.OutsideAdmins = OutsideNetwork
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

// ServicesServer is the pseudo-server the services bots sit on when this
// server runs services and links: name and SID, ok false otherwise. The
// defaults are services.<our domain> and our SID with 9 (or 8) first.
func (c *Config) ServicesServer() (name, sid string, ok bool) {
	if !c.Services.On() || !c.Link.Linked() {
		return "", "", false
	}
	name, sid = c.Services.ServerName, c.Services.ServerSID
	if name == "" {
		labels := strings.Split(c.Server.Name, ".")
		if len(labels) >= 3 {
			labels = labels[1:]
		}
		name = "services." + strings.Join(labels, ".")
	}
	if sid == "" && len(c.Server.SID) == 3 {
		first := "9"
		if c.Server.SID[0] == '9' {
			first = "8"
		}
		sid = first + c.Server.SID[1:]
	}
	return name, sid, true
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
	if c.Link.OutsideAdmins != OutsideNetwork && c.Link.OutsideAdmins != OutsideNone {
		return fmt.Errorf("[link] outside_admins %q must be network or none", c.Link.OutsideAdmins)
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
	if name, sid, ok := c.ServicesServer(); ok {
		switch {
		case !ValidServerName(name) || !strings.Contains(name, "."):
			return fmt.Errorf("[services] server_name %q must be a server name like services.example.net", name)
		case strings.EqualFold(name, c.Server.Name):
			return fmt.Errorf("[services] server_name must differ from [server] name")
		case !sidRe.MatchString(sid):
			return fmt.Errorf("[services] server_sid %q must be a digit and two capital letters or digits", sid)
		case sid == c.Server.SID:
			return fmt.Errorf("[services] server_sid must differ from [server] sid")
		}
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

// Check reports what is wrong with a block for a server named self.
func (s LinkServer) Check(self string) error { return s.check(self) }

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
	case s.Protocol != "" && !strings.EqualFold(s.Protocol, "unrealircd") && !s.Insp():
		return fmt.Errorf("protocol must be \"unrealircd\" or \"inspircd\"")
	case s.Insp() && s.Password == "":
		return fmt.Errorf("an InspIRCd link needs a password (its <link> block wants sendpass and recvpass)")
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
