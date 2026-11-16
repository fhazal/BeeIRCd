package extauth

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The few pieces of BER that LDAP's bind and search need.

const (
	tagBool     = 0x01
	tagInt      = 0x02
	tagOctets   = 0x04
	tagEnum     = 0x0a
	tagSequence = 0x30
	tagSet      = 0x31

	appBindRequest    = 0x60
	appBindResponse   = 0x61
	appUnbindRequest  = 0x42
	appSearchRequest  = 0x63
	appSearchEntry    = 0x64
	appSearchDone     = 0x65
	appExtendedReq    = 0x77
	appExtendedResp   = 0x78
	ctxSimplePassword = 0x80
)

const maxElement = 1 << 20 // the largest answer read from a directory

func tlv(tag byte, body ...[]byte) []byte {
	n := 0
	for _, b := range body {
		n += len(b)
	}
	out := make([]byte, 0, n+6)
	out = append(out, tag)
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	case n < 0x100:
		out = append(out, 0x81, byte(n))
	case n < 0x10000:
		out = append(out, 0x82, byte(n>>8), byte(n))
	default:
		out = append(out, 0x83, byte(n>>16), byte(n>>8), byte(n))
	}
	for _, b := range body {
		out = append(out, b...)
	}
	return out
}

func berInt(tag byte, v int) []byte {
	if v < 0 {
		v = 0
	}
	var b []byte
	for {
		b = append([]byte{byte(v)}, b...)
		v >>= 8
		if v == 0 {
			break
		}
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return tlv(tag, b)
}

func berString(tag byte, s string) []byte { return tlv(tag, []byte(s)) }

func berBool(v bool) []byte {
	if v {
		return tlv(tagBool, []byte{0xff})
	}
	return tlv(tagBool, []byte{0})
}

// element is one decoded tag with its content.
type element struct {
	tag  byte
	body []byte
}

// readElement reads one element from r.
func readElement(r *bufio.Reader) (element, error) {
	tag, err := r.ReadByte()
	if err != nil {
		return element{}, err
	}
	if tag&0x1f == 0x1f {
		return element{}, errors.New("ldap: long tags are not supported")
	}
	l, err := r.ReadByte()
	if err != nil {
		return element{}, err
	}
	n := int(l)
	if l&0x80 != 0 {
		k := int(l & 0x7f)
		if k == 0 || k > 3 {
			return element{}, errors.New("ldap: bad length")
		}
		n = 0
		for i := 0; i < k; i++ {
			b, err := r.ReadByte()
			if err != nil {
				return element{}, err
			}
			n = n<<8 | int(b)
		}
	}
	if n > maxElement {
		return element{}, errors.New("ldap: answer too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return element{}, err
	}
	return element{tag, body}, nil
}

// children splits a constructed element's content.
func children(body []byte) ([]element, error) {
	var out []element
	for len(body) > 0 {
		if len(body) < 2 || body[0]&0x1f == 0x1f {
			return nil, errors.New("ldap: bad element")
		}
		tag, l := body[0], body[1]
		body = body[2:]
		n := int(l)
		if l&0x80 != 0 {
			k := int(l & 0x7f)
			if k == 0 || k > 3 || len(body) < k {
				return nil, errors.New("ldap: bad length")
			}
			n = 0
			for i := 0; i < k; i++ {
				n = n<<8 | int(body[i])
			}
			body = body[k:]
		}
		if n > len(body) {
			return nil, errors.New("ldap: short element")
		}
		out = append(out, element{tag, body[:n]})
		body = body[n:]
	}
	return out, nil
}

func (e element) int() int {
	v := 0
	for _, b := range e.body {
		v = v<<8 | int(b)
	}
	return v
}

// escapeFilter makes v safe inside a search filter (RFC 4515).
func escapeFilter(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '*', '(', ')', '\\', 0:
			fmt.Fprintf(&b, "\\%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// escapeDN makes v safe as a value inside a DN (RFC 4514).
func escapeDN(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == 0:
			b.WriteString("\\00")
		case strings.IndexByte(`,+"\<>;=`, c) >= 0,
			c == '#' && i == 0,
			c == ' ' && (i == 0 || i == len(v)-1):
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// encodeFilter turns a filter like (&(uid=ann)(objectClass=person)) into
// its BER form.
func encodeFilter(f string) ([]byte, error) {
	out, rest, err := parseFilter(f)
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, fmt.Errorf("ldap filter: unexpected %q", rest)
	}
	return out, nil
}

func parseFilter(f string) (out []byte, rest string, err error) {
	if !strings.HasPrefix(f, "(") {
		return nil, "", fmt.Errorf("ldap filter: expected ( at %q", f)
	}
	f = f[1:]
	if f == "" {
		return nil, "", errors.New("ldap filter: ends early")
	}
	switch f[0] {
	case '&', '|':
		tag := byte(0xa0)
		if f[0] == '|' {
			tag = 0xa1
		}
		f = f[1:]
		var parts [][]byte
		for strings.HasPrefix(f, "(") {
			var p []byte
			if p, f, err = parseFilter(f); err != nil {
				return nil, "", err
			}
			parts = append(parts, p)
		}
		if !strings.HasPrefix(f, ")") {
			return nil, "", errors.New("ldap filter: missing )")
		}
		return tlv(tag, parts...), f[1:], nil
	case '!':
		p, r, err := parseFilter(f[1:])
		if err != nil {
			return nil, "", err
		}
		if !strings.HasPrefix(r, ")") {
			return nil, "", errors.New("ldap filter: missing )")
		}
		return tlv(0xa2, p), r[1:], nil
	}
	end := -1
	for i := 0; i < len(f); i++ {
		if f[i] == '\\' {
			i += 2
			continue
		}
		if f[i] == '(' {
			return nil, "", errors.New("ldap filter: ( inside a value; write it as \\28")
		}
		if f[i] == ')' {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", errors.New("ldap filter: missing )")
	}
	item, rest := f[:end], f[end+1:]
	eq := strings.IndexByte(item, '=')
	if eq < 1 {
		return nil, "", fmt.Errorf("ldap filter: %q has no attribute=value", item)
	}
	attr, val := item[:eq], item[eq+1:]
	tag := byte(0xa3) // equality
	switch attr[len(attr)-1] {
	case '>':
		tag, attr = 0xa5, attr[:len(attr)-1]
	case '<':
		tag, attr = 0xa6, attr[:len(attr)-1]
	case '~':
		tag, attr = 0xa8, attr[:len(attr)-1]
	case ':':
		return nil, "", errors.New("ldap filter: extensible matches (:=) are not supported")
	}
	if attr == "" {
		return nil, "", fmt.Errorf("ldap filter: %q has no attribute", item)
	}
	if tag == 0xa3 && val == "*" {
		return berString(0x87, attr), rest, nil // present
	}
	if tag == 0xa3 && strings.Contains(val, "*") {
		pieces := strings.Split(val, "*")
		var subs [][]byte
		for i, p := range pieces {
			if p == "" {
				continue
			}
			v, err := unescapeFilter(p)
			if err != nil {
				return nil, "", err
			}
			t := byte(0x81) // any
			if i == 0 {
				t = 0x80 // initial
			} else if i == len(pieces)-1 {
				t = 0x82 // final
			}
			subs = append(subs, tlv(t, v))
		}
		return tlv(0xa4, berString(tagOctets, attr), tlv(tagSequence, subs...)), rest, nil
	}
	v, err := unescapeFilter(val)
	if err != nil {
		return nil, "", err
	}
	return tlv(tag, berString(tagOctets, attr), tlv(tagOctets, v)), rest, nil
}

func unescapeFilter(v string) ([]byte, error) {
	out := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			out = append(out, v[i])
			continue
		}
		if i+2 >= len(v) {
			return nil, errors.New("ldap filter: \\ needs two hex digits")
		}
		var b byte
		if _, err := fmt.Sscanf(v[i+1:i+3], "%02x", &b); err != nil {
			return nil, errors.New("ldap filter: \\ needs two hex digits")
		}
		out = append(out, b)
		i += 2
	}
	return out, nil
}
