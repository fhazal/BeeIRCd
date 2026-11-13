package config

// [[webirc]]: trusted web gateways (BeeRoom / The Lounge, KiwiIRC, gamja
// behind a bouncer) that tell us each person's real IP with WEBIRC
// (IRCv3 WebIRC), so bans, session limits, the connect throttle, DNSBL and
// cloaks use the person, not the gateway.

import (
	"fmt"
	"net/netip"
	"strings"
)

// WebIRC is one trusted gateway.
type WebIRC struct {
	// Name shows in WHOIS and notices, like "beeroom".
	Name string `toml:"name"`
	// Password the gateway sends in WEBIRC: a long random secret (16+
	// characters), or a hash from `beeircd mkpasswd` (checked off the loop,
	// slower for every connection).
	Password string `toml:"password"`
	// Hosts are the gateway's IPs or CIDRs; WEBIRC from anywhere else is
	// refused.
	Hosts []string `toml:"hosts"`
}

// webircMinSecret is the shortest plain password accepted.
const webircMinSecret = 16

// Hashed reports whether Password is a mkpasswd hash.
func (w WebIRC) Hashed() bool { return strings.HasPrefix(w.Password, "$argon2id$") }

// Allows reports whether ip is one of the gateway's hosts.
func (w WebIRC) Allows(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, h := range w.Hosts {
		if p, err := netip.ParsePrefix(h); err == nil {
			if p.Contains(a) {
				return true
			}
			continue
		}
		if b, err := netip.ParseAddr(h); err == nil && b.Unmap() == a {
			return true
		}
	}
	return false
}

// WebIRCFor returns the gateway blocks ip may use.
func (c *Config) WebIRCFor(ip string) []WebIRC {
	var out []WebIRC
	for _, w := range c.WebIRC {
		if w.Allows(ip) {
			out = append(out, w)
		}
	}
	return out
}

func (c *Config) checkWebIRC() error {
	seen := make(map[string]bool)
	for i, w := range c.WebIRC {
		name := strings.ToLower(w.Name)
		where := fmt.Sprintf("[[webirc]] %q", w.Name)
		switch {
		case w.Name == "":
			return fmt.Errorf("[[webirc]] block %d needs a name, like \"beeroom\"", i+1)
		case strings.ContainsAny(w.Name, " \t,:*?!@"):
			return fmt.Errorf("%s: the name may not have spaces or , : * ? ! @", where)
		case seen[name]:
			return fmt.Errorf("%s is in the file twice", where)
		case w.Password == "":
			return fmt.Errorf("%s needs a password (the one the gateway sends)", where)
		case !w.Hashed() && len(w.Password) < webircMinSecret:
			return fmt.Errorf("%s: the password must be at least %d characters (a long random secret) or a `beeircd mkpasswd` hash", where, webircMinSecret)
		case strings.ContainsAny(w.Password, " \t"):
			return fmt.Errorf("%s: the password may not have spaces", where)
		case len(w.Hosts) == 0:
			return fmt.Errorf("%s needs hosts: the gateway's IPs, like [\"127.0.0.1\"]", where)
		}
		seen[name] = true
		for _, h := range w.Hosts {
			if p, err := netip.ParsePrefix(h); err == nil {
				if p.Bits() < 8 {
					return fmt.Errorf("%s: host %s covers too much of the internet; list the gateway's own IPs", where, h)
				}
				continue
			}
			if _, err := netip.ParseAddr(h); err != nil {
				return fmt.Errorf("%s: host %q must be an IP or CIDR (like 127.0.0.1 or 10.0.0.0/24), not a name", where, h)
			}
		}
	}
	return nil
}
