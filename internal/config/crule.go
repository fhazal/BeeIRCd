package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/fhazal/beeircd/internal/irc"
)

// A link rule (rule = on a [[link.server]] block) says when linking to that
// server is allowed, like UnrealIRCd's crule. It is made of these words:
//
//	connected(mask)       a server matching mask is on the network
//	directcon(mask)       a server matching mask is linked straight to us
//	via(viamask, mask)    a server matching mask is reached through our
//	                      direct link matching viamask
//	directop()            an oper is connected to this server
//
// joined with &&, ||, ! and ( ). Masks are server names with * and ?,
// ignoring case. A rule that comes out true lets the link go ahead.
//
// A user rule (rule = in a [[security_group]] block) uses the same parser
// with UnrealIRCd's user words on top (see userWords), 'quoted' arguments
// and number tests like reputation() < 10.

// RuleNet is what a link rule looks at: the network as it is now.
type RuleNet interface {
	Connected(mask string) bool
	DirectCon(mask string) bool
	Via(via, mask string) bool
	DirectOp() bool
}

// LinkRule is a parsed link rule.
type LinkRule struct {
	text string
	root ruleNode
}

// String is the rule as it was written (trimmed).
func (r *LinkRule) String() string { return r.text }

// Allows reports whether the rule lets the link go ahead on network n.
func (r *LinkRule) Allows(n RuleNet) bool { return r.root.eval(n) }

// Limits that keep a rule small: it runs before every autoconnect try.
const (
	maxRuleLen   = 400
	maxRuleDepth = 20
)

// ParseLinkRule reads a rule; the error says what is wrong in plain words.
func ParseLinkRule(text string) (*LinkRule, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return nil, fmt.Errorf("the rule is empty")
	case len(text) > maxRuleLen:
		return nil, fmt.Errorf("the rule is longer than %d characters", maxRuleLen)
	}
	root, err := parseRule(text, false)
	if err != nil {
		return nil, err
	}
	return &LinkRule{text: text, root: root}, nil
}

func parseRule(text string, user bool) (ruleNode, error) {
	toks, err := ruleTokens(text, user)
	if err != nil {
		return nil, err
	}
	p := &ruleParser{toks: toks, user: user}
	root, err := p.or(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEnd {
		return nil, fmt.Errorf("unexpected %q at character %d", t.text, t.pos+1)
	}
	return root, nil
}

// RuleUser is what a user rule looks at: one person, and the network
// (so the link words work in user rules too, as in UnrealIRCd).
type RuleUser interface {
	RuleNet
	// RuleValue is what user word (one of userWords) gives for the
	// person: 1 or 0 for yes/no words, the number for counting words.
	// arg is the word's argument ("" when it takes none).
	RuleValue(word, arg string) int
}

// UserRule is a parsed user rule.
type UserRule struct {
	text   string
	root   ruleNode
	groups []string
}

// String is the rule as it was written (trimmed).
func (r *UserRule) String() string { return r.text }

// Matches reports whether the rule comes out true for u.
func (r *UserRule) Matches(u RuleUser) bool { return r.root.eval(u) }

// Groups are the groups the rule names in in_security_group().
func (r *UserRule) Groups() []string { return r.groups }

// ParseUserRule reads a user rule; the error says what is wrong.
func ParseUserRule(text string) (*UserRule, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return nil, fmt.Errorf("the rule is empty")
	case len(text) > maxRuleLen:
		return nil, fmt.Errorf("the rule is longer than %d characters", maxRuleLen)
	}
	root, err := parseRule(text, true)
	if err != nil {
		return nil, err
	}
	r := &UserRule{text: text, root: root}
	walkCalls(root, func(c ruleCall) {
		if c.word == "in_security_group" {
			r.groups = append(r.groups, c.args[0])
		}
	})
	return r, nil
}

func walkCalls(n ruleNode, f func(ruleCall)) {
	switch n := n.(type) {
	case ruleAnd:
		walkCalls(n.a, f)
		walkCalls(n.b, f)
	case ruleOr:
		walkCalls(n.a, f)
		walkCalls(n.b, f)
	case ruleNot:
		walkCalls(n.a, f)
	case ruleCmp:
		f(n.call)
	case ruleCall:
		f(n)
	}
}

// userRules keeps parsed user rules by their text, so a group check (for
// everyone, once a minute) doesn't parse them again. Only rules from the
// config end up here, so it stays small; the cap is a safety net.
var userRules struct {
	sync.Mutex
	m map[string]*UserRule
}

// CachedUserRule is ParseUserRule(text) kept for next time; nil when text
// is empty or not a good rule (the config check refuses those).
func CachedUserRule(text string) *UserRule {
	userRules.Lock()
	defer userRules.Unlock()
	if r, ok := userRules.m[text]; ok {
		return r
	}
	r, err := ParseUserRule(text)
	if err != nil {
		r = nil
	}
	if userRules.m == nil || len(userRules.m) >= 256 {
		userRules.m = map[string]*UserRule{}
	}
	userRules.m[text] = r
	return r
}

// The pieces a rule is made of.
type ruleTok struct {
	kind tokKind
	text string
	pos  int // byte offset in the rule, for error messages
}

type tokKind int

const (
	tokEnd tokKind = iota
	tokWord
	tokAnd
	tokOr
	tokNot
	tokOpen
	tokClose
	tokComma
	tokCmp // <, <=, >, >=, ==, != (user rules)
)

// ruleTokens splits text into words (function names and masks) and signs.
// User rules also have comparisons and 'quoted' words.
func ruleTokens(text string, user bool) ([]ruleTok, error) {
	var toks []ruleTok
	stops := " \t&|!(),"
	if user {
		stops += "<>='"
	}
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case user && (strings.HasPrefix(text[i:], "<=") || strings.HasPrefix(text[i:], ">=") ||
			strings.HasPrefix(text[i:], "==") || strings.HasPrefix(text[i:], "!=")):
			toks = append(toks, ruleTok{tokCmp, text[i : i+2], i})
			i += 2
		case user && (c == '<' || c == '>'):
			toks = append(toks, ruleTok{tokCmp, text[i : i+1], i})
			i++
		case user && c == '=':
			return nil, fmt.Errorf("use == to compare, not a single = (character %d)", i+1)
		case user && c == '\'':
			end := strings.IndexByte(text[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("the ' at character %d is never closed", i+1)
			}
			word := text[i+1 : i+1+end]
			if strings.ContainsFunc(word, func(r rune) bool { return r < ' ' || r == 0x7f }) {
				return nil, fmt.Errorf("the rule has a control character")
			}
			toks = append(toks, ruleTok{tokWord, word, i})
			i += end + 2
		case strings.HasPrefix(text[i:], "&&"):
			toks = append(toks, ruleTok{tokAnd, "&&", i})
			i += 2
		case strings.HasPrefix(text[i:], "||"):
			toks = append(toks, ruleTok{tokOr, "||", i})
			i += 2
		case c == '!':
			toks = append(toks, ruleTok{tokNot, "!", i})
			i++
		case c == '(':
			toks = append(toks, ruleTok{tokOpen, "(", i})
			i++
		case c == ')':
			toks = append(toks, ruleTok{tokClose, ")", i})
			i++
		case c == ',':
			toks = append(toks, ruleTok{tokComma, ",", i})
			i++
		case c == '&' || c == '|':
			return nil, fmt.Errorf("use %c%c, not a single %c (character %d)", c, c, c, i+1)
		case c < ' ' || c == 0x7f:
			return nil, fmt.Errorf("the rule has a control character")
		default:
			start := i
			for i < len(text) && !strings.ContainsRune(stops, rune(text[i])) && text[i] >= ' ' && text[i] != 0x7f {
				i++
			}
			toks = append(toks, ruleTok{tokWord, text[start:i], start})
		}
	}
	return append(toks, ruleTok{tokEnd, "end of rule", len(text)}), nil
}

// ruleParser reads tokens: || binds loosest, then &&, then !.
type ruleParser struct {
	toks []ruleTok
	at   int
	user bool // a user rule: user words and comparisons too
}

func (p *ruleParser) peek() ruleTok { return p.toks[p.at] }

func (p *ruleParser) next() ruleTok {
	t := p.toks[p.at]
	if t.kind != tokEnd {
		p.at++
	}
	return t
}

func (p *ruleParser) or(depth int) (ruleNode, error) {
	left, err := p.and(depth)
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		p.next()
		right, err := p.and(depth)
		if err != nil {
			return nil, err
		}
		left = ruleOr{left, right}
	}
	return left, nil
}

func (p *ruleParser) and(depth int) (ruleNode, error) {
	left, err := p.unary(depth)
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokAnd {
		p.next()
		right, err := p.unary(depth)
		if err != nil {
			return nil, err
		}
		left = ruleAnd{left, right}
	}
	return left, nil
}

func (p *ruleParser) unary(depth int) (ruleNode, error) {
	if depth > maxRuleDepth {
		return nil, fmt.Errorf("the rule nests deeper than %d", maxRuleDepth)
	}
	t := p.next()
	switch t.kind {
	case tokNot:
		n, err := p.unary(depth + 1)
		if err != nil {
			return nil, err
		}
		return ruleNot{n}, nil
	case tokOpen:
		n, err := p.or(depth + 1)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tokClose {
			return nil, fmt.Errorf("missing ) for the ( at character %d", t.pos+1)
		}
		return n, nil
	case tokWord:
		c, err := p.call(t)
		if err != nil || !p.user || p.peek().kind != tokCmp {
			return c, err
		}
		op := p.next()
		v := p.next()
		n, err := strconv.Atoi(v.text)
		if v.kind != tokWord || err != nil {
			return nil, fmt.Errorf("%s(): expected a number after %s at character %d, got %q", c.word, op.text, v.pos+1, v.text)
		}
		return ruleCmp{call: c, op: op.text, value: n}, nil
	}
	if p.user {
		return nil, fmt.Errorf("expected a word like is_identified() or reputation() at character %d, got %q", t.pos+1, t.text)
	}
	return nil, fmt.Errorf("expected connected(), directcon(), via() or directop() at character %d, got %q", t.pos+1, t.text)
}

// ruleWords maps each rule word to how many masks it takes.
var ruleWords = map[string]int{"connected": 1, "directcon": 1, "via": 2, "directop": 0}

// userWords are the words of user rules (UnrealIRCd's names) and how many
// arguments each takes. Yes/no words give 1 or 0, the others a number;
// any word can be compared (reputation() < 10, is_tls() == 0).
var userWords = map[string]int{
	"is_identified": 0, "is_webirc": 0, "is_websocket": 0, "is_tls": 0,
	"is_oper": 0, "is_away": 0, "is_local": 0, "has_swhois": 0,
	"reputation": 0, "online_time": 0, "server_port": 0, "channel_count": 0,
	"in_security_group": 1, "in_channel": 1, "has_user_mode": 1, "cap_set": 1,
	"match_mask": 1, "match_ip": 1, "match_account": 1, "match_country": 1,
	"match_asn": 1, "match_asname": 1, "match_certfp": 1, "match_realname": 1,
	"match_realhost": 1, "match_vhost": 1, "match_server": 1, "match_away": 1,
	"match_operlogin": 1, "match_operclass": 1,
}

// UserRuleWords lists the user words in order, for docs and help.
func UserRuleWords() []string {
	out := make([]string, 0, len(userWords))
	for w := range userWords {
		out = append(out, w)
	}
	slices.Sort(out)
	return out
}

// call reads name(mask, ...) after the name.
func (p *ruleParser) call(name ruleTok) (ruleCall, error) {
	word := strings.ToLower(name.text)
	want, ok := ruleWords[word]
	if !ok && p.user {
		want, ok = userWords[word]
		if !ok {
			return ruleCall{}, fmt.Errorf("unknown word %q at character %d", name.text, name.pos+1)
		}
	}
	if !ok {
		return ruleCall{}, fmt.Errorf("unknown word %q at character %d (use connected, directcon, via or directop)", name.text, name.pos+1)
	}
	if p.next().kind != tokOpen {
		return ruleCall{}, fmt.Errorf("%s needs ( ) after it (character %d)", word, name.pos+1)
	}
	var args []string
	if p.peek().kind != tokClose {
		for {
			a := p.next()
			if a.kind != tokWord {
				what := "a server mask"
				if _, u := userWords[word]; u {
					what = "an argument"
				}
				return ruleCall{}, fmt.Errorf("%s(): expected %s at character %d, got %q", word, what, a.pos+1, a.text)
			}
			args = append(args, a.text)
			if p.peek().kind != tokComma {
				break
			}
			p.next()
		}
	}
	if c := p.next(); c.kind != tokClose {
		if p.user && c.kind == tokNot {
			return ruleCall{}, fmt.Errorf("%s(): put a mask with ! in 'quotes' (character %d)", word, c.pos+1)
		}
		return ruleCall{}, fmt.Errorf("%s(): missing ) at character %d", word, c.pos+1)
	}
	if _, u := userWords[word]; u {
		switch {
		case len(args) != want && want == 0:
			return ruleCall{}, fmt.Errorf("%s() takes nothing between the ( )", word)
		case len(args) != want:
			return ruleCall{}, fmt.Errorf("%s() takes one argument", word)
		}
		if err := checkUserArg(word, args); err != nil {
			return ruleCall{}, err
		}
		return ruleCall{word: word, args: args}, nil
	}
	if len(args) != want {
		switch want {
		case 0:
			return ruleCall{}, fmt.Errorf("directop() takes no mask")
		case 1:
			return ruleCall{}, fmt.Errorf("%s() takes one server mask", word)
		default:
			return ruleCall{}, fmt.Errorf("via() takes two masks: via(through, server)")
		}
	}
	return ruleCall{word: word, args: args}, nil
}

// checkUserArg catches arguments that can never match.
func checkUserArg(word string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	a := args[0]
	switch word {
	case "in_security_group":
		if !ValidGroupName(a) && !strings.EqualFold(a, UnknownUsers) {
			return fmt.Errorf("in_security_group(): %q is not a group name", a)
		}
	case "has_user_mode":
		for _, r := range a {
			if r > 0x7f || !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z') {
				return fmt.Errorf("has_user_mode(): %q is not user mode letters, like 'x' or 'iw'", a)
			}
		}
	case "match_asn":
		if _, err := strconv.ParseUint(a, 10, 32); err != nil {
			return fmt.Errorf("match_asn(): %q is not an AS number, like 64496", a)
		}
	case "in_channel":
		if !strings.Contains(a, "#") {
			return fmt.Errorf("in_channel(): %q is not a channel, like '#help' or '@#help'", a)
		}
	}
	if a == "" {
		return fmt.Errorf("%s(): the argument is empty", word)
	}
	return nil
}

// ruleNode is one part of a parsed rule.
type ruleNode interface{ eval(RuleNet) bool }

type (
	ruleAnd  struct{ a, b ruleNode }
	ruleOr   struct{ a, b ruleNode }
	ruleNot  struct{ a ruleNode }
	ruleCall struct {
		word string
		args []string
	}
	// ruleCmp is call op value, like reputation() < 10.
	ruleCmp struct {
		call  ruleCall
		op    string
		value int
	}
)

func (r ruleAnd) eval(n RuleNet) bool { return r.a.eval(n) && r.b.eval(n) }
func (r ruleOr) eval(n RuleNet) bool  { return r.a.eval(n) || r.b.eval(n) }
func (r ruleNot) eval(n RuleNet) bool { return !r.a.eval(n) }

func (r ruleCall) eval(n RuleNet) bool { return r.value(n) != 0 }

func (r ruleCmp) eval(n RuleNet) bool {
	v := r.call.value(n)
	switch r.op {
	case "<":
		return v < r.value
	case "<=":
		return v <= r.value
	case ">":
		return v > r.value
	case ">=":
		return v >= r.value
	case "==":
		return v == r.value
	}
	return v != r.value
}

func yes(b bool) int {
	if b {
		return 1
	}
	return 0
}

// value is what the word gives: 1 or 0 for the link words.
func (r ruleCall) value(n RuleNet) int {
	switch r.word {
	case "connected":
		return yes(n.Connected(r.args[0]))
	case "directcon":
		return yes(n.DirectCon(r.args[0]))
	case "via":
		return yes(n.Via(r.args[0], r.args[1]))
	case "directop":
		return yes(n.DirectOp())
	}
	if u, ok := n.(RuleUser); ok {
		arg := ""
		if len(r.args) > 0 {
			arg = r.args[0]
		}
		return u.RuleValue(r.word, arg)
	}
	return 0
}

// RuleMatch reports whether server name matches mask, the way link rules
// compare them (glob, ignoring case).
func RuleMatch(mask, name string) bool { return irc.Match(mask, name) }
