package link

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// InspIRCd's R-line is a regular expression (the engine is the server's
// config: posix, pcre, stdlib or re2), matched against both
// "nick!user@realhost realname" and "nick!user@ip realname". A BeeIRCd
// realname ban only looks at the realname, so on the way out it becomes
//
//	^[^[:space:]]+[[:space:]]<the mask as a regex>$
//
// where the first part skips "nick!user@host " and the rest is our glob.
// Only syntax that all four engines read is used: [[:space:]] for a space
// (a literal space cannot sit in the ADDLINE line anyway), [xX] pairs for
// a letter (ours is case-insensitive, and not every engine can be told to
// be), .* for *, . for ?, and a backslash before the other metacharacters.
const (
	rlinePrefix = `^[^[:space:]]+[[:space:]]`
	rlineSuffix = `$`
	rlineSpace  = `[[:space:]]`
	// rlineUser is the user field the bridge puts on the TKL it makes
	// from an R-line: the host is the base64 of our own mask.
	rlineUser = "~rline:"
	rlineMeta = `\.+()[]{}|^$`
)

// rlineFromGlob turns our realname glob (a space written ' ') into the
// regular expression InspIRCd gets.
func rlineFromGlob(glob string) string {
	var sb strings.Builder
	sb.WriteString(rlinePrefix)
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*':
			sb.WriteString(".*")
		case c == '?':
			sb.WriteByte('.')
		case c == ' ':
			sb.WriteString(rlineSpace)
		case c >= 'a' && c <= 'z':
			sb.WriteString("[" + string(rune(c)) + string(rune(c-32)) + "]")
		case c >= 'A' && c <= 'Z':
			sb.WriteString("[" + string(rune(c)) + string(rune(c+32)) + "]")
		case strings.IndexByte(rlineMeta, c) >= 0:
			sb.WriteByte(0x5c)
			sb.WriteByte(c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteString(rlineSuffix)
	return sb.String()
}

// globFromRline is the reverse: it succeeds only for an expression of
// exactly the shape rlineFromGlob makes.
func globFromRline(rx string) (string, bool) {
	body, ok := strings.CutPrefix(rx, rlinePrefix)
	if !ok {
		return "", false
	}
	if body, ok = strings.CutSuffix(body, rlineSuffix); !ok {
		return "", false
	}
	var sb strings.Builder
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case strings.HasPrefix(body[i:], ".*"):
			sb.WriteByte('*')
			i += 2
		case c == '.':
			sb.WriteByte('?')
			i++
		case strings.HasPrefix(body[i:], rlineSpace):
			sb.WriteByte(' ')
			i += len(rlineSpace)
		case c == '[':
			if i+3 >= len(body) || body[i+3] != ']' || !isLetter(body[i+1]) || body[i+2] != body[i+1]^0x20 {
				return "", false
			}
			sb.WriteByte(body[i+1])
			i += 4
		case c == 0x5c:
			if i+1 >= len(body) || strings.IndexByte(rlineMeta, body[i+1]) < 0 {
				return "", false
			}
			sb.WriteByte(body[i+1])
			i += 2
		case isLetter(c) || strings.IndexByte(rlineMeta, c) >= 0 || c == '*' || c == '?':
			return "", false // a letter we would have put in a class, or a bare metacharacter
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return sb.String(), true
}

func isLetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

// rlineIn decides what BeeIRCd does with an InspIRCd R-line: the mask for
// our realname ban (a glob, or a /regex/), or why it is not copied.
func rlineIn(rx string) (mask, why string) {
	if g, ok := globFromRline(rx); ok {
		if strings.Trim(g, "*?") == "" {
			return "", "it would match everybody"
		}
		return g, ""
	}
	rest, ok := strings.CutPrefix(rx, rlinePrefix)
	if !ok {
		return "", "it is a regular expression over the whole nick!user@host realname text, and BeeIRCd realname bans only see the realname"
	}
	if _, err := regexp.Compile("(?i)" + rest); err != nil {
		return "", "its pattern is not one BeeIRCd can read (" + err.Error() + ")"
	}
	return "/^(?:" + rest + ")/", ""
}

// rlineHost is the host field of the TKL for mask.
func rlineHost(mask string) string { return base64.RawURLEncoding.EncodeToString([]byte(mask)) }
