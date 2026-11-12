package cloak

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// Cloaks of the cloak_inspircd module (BajuBesi's UnrealIRCd module,
// InspIRCd's "half" cloak), so BeeIRCd can join a network whose
// UnrealIRCd servers load it: the same three keys give the same cloak.
//
//	192.0.2.10            -> abc.def.0.192.ip
//	2001:db8::1           -> abcdef.ghij.klmn.db8.2001.ip
//	dsl-7.isp.example.com -> abcdef.isp.example.com
//	localhost             -> the IP form (no domain part)

const (
	inspSuffix   = "ip"
	inspParts    = 3  // domain labels kept
	inspMaxCloak = 50 // longer hostname cloaks fall back to the IP form
	inspBase32   = "0123456789abcdefghijklmnopqrstuv"
)

func inspFullKey(keys [3]string) string { return keys[0] + ":" + keys[1] + ":" + keys[2] }

// InspChecksum is what cloak_inspircd shows other servers for its keys
// (NETINFO): "INSPMD5:" and md5("INSPIRCD-HALF-1:k1:k2:k3") in hex.
func InspChecksum(keys [3]string) string {
	sum := md5.Sum([]byte("INSPIRCD-HALF-1:" + inspFullKey(keys)))
	return "INSPMD5:" + hex.EncodeToString(sum[:])
}

// segment is md5(id + fullkey + NUL + item), each of the first n bytes as
// one base32 digit.
func segment(full string, item []byte, id byte, n int) string {
	if len(item) > 400 {
		item = item[:400]
	}
	in := make([]byte, 0, 2+len(full)+len(item))
	in = append(in, id)
	in = append(in, full...)
	in = append(in, 0)
	in = append(in, item...)
	md := md5.Sum(in)
	out := make([]byte, n)
	for i := range out {
		out[i] = inspBase32[md[i]&0x1f]
	}
	return string(out)
}

// inspIP is the IP form, "" when ip isn't one.
func inspIP(full, ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	if v4 := p.To4(); v4 != nil && !strings.Contains(ip, ":") {
		return fmt.Sprintf("%s.%s.%d.%d.%s", segment(full, v4, 10, 3), segment(full, v4[:3], 11, 3), v4[1], v4[0], inspSuffix)
	}
	b := p.To16()
	return fmt.Sprintf("%s.%s.%s.%02x%02x.%02x%02x.%s", segment(full, b, 10, 6), segment(full, b[:8], 11, 4), segment(full, b[:6], 12, 4),
		b[2], b[3], b[0], b[1], inspSuffix)
}

// visiblePart is the text after the parts-th dot from the end (after the
// first dot when there are fewer), "" with no dot.
func visiblePart(host string, parts int) string {
	seen, sep := 0, -1
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] != '.' {
			continue
		}
		sep = i
		seen++
		if seen >= parts {
			break
		}
	}
	if seen == 0 {
		return ""
	}
	return host[sep+1:]
}

// InspHost is cloak_inspircd's cloak for host (a hostname or an IP),
// with ip for when a hostname has no usable domain part.
func InspHost(keys [3]string, host, ip string) string {
	full := inspFullKey(keys)
	if c := inspIP(full, host); c != "" {
		return c
	}
	seg := segment(full, []byte(strings.ToLower(host)), 1, 6)
	if vis := visiblePart(host, inspParts); vis != "" {
		if c := seg + "." + vis; len(c) <= inspMaxCloak {
			return c
		}
	}
	if c := inspIP(full, ip); c != "" {
		return c
	}
	return seg + "." + inspSuffix
}
