// Package cloak makes the hidden host people see instead of a real IP or
// hostname (user mode +x).
//
// Cloaks keep enough shape for bans to work on ranges:
//
//	192.0.2.10            -> beenet-1a2b3c4d.5e6f7a.ip   (second part = the /24)
//	2001:db8::1           -> beenet-1a2b3c4d.5e6f7a.ip   (second part = the /64)
//	dsl-7.isp.example.com -> beenet-1a2b3c4d.example.com
//	localhost             -> beenet-1a2b3c4d.cloak
//
// The same secret gives the same cloak on every server and every restart.
package cloak

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// Host returns the cloak for a connection. realHost is the hostname, or the
// IP when no hostname was found (isIP true).
func Host(secret, network, realHost, ip string, isIP bool) string {
	prefix := label(network)
	hash := func(s string, n int) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(s))
		return hex.EncodeToString(mac.Sum(nil))[:n]
	}
	if isIP {
		if p := net.ParseIP(ip); p != nil {
			var block string
			if v4 := p.To4(); v4 != nil {
				block = fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
			} else {
				block = p.Mask(net.CIDRMask(64, 128)).String()
			}
			return prefix + "-" + hash(p.String(), 8) + "." + hash(block, 6) + ".ip"
		}
	}
	host := strings.ToLower(realHost)
	labels := strings.Split(host, ".")
	if len(labels) > 2 {
		c := prefix + "-" + hash(host, 8) + "." + strings.Join(labels[len(labels)-2:], ".")
		if len(c) <= 63 {
			return c
		}
	}
	return prefix + "-" + hash(host, 8) + ".cloak"
}

// label turns the network name into a lower-case hostname part.
func label(network string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(network) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() == 16 {
			break
		}
	}
	if b.Len() == 0 {
		return "cloak"
	}
	return b.String()
}
