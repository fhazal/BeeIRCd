package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// RPC is the [rpc] section: the JSON-RPC admin API for tools and scripts,
// with UnrealIRCd's method names (user.list, server_ban.add, ...). Off
// until listen or socket is set.
type RPC struct {
	// Listen is the HTTPS address, like "127.0.0.1:8600". It uses the
	// server's certificate ([tls]).
	Listen string `toml:"listen"`
	// Socket is a local socket file: whoever may open it has every right,
	// without a password.
	Socket string `toml:"socket"`
	// Users are the [[rpc.user]] blocks: who may call over HTTPS.
	Users []RPCUser `toml:"user"`
}

// RPCUser is one [[rpc.user]] block: a tool and what it may do.
type RPCUser struct {
	Name string `toml:"name"`
	// Password is a hash from `beeircd mkpasswd`.
	Password string `toml:"password"`
	// Allow lists the methods the tool may call: "*" (the default),
	// groups like "user.*", or single methods like "server_ban.add".
	Allow []string `toml:"allow"`
	// IPs it may call from, addresses or ranges; this machine only when
	// empty.
	IPs []string `toml:"ips"`
}

var (
	rpcNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	rpcAllowRe = regexp.MustCompile(`^(\*|[a-z_]+\.(\*|[a-z_]+))$`)
)

// Allows reports whether the user may call method.
func (u RPCUser) Allows(method string) bool {
	if len(u.Allow) == 0 {
		return true
	}
	for _, a := range u.Allow {
		if a == "*" || a == method || strings.HasSuffix(a, ".*") && strings.HasPrefix(method, a[:len(a)-1]) {
			return true
		}
	}
	return false
}

// From reports whether the user may call from ip.
func (u RPCUser) From(ip net.IP) bool {
	if len(u.IPs) == 0 {
		return ip.IsLoopback()
	}
	for _, m := range u.IPs {
		if n, err := ParseCIDR(m); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckRPC checks the [rpc] section.
func (c *Config) CheckRPC() error {
	r := c.RPC
	if r.Listen != "" {
		if _, _, err := net.SplitHostPort(r.Listen); err != nil {
			return fmt.Errorf("[rpc] listen %q: %v", r.Listen, err)
		}
		if len(r.Users) == 0 {
			return fmt.Errorf("[rpc] listen needs at least one [[rpc.user]] block (name and password)")
		}
	}
	seen := map[string]bool{}
	for _, u := range r.Users {
		low := strings.ToLower(u.Name)
		switch {
		case !rpcNameRe.MatchString(u.Name):
			return fmt.Errorf("[[rpc.user]] name %q: use 1 to 32 letters, digits, '_', '.' or '-'", u.Name)
		case seen[low]:
			return fmt.Errorf("[[rpc.user]] %s is there twice", u.Name)
		case !strings.HasPrefix(u.Password, "$argon2id$"):
			return fmt.Errorf("[[rpc.user]] %s: password must be a hash from `beeircd mkpasswd`, not the password itself", u.Name)
		}
		seen[low] = true
		for _, a := range u.Allow {
			if !rpcAllowRe.MatchString(a) {
				return fmt.Errorf("[[rpc.user]] %s: allow %q: use \"*\", a group like \"user.*\" or a method like \"server_ban.add\"", u.Name, a)
			}
		}
		for _, m := range u.IPs {
			if _, err := ParseCIDR(m); err != nil {
				return fmt.Errorf("[[rpc.user]] %s: ips %q is not an IP address or range", u.Name, m)
			}
		}
	}
	return nil
}
