package irc

import (
	"net/netip"
	"strconv"
	"strings"
)

// MatchHost matches a host mask against host: a glob like *.example.org,
// or a CIDR range like 10.0.0.0/8 or 2001:db8::/32 (which only matches IPs).
func MatchHost(mask, host string) bool {
	if strings.Contains(mask, "/") {
		p, err := netip.ParsePrefix(mask)
		if err != nil {
			return false
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return false
		}
		return p.Contains(a.Unmap())
	}
	return Match(mask, host)
}

// MatchUserHost matches a user@host mask against ident at any of hosts.
func MatchUserHost(mask, ident string, hosts ...string) bool {
	user, host, ok := strings.Cut(mask, "@")
	if !ok || !Match(user, ident) {
		return false
	}
	for _, h := range hosts {
		if h != "" && MatchHost(host, h) {
			return true
		}
	}
	return false
}

// IsCIDR reports whether mask is a valid CIDR range.
func IsCIDR(mask string) bool {
	_, err := netip.ParsePrefix(mask)
	return err == nil
}

// Extbans (UnrealIRCd/InspIRCd style) in +b, +e and +I:
//
//	~a:account   ~a:0 = not identified, ~a:* = anyone identified
//	~r:realname
//	~m:mask      mute: may stay, may not talk (~q:mask is the same)
//	~t:min:mask  timed: lifts itself after min minutes
const ExtbanLetters = "amqrt"

// MaxTimedBan is the longest ~t ban, in minutes (one year).
const MaxTimedBan = 525600

// NormalizeBan is NormalizeMask that also takes extbans; false when an
// extban is malformed.
func NormalizeBan(m string) (string, bool) {
	if m == "" || strings.ContainsAny(m, " \r\n\x00") {
		return "", false // a mask with a space would break list replies (367)
	}
	if !strings.HasPrefix(m, "~") {
		return NormalizeMask(m), true
	}
	if len(m) < 4 || m[2] != ':' {
		return "", false
	}
	letter, arg := m[1]|0x20, m[3:]
	switch letter {
	case 'a', 'r':
		if arg == "" || strings.ContainsAny(arg, " ") {
			return "", false
		}
		out := Truncate("~"+string(letter)+":"+arg, 100)
		if len(out) <= 3 { // the cut left no account or realname (bad UTF-8)
			return "", false
		}
		return out, true
	case 'm', 'q':
		inner, ok := NormalizeBan(arg)
		if !ok || strings.HasPrefix(inner, "~m:") || strings.HasPrefix(inner, "~q:") || strings.HasPrefix(inner, "~t:") {
			return "", false
		}
		return "~" + string(letter) + ":" + inner, true
	case 't':
		mins, rest, ok := strings.Cut(arg, ":")
		n, err := strconv.Atoi(mins)
		if !ok || err != nil || n < 1 || n > MaxTimedBan {
			return "", false
		}
		inner, ok := NormalizeBan(rest)
		if !ok || strings.HasPrefix(inner, "~t:") {
			return "", false
		}
		return "~t:" + strconv.Itoa(n) + ":" + inner, true
	}
	return "", false
}

// TimedBan reads "~t:30:mask" as 30 minutes; false for other masks.
func TimedBan(m string) (minutes int, ok bool) {
	if !strings.HasPrefix(m, "~t:") {
		return 0, false
	}
	mins, _, found := strings.Cut(m[3:], ":")
	n, err := strconv.Atoi(mins)
	return n, found && err == nil
}
