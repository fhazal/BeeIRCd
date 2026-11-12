package irc

import (
	"strings"
	"sync/atomic"
)

// Casemappings: how nicks and channel names ignore case.
const (
	// CaseASCII folds only A-Z (UnrealIRCd 6, InspIRCd 4): the default.
	CaseASCII = "ascii"
	// CaseRFC1459 also folds []\~ to {}|^ (the old Scandinavian rule).
	CaseRFC1459 = "rfc1459"
)

// rfc1459 is the casemapping in use. It is set once at start, before
// anything compares names (SetCasemapping), and never changes after;
// atomic because tests run several servers in one process.
var rfc1459 atomic.Bool

// SetCasemapping chooses the casemapping (CaseASCII or CaseRFC1459). Call
// it before any name is stored or compared.
func SetCasemapping(name string) {
	rfc1459.Store(name == CaseRFC1459)
}

// Casemapping is the casemapping in use, as ISUPPORT names it.
func Casemapping() string {
	if rfc1459.Load() {
		return CaseRFC1459
	}
	return CaseASCII
}

// Fold lower-cases s with the casemapping in use. Use it for every nick
// and channel comparison.
func Fold(s string) string {
	return FoldWith(rfc1459.Load(), s)
}

// FoldWith lower-cases s with ascii or (rfc true) rfc1459, whatever is in
// use (for moving stored names from one to the other).
func FoldWith(rfc bool, s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c + 32
		case !rfc:
		case c == '[':
			b[i] = '{'
		case c == ']':
			b[i] = '}'
		case c == '\\':
			b[i] = '|'
		case c == '~':
			b[i] = '^'
		}
	}
	return string(b)
}

// EqualFold reports whether a and b are the same name.
func EqualFold(a, b string) bool { return Fold(a) == Fold(b) }

// IsChannel reports whether name looks like a channel.
func IsChannel(name string) bool { return strings.HasPrefix(name, "#") }

func isLetter(c byte) bool  { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool   { return c >= '0' && c <= '9' }
func isSpecial(c byte) bool { return strings.IndexByte("[]\\`_^{|}", c) >= 0 }

// ValidNick reports whether nick is allowed: a letter or one of []\`_^{|}
// first, then those, digits or '-', at most maxLen bytes.
func ValidNick(nick string, maxLen int) bool {
	if nick == "" || len(nick) > maxLen {
		return false
	}
	if !isLetter(nick[0]) && !isSpecial(nick[0]) {
		return false
	}
	for i := 1; i < len(nick); i++ {
		c := nick[i]
		if !isLetter(c) && !isDigit(c) && !isSpecial(c) && c != '-' {
			return false
		}
	}
	return true
}

// ValidChannel reports whether name is an allowed channel name: '#' then at
// least one character, no spaces, commas, colons or control characters.
func ValidChannel(name string, maxLen int) bool {
	if len(name) < 2 || len(name) > maxLen || name[0] != '#' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c == ',' || c == ':' || c == 0x7f {
			return false
		}
	}
	return true
}

// Match reports whether s matches the glob pattern (* and ?), ignoring case
// with the casemapping in use.
func Match(pattern, s string) bool {
	p, t := Fold(pattern), Fold(s)
	pi, ti := 0, 0
	star, mark := -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// NormalizeMask turns what people type after +b into a full nick!user@host
// mask: "bob" -> "bob!*@*", "*.example" -> "*!*@*.example",
// "user@host" -> "*!user@host", "nick!user" -> "nick!user@*".
func NormalizeMask(m string) string {
	hasBang, hasAt := strings.Contains(m, "!"), strings.Contains(m, "@")
	switch {
	case hasBang && hasAt:
	case hasAt:
		m = "*!" + m
	case hasBang:
		m += "@*"
	case strings.ContainsAny(m, ".:"):
		m = "*!*@" + m
	default:
		m += "!*@*"
	}
	// Cut each part on its own (like UnrealIRCd), so a long mask keeps its
	// nick!user@host shape and still matches: 30 + 1 + 10 + 1 + 58 = 100.
	at := strings.LastIndexByte(m, '@')
	host := m[at+1:]
	nick, user, _ := strings.Cut(m[:at], "!")
	if !strings.Contains(m[:at], "!") {
		nick, user = "*", m[:at]
	}
	return orStar(Truncate(nick, 30)) + "!" + orStar(Truncate(user, 10)) + "@" + orStar(Truncate(host, 58))
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}
