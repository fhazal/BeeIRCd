package services

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SET LANGUAGE (Anope's language support): what NickServ, ChanServ,
// MemoServ, HostServ, BotServ and HelpServ say to people comes in the
// language of their account, or the network's default_language. The
// English text is the key, as with Anope's gettext; a line a language
// lacks stays English. OperServ and AdminServ, the oper tools, stay
// English.
//
// lang/<code>.txt holds one language: a "# name: <name>" line, then one
// "<English>" = "<translation>" pair per line, both quoted as in Go.

//go:embed lang/*.txt
var langFiles embed.FS

// language is one language services can answer in.
type language struct {
	code  string
	name  string            // in that language: "Bahasa Melayu"
	lines map[string]string // English -> this language; nil for English
}

var english = &language{code: "en", name: "English"}

// languages are the languages there are, by code.
var languages = loadLanguages()

func loadLanguages() map[string]*language {
	out := map[string]*language{"en": english}
	names, _ := fs.Glob(langFiles, "lang/*.txt")
	for _, n := range names {
		b, err := langFiles.ReadFile(n)
		if err != nil {
			panic(err)
		}
		code := strings.TrimSuffix(path.Base(n), ".txt")
		l, err := parseLanguage(code, string(b))
		if err != nil {
			panic(n + ": " + err.Error())
		}
		out[code] = l
	}
	return out
}

// parseLanguage reads one lang/<code>.txt.
func parseLanguage(code, text string) (*language, error) {
	l := &language{code: code, name: code, lines: map[string]string{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "# name:"):
			l.name = strings.TrimSpace(strings.TrimPrefix(line, "# name:"))
			continue
		case strings.HasPrefix(line, "#"):
			continue
		}
		en, rest, err := quoted(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n, err)
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "=") {
			return nil, fmt.Errorf("line %d: want \"English\" = \"translation\"", n)
		}
		tr, rest, err := quoted(strings.TrimSpace(rest[1:]))
		if err != nil || strings.TrimSpace(rest) != "" {
			return nil, fmt.Errorf("line %d: want \"English\" = \"translation\"", n)
		}
		if tr != "" {
			l.lines[en] = tr
		}
	}
	return l, sc.Err()
}

func quoted(s string) (string, string, error) {
	q, err := strconv.QuotedPrefix(s)
	if err != nil {
		return "", "", err
	}
	v, err := strconv.Unquote(q)
	return v, s[len(q):], err
}

// languageCodes are the codes, English first, then by code.
func languageCodes() []string {
	var out []string
	for c := range languages {
		if c != "en" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return append([]string{"en"}, out...)
}

// Languages lists the languages there are as "en English, ms Bahasa
// Melayu", English first.
func Languages() string {
	var out []string
	for _, c := range languageCodes() {
		out = append(out, c+" "+languages[c].name)
	}
	return strings.Join(out, ", ")
}

// KnownLanguage reports whether code is a language services speak.
func KnownLanguage(code string) bool { return languages[strings.ToLower(code)] != nil }

// defaultLanguage is [services] default_language, English when unset or
// unknown.
func (s *Services) defaultLanguage() *language {
	if l := languages[strings.ToLower(s.cfg.Services.DefaultLanguage)]; l != nil {
		return l
	}
	return english
}

// langOf is the language c gets: their account's, else the default.
func (s *Services) langOf(c *Client) *language {
	if c != nil && c.accountID != 0 {
		if a, _ := s.store.AccountByID(c.accountID); a != nil && a.Language != "" {
			if l := languages[a.Language]; l != nil {
				return l
			}
		}
	}
	return s.defaultLanguage()
}

// translated reports whether sv speaks people's languages: not the oper
// tools.
func (s *Services) translated(sv *service) bool {
	return sv != nil && sv != s.operServ && sv.need != 'a' && sv.need != 'o'
}

// tr is text in c's language (text itself when there is no line for it).
// The Hive Panel's stand-in client always gets English.
func (s *Services) tr(c *Client, text string) string {
	if c == nil || c.reply != nil {
		return text
	}
	l := s.langOf(c)
	if l.lines == nil {
		return text
	}
	if t, ok := l.lines[text]; ok {
		return t
	}
	return text
}

// trFor is tr for a line sv says (oper tools stay English).
func (s *Services) trFor(c *Client, sv *service, text string) string {
	if !s.translated(sv) {
		return text
	}
	return s.tr(c, text)
}

// say sends format, in c's language, filled with a, from sv.
func (s *Services) say(c *Client, sv *service, format string, a ...any) {
	if s.translated(sv) {
		format = s.tr(c, format)
	}
	s.svcReply(c, sv, fmt.Sprintf(format, a...))
}
