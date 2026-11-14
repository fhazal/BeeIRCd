package cloak

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// UnrealIRCd-compatible cloaks: the same three keys give the
// same cloak as UnrealIRCd 6's cloak_sha256 module, so a person has one
// cloak on a network that mixes BeeIRCd and UnrealIRCd servers and bans
// on cloaks work on all of them.
//
//	192.0.2.10            -> 9A2B1C3D.4E5F6A7B.8C9D0E1F.IP
//	2001:db8::1           -> 9A2B1C3D:4E5F6A7B:8C9D0E1F:IP
//	dsl-7.isp.example.com -> Clk-9A2B1C3D.isp.example.com
//	localhost             -> Clk-9A2B1C3D

// UnrealChecksum is what UnrealIRCd shows other servers for its keys
// (NETINFO): "SHA256:" and the first 56 hex digits of sha256(k1:k2:k3).
func UnrealChecksum(keys [3]string) string {
	sum := sha256.Sum256([]byte(keys[0] + ":" + keys[1] + ":" + keys[2]))
	return ("SHA256:" + hex.EncodeToString(sum[:]))[:63]
}

// downsample folds 32 bytes to 32 bits by XOR, a byte per 8. UnrealIRCd
// builds with unsigned chars, so nothing sign-extends (checked against
// UnrealIRCd 6.2.7).
func downsample(h []byte) uint32 {
	var out uint32
	for i := 0; i < 4; i++ {
		var x byte
		for _, b := range h[i*8 : i*8+8] {
			x ^= b
		}
		out = out<<8 | uint32(x)
	}
	return out
}

// part is downsample(sha256(sha256(a:text:b) + c)).
func part(a, text, b, c string) uint32 {
	inner := sha256.Sum256([]byte(a + ":" + text + ":" + b))
	outer := sha256.Sum256(append(inner[:], c...))
	return downsample(outer[:])
}

// UnrealHost is UnrealIRCd's cloak for host: an IP (v4 or v6) or a
// hostname. prefix starts hostname cloaks ("Clk" by default there).
func UnrealHost(keys [3]string, prefix, host string) string {
	k1, k2, k3 := keys[0], keys[1], keys[2]
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil && !strings.Contains(host, ":") {
			a, b, c := v4[0], v4[1], v4[2]
			return fmt.Sprintf("%X.%X.%X.IP",
				part(k2, host, k3, k1),
				part(k3, fmt.Sprintf("%d.%d.%d", a, b, c), k1, k2),
				part(k1, fmt.Sprintf("%d.%d", a, b), k2, k3))
		}
		g := groups(ip)
		full := fmt.Sprintf("%x:%x:%x:%x:%x:%x:%x:%x", g[0], g[1], g[2], g[3], g[4], g[5], g[6], g[7])
		return fmt.Sprintf("%X:%X:%X:IP",
			part(k2, full, k3, k1),
			part(k3, fmt.Sprintf("%x:%x:%x:%x:%x:%x:%x", g[0], g[1], g[2], g[3], g[4], g[5], g[6]), k1, k2),
			part(k1, fmt.Sprintf("%x:%x:%x:%x", g[0], g[1], g[2], g[3]), k2, k3))
	}
	alpha := part(k1, host, k2, k3)
	for i := 0; i+1 < len(host); i++ {
		if host[i] == '.' && isAlpha(host[i+1]) {
			rest := host[i+1:]
			c := fmt.Sprintf("%s-%X.", prefix, alpha)
			if n := len(c) + len(rest); n > hostLen {
				rest = rest[n-hostLen:]
			}
			return c + rest
		}
	}
	return fmt.Sprintf("%s-%X", prefix, alpha)
}

// hostLen is UnrealIRCd's HOSTLEN.
const hostLen = 63

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// groups are the eight 16-bit groups of an IPv6 address (UnrealIRCd
// cloaks the uncompressed form).
func groups(ip net.IP) [8]uint16 {
	var g [8]uint16
	b := ip.To16()
	for i := range g {
		g[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	return g
}
