// Package irc holds the IRC wire format: parsing and building lines, nick and
// channel rules, case folding and mask matching. It does no I/O.
package irc

import (
	"errors"
	"sort"
	"strings"
)

// MaxLine is the longest line, without tags and without CRLF, that we send.
const MaxLine = 510

// ErrEmpty is returned for a line with no command.
var ErrEmpty = errors.New("empty message")

// Message is one IRC line.
type Message struct {
	Tags    map[string]string
	Source  string
	Command string
	Params  []string
	// Trailing marks the last parameter with ':' even when it doesn't need
	// it. Use it when the last parameter is free text.
	Trailing bool
}

// Parse reads one line (CRLF optional). The command is upper-cased.
func Parse(line string) (Message, error) {
	line = strings.TrimRight(line, "\r\n")
	var m Message
	line = strings.TrimLeft(line, " ")
	if strings.HasPrefix(line, "@") {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return m, ErrEmpty
		}
		m.Tags = parseTags(line[1:sp])
		line = strings.TrimLeft(line[sp+1:], " ")
	}
	if strings.HasPrefix(line, ":") {
		sp := strings.IndexByte(line, ' ')
		if sp <= 1 {
			return Message{}, ErrEmpty
		}
		m.Source = line[1:sp]
		line = strings.TrimLeft(line[sp+1:], " ")
	}
	for line != "" {
		if m.Command != "" && strings.HasPrefix(line, ":") {
			m.Params = append(m.Params, line[1:])
			break
		}
		var word string
		if sp := strings.IndexByte(line, ' '); sp < 0 {
			word, line = line, ""
		} else {
			word, line = line[:sp], strings.TrimLeft(line[sp+1:], " ")
		}
		if m.Command == "" {
			m.Command = strings.ToUpper(word)
		} else {
			m.Params = append(m.Params, word)
		}
	}
	if m.Command == "" || strings.HasPrefix(m.Command, ":") {
		return Message{}, ErrEmpty
	}
	return m, nil
}

// String builds the line without CRLF. The last parameter gets a ':' only
// when it needs one (empty, has a space, or starts with ':').
func (m Message) String() string {
	var b strings.Builder
	if len(m.Tags) > 0 {
		keys := make([]string, 0, len(m.Tags))
		for k := range m.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('@')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteString(k)
			if v := m.Tags[k]; v != "" {
				b.WriteByte('=')
				b.WriteString(EscapeTag(v))
			}
		}
		b.WriteByte(' ')
	}
	if m.Source != "" {
		b.WriteByte(':')
		b.WriteString(CleanFraming(m.Source))
		b.WriteByte(' ')
	}
	b.WriteString(CleanFraming(m.Command))
	for i, p := range m.Params {
		p = CleanFraming(p) // a CR, LF or NUL would let text start a new line
		b.WriteByte(' ')
		if i == len(m.Params)-1 {
			if m.Trailing || p == "" || strings.IndexByte(p, ' ') >= 0 || p[0] == ':' {
				b.WriteByte(':')
			}
		} else {
			p = middleParam(p)
		}
		b.WriteString(p)
	}
	return b.String()
}

// Param returns parameter i, or "" when there is none.
func (m Message) Param(i int) string {
	if i < len(m.Params) {
		return m.Params[i]
	}
	return ""
}

// Truncate cuts s to at most n bytes without splitting a UTF-8 character.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

func parseTags(raw string) map[string]string {
	tags := make(map[string]string)
	for _, part := range strings.Split(raw, ";") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if !validTagKey(k) {
			continue // IRCv3 message-tags: [+][vendor/]name of letters, digits, -
		}
		tags[k] = unescapeTag(v)
	}
	return tags
}

// tagEscaper escapes tag values; NUL has no escape in IRCv3 and is dropped.
var tagEscaper = strings.NewReplacer(`\`, `\\`, ";", `\:`, " ", `\s`, "\r", `\r`, "\n", `\n`, "\x00", "")

// EscapeTag escapes a tag value for the wire.
func EscapeTag(v string) string { return tagEscaper.Replace(v) }

func unescapeTag(v string) string {
	if strings.IndexByte(v, '\\') < 0 {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(v) {
			break
		}
		switch v[i] {
		case ':':
			b.WriteByte(';')
		case 's':
			b.WriteByte(' ')
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		default:
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

// middleParam makes p safe as a parameter that isn't the last: never
// empty, no spaces, no leading ':' (any of those would shift the
// parameters after it, so an echoed argument could forge one).
func middleParam(p string) string {
	if p == "" {
		return "*"
	}
	if strings.IndexByte(p, ' ') >= 0 {
		p = strings.ReplaceAll(p, " ", "_")
	}
	if p[0] == ':' {
		p = "_" + p[1:]
	}
	return p
}

// validTagKey reports whether k is a tag key IRCv3 allows: an optional +,
// an optional vendor (a host name) and /, then letters, digits and -.
func validTagKey(k string) bool {
	k = strings.TrimPrefix(k, "+")
	if i := strings.LastIndexByte(k, '/'); i >= 0 {
		vendor := k[:i]
		if vendor == "" {
			return false
		}
		for j := 0; j < len(vendor); j++ {
			if c := vendor[j]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
				return false
			}
		}
		k = k[i+1:]
	}
	if k == "" {
		return false
	}
	for j := 0; j < len(k); j++ {
		if c := k[j]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// framing are the bytes that end or break an IRC line.
const framing = "\r\n\x00"

// SplitFraming cuts one line read up to LF into the IRC lines it holds:
// CR, LF and CRLF all end a line (a bare CR can't hide a second line in
// a message), empty lines are dropped, and a line with a NUL byte is
// dropped whole (NUL is never allowed in IRC).
func SplitFraming(line string) []string {
	if strings.IndexByte(line, '\r') < 0 && strings.IndexByte(line, '\n') < 0 {
		if line == "" || strings.IndexByte(line, 0) >= 0 {
			return nil
		}
		return []string{line}
	}
	var out []string
	for _, l := range strings.FieldsFunc(line, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if strings.IndexByte(l, 0) < 0 {
			out = append(out, l)
		}
	}
	return out
}

// CleanFraming turns any CR, LF or NUL in s into a space, so text from
// anywhere (config files, services data) can't break an outgoing line.
func CleanFraming(s string) string {
	if !strings.ContainsAny(s, framing) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, s)
}
