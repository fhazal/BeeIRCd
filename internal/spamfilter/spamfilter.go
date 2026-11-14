// Package spamfilter holds BeeIRCd's spam filter rules: what a rule is,
// how its match is compiled and checked, and the normal chat lines every
// new rule is tried on first (the safety net).
//
// It follows UnrealIRCd's spamfilter so its opers know it: the same target
// letters, action names and TKL F line on links. Matching is always
// case-insensitive on the text with colours and formatting taken out.
// Expert rules are Go regular expressions (RE2): they run in linear time,
// so no pattern can freeze the server.
package spamfilter

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Match types (UnrealIRCd names).
const (
	Simple = "simple" // * and ?
	Regex  = "regex"  // Go RE2
)

// Targets: where a rule looks, as UnrealIRCd letters.
const (
	Channel       = 'c' // channel message
	Private       = 'p' // private message
	PrivateNotice = 'n' // private notice
	ChannelNotice = 'N' // channel notice
	Part          = 'P' // part reason
	Quit          = 'q' // quit reason
	Topic         = 't' // topic
	Away          = 'a' // away message
	User          = 'u' // nick!user@host:realname (connect and nick change)
)

// AllTargets lists the letters in the order lists show them.
const AllTargets = "cpnNPqtau"

// TargetWords are the plain names the Hive Panel and lists show.
var TargetWords = map[byte]string{
	Channel: "chan", Private: "private", PrivateNotice: "notice", ChannelNotice: "chan notice",
	Part: "part", Quit: "quit", Topic: "topic", Away: "away", User: "nick",
}

// Actions (UnrealIRCd names). Kline and Gline both end in an AKILL here,
// Zline and Gzline in a ZLINE: one network ban list.
const (
	Warn     = "warn"     // tell opers, let it through
	Block    = "block"    // drop it, tell the sender
	Kill     = "kill"     // disconnect
	TempShun = "tempshun" // shun until they reconnect
	Shun     = "shun"     // timed SHUN
	Kline    = "kline"
	Gline    = "gline" // timed AKILL
	Zline    = "zline"
	Gzline   = "gzline" // timed ZLINE
)

// Actions are the ones BeeIRCd carries out, mildest first.
var Actions = []string{Warn, Block, Kill, TempShun, Shun, Gline, Gzline}

// known are the actions we act on; others (viruschan, dccblock ...) from
// UnrealIRCd servers are kept and passed on, but do nothing here.
var known = map[string]bool{Warn: true, Block: true, Kill: true, TempShun: true, Shun: true, Kline: true, Gline: true, Zline: true, Gzline: true}

// Known reports whether action is one BeeIRCd carries out.
func Known(action string) bool { return known[action] }

// Bans reports whether action sets a timed network ban.
func Bans(action string) bool {
	switch action {
	case Shun, Kline, Gline, Zline, Gzline:
		return true
	}
	return false
}

// Limits.
const (
	MaxMatch   = 500 // bytes
	MaxReason  = 200
	MinSimple  = 4 // normal characters a simple match needs (no "*a*")
	MaxFilters = 1000
)

// Filter is one rule.
type Filter struct {
	ID      int64
	Type    string // Simple or Regex
	Match   string
	Targets string // letters, in AllTargets order
	Action  string
	BanTime time.Duration // for Bans(Action); 0 = until removed
	Reason  string
	SetBy   string
	SetAt   time.Time
	Expires time.Time // the rule itself; zero = never
}

// Live reports whether f is still in force at now.
func (f Filter) Live(now time.Time) bool { return f.Expires.IsZero() || now.Before(f.Expires) }

// Key is what makes a rule the same rule on every server (UnrealIRCd
// matches type, match, targets and action).
func (f Filter) Key() string {
	return f.Type + "\x00" + f.Targets + "\x00" + f.Action + "\x00" + f.Match
}

// Has reports whether f looks at target t.
func (f Filter) Has(t byte) bool { return strings.IndexByte(f.Targets, t) >= 0 }

// Words is f's targets in plain words.
func (f Filter) Words() []string {
	var out []string
	for i := 0; i < len(f.Targets); i++ {
		out = append(out, TargetWords[f.Targets[i]])
	}
	return out
}

// NormTargets checks target letters and puts them in AllTargets order.
func NormTargets(t string) (string, error) {
	if t == "" {
		return "", errors.New("say where to look: some of " + AllTargets)
	}
	var b strings.Builder
	for i := 0; i < len(AllTargets); i++ {
		if strings.IndexByte(t, AllTargets[i]) >= 0 {
			b.WriteByte(AllTargets[i])
		}
	}
	for i := 0; i < len(t); i++ {
		if strings.IndexByte(AllTargets, t[i]) < 0 {
			return "", fmt.Errorf("%q is not a target (use %s)", t[i], AllTargets)
		}
	}
	return b.String(), nil
}

// Check says what is wrong with f as a new rule from an oper, or nil.
// Rules from other servers are checked with Compile only.
func Check(f Filter) error {
	if f.Type != Simple && f.Type != Regex {
		return errors.New("the match type is simple or regex")
	}
	if _, err := NormTargets(f.Targets); err != nil {
		return err
	}
	if !Known(f.Action) {
		return fmt.Errorf("%q is not an action (use %s)", f.Action, strings.Join(Actions, ", "))
	}
	if len(f.Reason) > MaxReason {
		return fmt.Errorf("the reason is longer than %d characters", MaxReason)
	}
	if f.Type == Simple {
		n := 0
		for _, r := range f.Match {
			if r != '*' && r != '?' && r != ' ' {
				n++
			}
		}
		if n < MinSimple {
			return fmt.Errorf("a simple match needs at least %d normal characters", MinSimple)
		}
	}
	_, err := Compile(f)
	return err
}

// Rule is a compiled filter.
type Rule struct {
	Filter
	re   *regexp.Regexp
	glob string // lower-cased simple match
}

// Compile prepares f for matching.
func Compile(f Filter) (*Rule, error) {
	if f.Match == "" || len(f.Match) > MaxMatch || !utf8.ValidString(f.Match) || strings.ContainsAny(f.Match, "\r\n\x00") {
		return nil, fmt.Errorf("the match must be 1 to %d characters on one line", MaxMatch)
	}
	r := &Rule{Filter: f}
	switch f.Type {
	case Simple:
		r.glob = strings.ToLower(f.Match)
	case Regex:
		re, err := regexp.Compile("(?i)" + f.Match)
		if err != nil {
			return nil, fmt.Errorf("the regex doesn't work: %v", strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		}
		r.re = re
	default:
		return nil, errors.New("the match type is simple or regex")
	}
	return r, nil
}

// Catches reports whether r matches text (already Clean).
func (r *Rule) Catches(clean string) bool {
	if r.re != nil {
		return r.re.MatchString(clean)
	}
	return glob(r.glob, clean)
}

// Set is the compiled rules, by target.
type Set struct {
	by  map[byte][]*Rule
	all []*Rule
}

// NewSet compiles the live filters; rules that don't compile (a regex
// from another server our RE2 can't read) are skipped and returned.
func NewSet(fs []Filter, now time.Time) (*Set, []Filter) {
	s := &Set{by: map[byte][]*Rule{}}
	var bad []Filter
	sorted := append([]Filter(nil), fs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SetAt.Before(sorted[j].SetAt) })
	for _, f := range sorted {
		if !f.Live(now) {
			continue
		}
		r, err := Compile(f)
		if err != nil {
			bad = append(bad, f)
			continue
		}
		s.all = append(s.all, r)
		for i := 0; i < len(f.Targets); i++ {
			s.by[f.Targets[i]] = append(s.by[f.Targets[i]], r)
		}
	}
	return s, bad
}

// Empty reports whether no rule looks at target t (nothing to clean).
func (s *Set) Empty(t byte) bool { return s == nil || len(s.by[t]) == 0 }

// First is the oldest rule for target t that catches text, or nil.
func (s *Set) First(t byte, text string) *Rule {
	if s.Empty(t) {
		return nil
	}
	clean := Clean(text)
	for _, r := range s.by[t] {
		if r.Catches(clean) {
			return r
		}
	}
	return nil
}

// All is every rule that catches text for target t (the test command).
func (s *Set) All(t byte, text string) []*Rule {
	if s.Empty(t) {
		return nil
	}
	clean := Clean(text)
	var out []*Rule
	for _, r := range s.by[t] {
		if r.Catches(clean) {
			out = append(out, r)
		}
	}
	return out
}

// Clean lower-cases text and takes out colours and formatting, so a
// rule doesn't miss "f\x02ree" or coloured spam.
func Clean(text string) string {
	if strings.IndexFunc(text, func(r rune) bool { return r < ' ' }) < 0 {
		return strings.ToLower(text)
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case 0x03: // colour: up to 2 digits, optional ,2 digits
			i = skip(text, i+1, isDigit, 2)
		case 0x04: // hex colour
			i = skip(text, i+1, isHex, 6)
		case 0x01, 0x02, 0x0f, 0x11, 0x16, 0x1d, 0x1e, 0x1f:
		default:
			b.WriteByte(c)
		}
	}
	return strings.ToLower(b.String())
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHex(c byte) bool   { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// skip passes "fg[,bg]" from i and returns the index of the last byte used.
func skip(t string, i int, ok func(byte) bool, n int) int {
	j := i
	for j < len(t) && j-i < n && ok(t[j]) {
		j++
	}
	if j > i && j+1 < len(t) && t[j] == ',' && ok(t[j+1]) {
		k := j + 1
		for k < len(t) && k-(j+1) < n && ok(t[k]) {
			k++
		}
		j = k
	}
	return j - 1
}

// glob matches a lower-cased * ? pattern against the whole of s.
func glob(p, s string) bool {
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// UnrealIRCd writes the reason on the TKL F line (and in /SPAMFILTER)
// with _ for a space and __ for an underscore (unreal_encodespace).

// EncodeReason is a reason for the TKL F line.
func EncodeReason(r string) string {
	return strings.ReplaceAll(strings.ReplaceAll(r, "_", "__"), " ", "_")
}

// DecodeReason reads a TKL F reason.
func DecodeReason(r string) string {
	var b strings.Builder
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '_' && i+1 < len(r) && r[i+1] == '_':
			b.WriteByte('_')
			i++
		case r[i] == '_':
			b.WriteByte(' ')
		default:
			b.WriteByte(r[i])
		}
	}
	return b.String()
}

// Action letters on the TKL F line (UnrealIRCd banact table).
var actionLetters = map[string]byte{Kill: 'K', TempShun: 'S', Shun: 's', Kline: 'k', Zline: 'z', Gline: 'g',
	Gzline: 'Z', Block: 'b', Warn: 'w', "soft-kill": 'i', "soft-tempshun": 'T', "soft-shun": 'H', "soft-kline": 'I',
	"soft-gline": 'G', "soft-block": 'B', "dccblock": 'd', "soft-dccblock": 'D', "viruschan": 'v', "soft-viruschan": 'V', "soft-warn": 'W'}

// ActionLetter is action for the TKL F line (0 = none).
func ActionLetter(action string) byte { return actionLetters[action] }

// ActionOf reads a TKL F action letter ("" = unknown).
func ActionOf(letter byte) string {
	for a, l := range actionLetters {
		if l == letter {
			return a
		}
	}
	return ""
}
