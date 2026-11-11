package irc

import "strings"

// CleanKey drops characters a channel key can't hold and caps it at 23.
func CleanKey(k string) string {
	k = strings.Map(func(r rune) rune {
		if r <= ' ' || r == ',' || r == ':' {
			return -1
		}
		return r
	}, k)
	return Truncate(k, 23)
}

// DisplayIP makes an IP safe to show as a host: an IPv6 address starting
// with ':' would break the line format, so it gets a leading 0.
func DisplayIP(ip string) string {
	if strings.HasPrefix(ip, ":") {
		return "0" + ip
	}
	return ip
}
