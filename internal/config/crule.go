package config

import (
	"fmt"
	"strings"

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
	toks, err := ruleTokens(text)
	if err != nil {
		return nil, err
	}
	p := &ruleParser{toks: toks}
	root, err := p.or(0)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tokEnd {
		return nil, fmt.Errorf("unexpected %q at character %d", t.text, t.pos+1)
	}
	return &LinkRule{text: text, root: root}, nil
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
)

// ruleTokens splits text into words (function names and masks) and signs.
func ruleTokens(text string) ([]ruleTok, error) {
	var toks []ruleTok
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t':
			i++
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
			for i < len(text) && !strings.ContainsRune(" \t&|!(),", rune(text[i])) && text[i] >= ' ' && text[i] != 0x7f {
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
		return p.call(t)
	}
	return nil, fmt.Errorf("expected connected(), directcon(), via() or directop() at character %d, got %q", t.pos+1, t.text)
}

// ruleWords maps each rule word to how many masks it takes.
var ruleWords = map[string]int{"connected": 1, "directcon": 1, "via": 2, "directop": 0}

// call reads name(mask, ...) after the name.
func (p *ruleParser) call(name ruleTok) (ruleNode, error) {
	word := strings.ToLower(name.text)
	want, ok := ruleWords[word]
	if !ok {
		return nil, fmt.Errorf("unknown word %q at character %d (use connected, directcon, via or directop)", name.text, name.pos+1)
	}
	if p.next().kind != tokOpen {
		return nil, fmt.Errorf("%s needs ( ) after it (character %d)", word, name.pos+1)
	}
	var args []string
	if p.peek().kind != tokClose {
		for {
			a := p.next()
			if a.kind != tokWord {
				return nil, fmt.Errorf("%s(): expected a server mask at character %d, got %q", word, a.pos+1, a.text)
			}
			args = append(args, a.text)
			if p.peek().kind != tokComma {
				break
			}
			p.next()
		}
	}
	if c := p.next(); c.kind != tokClose {
		return nil, fmt.Errorf("%s(): missing ) at character %d", word, c.pos+1)
	}
	if len(args) != want {
		switch want {
		case 0:
			return nil, fmt.Errorf("directop() takes no mask")
		case 1:
			return nil, fmt.Errorf("%s() takes one server mask", word)
		default:
			return nil, fmt.Errorf("via() takes two masks: via(through, server)")
		}
	}
	return ruleCall{word: word, args: args}, nil
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
)

func (r ruleAnd) eval(n RuleNet) bool { return r.a.eval(n) && r.b.eval(n) }
func (r ruleOr) eval(n RuleNet) bool  { return r.a.eval(n) || r.b.eval(n) }
func (r ruleNot) eval(n RuleNet) bool { return !r.a.eval(n) }

func (r ruleCall) eval(n RuleNet) bool {
	switch r.word {
	case "connected":
		return n.Connected(r.args[0])
	case "directcon":
		return n.DirectCon(r.args[0])
	case "via":
		return n.Via(r.args[0], r.args[1])
	}
	return n.DirectOp()
}

// RuleMatch reports whether server name matches mask, the way link rules
// compare them (glob, ignoring case).
func RuleMatch(mask, name string) bool { return irc.Match(mask, name) }
