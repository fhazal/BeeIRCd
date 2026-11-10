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
		b.WriteString(m.Source)
		b.WriteByte(' ')
	}
	b.WriteString(m.Command)
	for i, p := range m.Params {
		b.WriteByte(' ')
		if i == len(m.Params)-1 && (m.Trailing || p == "" || strings.IndexByte(p, ' ') >= 0 || p[0] == ':') {
			b.WriteByte(':')
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
		tags[k] = unescapeTag(v)
	}
	return tags
}

var tagEscaper = strings.NewReplacer(`\`, `\\`, ";", `\:`, " ", `\s`, "\r", `\r`, "\n", `\n`)

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
