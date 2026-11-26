package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Doc is a config file as lines, for small changes that keep everything
// else (comments, order, blank lines) as it was.
type Doc struct {
	lines []string
}

// Edit reads the file at path, lets fn change it, checks that the result
// still loads, and only then saves it. It returns the new config.
func Edit(path string, fn func(*Doc) error) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	d := &Doc{lines: strings.Split(strings.TrimSuffix(text, "\n"), "\n")}
	if err := fn(d); err != nil {
		return nil, err
	}
	out := strings.Join(d.lines, eol) + eol
	cfg, err := load(path, out)
	if err != nil {
		return nil, fmt.Errorf("the change would break the config: %w", err)
	}
	if err := ReplaceFile(path, []byte(out)); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ReplaceFile puts data in the file at path so that a crash leaves either
// the old text or the new: it goes to a temp file next to it, on disk
// before it is renamed over the old one, with the old one's mode and,
// where this process may set it, its owner. A
// symlinked config is written where the link points, so the link stays.
func ReplaceFile(path string, data []byte) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	dir := filepath.Dir(real)
	tmp := filepath.Join(dir, "."+filepath.Base(real)+".new")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		keepOwner(f, info)
		err = f.Chmod(info.Mode().Perm()) // not narrowed by the umask
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, real)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	// The rename itself reaches the disk with the folder. Not every system
	// can sync a folder (Windows can't); the file is already safe there.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// String is the file as text.
func (d *Doc) String() string { return strings.Join(d.lines, "\n") + "\n" }

// header returns the table name of a header line, like "limits" or
// "[listen]" for an array table, or "" when the line isn't a header.
func header(line string) string {
	t := strings.TrimSpace(stripComment(line))
	if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
		return ""
	}
	if strings.HasPrefix(t, "[[") && strings.HasSuffix(t, "]]") {
		return "[" + strings.TrimSpace(t[2:len(t)-2]) + "]"
	}
	return strings.TrimSpace(t[1 : len(t)-1])
}

// heads is header for every line, read in order: a line inside a value
// that spans lines (an array, or a """ string) is never a header, even
// one like ["x"] on its own.
func (d *Doc) heads() []string {
	out := make([]string, len(d.lines))
	var sc valueScan
	for i, l := range d.lines {
		if sc.depth == 0 && sc.multi == "" {
			if out[i] = header(l); out[i] != "" {
				continue
			}
		}
		sc.line(l)
	}
	return out
}

// valueScan follows the brackets and multi-line strings of values from
// line to line.
type valueScan struct {
	depth int    // arrays still open
	multi string // `"""` or `'''` while inside a multi-line string
}

// line follows l and returns where a # comment starts on it, or -1.
func (sc *valueScan) line(l string) int {
	inStr := byte(0)
	for i := 0; i < len(l); i++ {
		ch := l[i]
		switch {
		case sc.multi != "":
			if sc.multi == `"""` && ch == '\\' {
				i++
			} else if strings.HasPrefix(l[i:], sc.multi) {
				sc.multi, i = "", i+2
			}
		case inStr == '"' && ch == '\\':
			i++
		case inStr != 0:
			if ch == inStr {
				inStr = 0
			}
		case strings.HasPrefix(l[i:], `"""`) || strings.HasPrefix(l[i:], `'''`):
			sc.multi, i = l[i:i+3], i+2
		case ch == '"' || ch == '\'':
			inStr = ch
		case ch == '#':
			return i
		case ch == '[':
			sc.depth++
		case ch == ']' && sc.depth > 0:
			sc.depth--
		}
	}
	return -1
}

// stripComment cuts a # comment that isn't inside a string.
func stripComment(line string) string {
	_, _, at := splitComment(line)
	if at < 0 {
		return line
	}
	return line[:at]
}

// splitComment finds where a # comment starts outside quotes.
func splitComment(line string) (string, string, int) {
	inStr := byte(0)
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case inStr != 0 && ch == '\\' && inStr == '"':
			i++
		case inStr != 0 && ch == inStr:
			inStr = 0
		case inStr == 0 && (ch == '"' || ch == '\''):
			inStr = ch
		case inStr == 0 && ch == '#':
			return line[:i], line[i:], i
		}
	}
	return line, "", -1
}

// section returns the lines [start, end) of the table that begins at
// header line h. Comments right above the next header belong to that one.
func (d *Doc) section(h int) (int, int) {
	end := len(d.lines)
	hs := d.heads()
	for i := h + 1; i < len(d.lines); i++ {
		if hs[i] != "" {
			end = i
			for end > h+1 && strings.HasPrefix(strings.TrimSpace(d.lines[end-1]), "#") {
				end--
			}
			break
		}
	}
	return h, end
}

// lastContent is the line after the last key or comment of a section.
func (d *Doc) lastContent(start, end int) int {
	at := start + 1
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(d.lines[i]) != "" {
			at = i + 1
		}
	}
	return at
}

func (d *Doc) insert(at int, lines ...string) {
	d.lines = append(d.lines[:at], append(lines, d.lines[at:]...)...)
}

func keyRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*=`)
}

// Set writes key = value (value is TOML, use Quote for text) into table,
// replacing the old value and keeping a comment after it.
func (d *Doc) Set(table, key, value string) {
	for h, name := range d.heads() {
		if name != table {
			continue
		}
		start, end := d.section(h)
		if v, ok := d.find(start, end, key); ok {
			// The value now fits one line: a comment from its first line
			// goes after it, or above it when the old last line had one.
			line := v.indent + key + " = " + value
			lines := []string{line}
			switch {
			case v.lead != "" && v.comment != "":
				lines = []string{v.indent + v.lead, line + " " + v.comment}
			case v.lead != "":
				lines[0] += " " + v.lead
			case v.comment != "":
				lines[0] += " " + v.comment
			}
			d.lines = append(d.lines[:v.first], append(lines, d.lines[v.last+1:]...)...)
			return
		}
		d.insert(d.lastContent(start, end), key+" = "+value)
		return
	}
	if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, "["+table+"]", key+" = "+value)
}

// Unset removes key from table (nothing happens if it isn't there).
func (d *Doc) Unset(table, key string) {
	for h, name := range d.heads() {
		if name != table {
			continue
		}
		start, end := d.section(h)
		if v, ok := d.find(start, end, key); ok {
			d.lines = append(d.lines[:v.first], d.lines[v.last+1:]...)
		}
		return
	}
}

// value reads key's raw value in the section [start, end). A value that
// spans lines (an array, a """ string) comes whole, its lines joined by
// "\n"; a line inside such a value is never taken for a key.
func (d *Doc) value(start, end int, key string) (string, bool) {
	v, ok := d.find(start, end, key)
	return v.text, ok
}

// keyValue is where a key and its value sit in the file.
type keyValue struct {
	first, last int    // the key's line and the last line of its value
	indent      string // before the key
	text        string // the value whole, as wholeValue gives it
	comment     string // the # comment after the value, or ""
	lead        string // a # comment on the key's line when the value goes on past it
}

// find finds key in the section [start, end), skipping lines inside
// other values that span lines.
func (d *Doc) find(start, end int, key string) (keyValue, bool) {
	re := keyRe(key)
	var sc valueScan
	for i := start + 1; i < end; i++ {
		l := d.lines[i]
		if sc.depth == 0 && sc.multi == "" {
			if m := re.FindStringSubmatchIndex(l); m != nil {
				v := d.wholeValue(i, end, l[m[1]:])
				v.indent = l[m[2]:m[3]]
				return v, true
			}
		}
		sc.line(l)
	}
	return keyValue{}, false
}

// wholeValue is the value that starts with first on line i and goes on
// while an array or multi-line string is still open, up to end. Comments
// and blank lines between array items are left out of its text; the text
// of a multi-line string is kept as it is.
func (d *Doc) wholeValue(i, end int, first string) keyValue {
	v := keyValue{first: i, last: i}
	var sc valueScan
	if at := sc.line(first); at >= 0 {
		v.comment = first[at:]
	}
	parts := []string{strings.TrimSpace(stripComment(first))}
	if sc.depth > 0 || sc.multi != "" {
		v.lead, v.comment = v.comment, ""
	}
	for j := i + 1; j < end && (sc.depth > 0 || sc.multi != ""); j++ {
		l := d.lines[j]
		if sc.multi != "" {
			parts = append(parts, l)
		} else if p := strings.TrimSpace(stripComment(l)); p != "" {
			parts = append(parts, p)
		}
		v.last, v.comment = j, ""
		if at := sc.line(l); at >= 0 {
			v.comment = l[at:]
		}
	}
	v.text = strings.Join(parts, "\n")
	return v
}

// listens returns the header line of every [[listen]] block.
func (d *Doc) listens() []int {
	var out []int
	for i, name := range d.heads() {
		if name == "[listen]" {
			out = append(out, i)
		}
	}
	return out
}

// AddListen adds a [[listen]] block after the last one.
func (d *Doc) AddListen(address string, tls bool) {
	block := []string{"[[listen]]", "address = " + Quote(address)}
	if tls {
		block = append(block, "tls = true")
	}
	all := d.listens()
	if len(all) == 0 {
		if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
			d.lines = append(d.lines, "")
		}
		d.lines = append(d.lines, block...)
		return
	}
	start, end := d.section(all[len(all)-1])
	at := d.lastContent(start, end)
	d.insert(at, append([]string{""}, block...)...)
}

// DelListen removes the [[listen]] block with this address.
func (d *Doc) DelListen(address string) bool {
	for _, h := range d.listens() {
		start, end := d.section(h)
		v, ok := d.value(start, end, "address")
		if !ok || unquote(v) != address {
			continue
		}
		cut := d.lastContent(start, end)
		// Take one blank line before the block with it.
		if start > 0 && strings.TrimSpace(d.lines[start-1]) == "" {
			start--
		}
		d.lines = append(d.lines[:start], d.lines[cut:]...)
		return true
	}
	return false
}

// Quote makes text a TOML string.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func unquote(v string) string {
	if u, err := strconv.Unquote(v); err == nil {
		return u
	}
	return strings.Trim(v, `"'`)
}

// FormatDuration writes a time the way the file takes it: 90d, 12h, 5m, 90s.
func FormatDuration(d time.Duration) string {
	day := 24 * time.Hour
	switch {
	case d >= day && d%day == 0:
		return strconv.Itoa(int(d/day)) + "d"
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Second)) + "s"
}

// ParseDuration reads 90d, 12h, 5m or 90s.
func ParseDuration(s string) (time.Duration, error) {
	var d Duration
	err := d.UnmarshalText([]byte(s))
	return d.Duration, err
}

// KV is one key = value line of a block (Value is TOML: use Quote).
type KV struct{ Key, Value string }

// blocks returns the header line of every [[name]] block.
func (d *Doc) blocks(name string) []int {
	var out []int
	for i, h := range d.heads() {
		if h == "["+name+"]" {
			out = append(out, i)
		}
	}
	return out
}

// findBlock returns the [[name]] block whose keyName equals keyValue
// (case doesn't matter), as its line range.
func (d *Doc) findBlock(name, keyName, keyValue string) (int, int, bool) {
	for _, h := range d.blocks(name) {
		start, end := d.section(h)
		if v, ok := d.value(start, end, keyName); ok && strings.EqualFold(unquote(v), keyValue) {
			return start, end, true
		}
	}
	return 0, 0, false
}

// SetBlock writes a [[name]] block with these lines, replacing the one
// whose kv[0] value matches (kv[0] is the block's name key), or adding it
// after the last [[name]] block (or at the end).
func (d *Doc) SetBlock(name string, kv []KV) {
	block := []string{"[[" + name + "]]"}
	for _, p := range kv {
		block = append(block, p.Key+" = "+p.Value)
	}
	if start, end, ok := d.findBlock(name, kv[0].Key, unquote(kv[0].Value)); ok {
		cut := d.lastContent(start, end)
		d.lines = append(d.lines[:start], append(block, d.lines[cut:]...)...)
		return
	}
	if all := d.blocks(name); len(all) > 0 {
		start, end := d.section(all[len(all)-1])
		d.insert(d.lastContent(start, end), append([]string{""}, block...)...)
		return
	}
	if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, block...)
}

// DelBlock removes the [[name]] block whose keyName equals keyValue.
func (d *Doc) DelBlock(name, keyName, keyValue string) bool {
	start, end, ok := d.findBlock(name, keyName, keyValue)
	if !ok {
		return false
	}
	cut := d.lastContent(start, end)
	if start > 0 && strings.TrimSpace(d.lines[start-1]) == "" {
		start--
	}
	d.lines = append(d.lines[:start], d.lines[cut:]...)
	return true
}

// QuoteList makes a TOML list of strings.
func QuoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// SetBlockKey writes key = value (TOML) into the [[name]] block whose
// keyName equals keyValue, keeping its other lines; false when there is
// no such block.
func (d *Doc) SetBlockKey(name, keyName, keyValue, key, value string) bool {
	start, end, ok := d.findBlock(name, keyName, keyValue)
	if !ok {
		return false
	}
	re := keyRe(key)
	for i := start + 1; i < end; i++ {
		if m := re.FindStringSubmatch(d.lines[i]); m != nil {
			_, comment, _ := splitComment(d.lines[i])
			line := m[1] + key + " = " + value
			if comment != "" {
				line += " " + comment
			}
			d.lines[i] = line
			return true
		}
	}
	d.insert(d.lastContent(start, end), key+" = "+value)
	return true
}
