package config

import (
	"strings"
	"testing"
)

// fakeNet is a network for rule tests: servers and the direct link each
// is reached through (itself for a direct link).
type fakeNet struct {
	via  map[string]string // server -> our direct link it is behind
	oper bool
}

func (f fakeNet) Connected(mask string) bool {
	for name := range f.via {
		if RuleMatch(mask, name) {
			return true
		}
	}
	return false
}

func (f fakeNet) DirectCon(mask string) bool {
	for name, v := range f.via {
		if name == v && RuleMatch(mask, name) {
			return true
		}
	}
	return false
}

func (f fakeNet) Via(via, mask string) bool {
	for name, v := range f.via {
		if RuleMatch(mask, name) && RuleMatch(via, v) {
			return true
		}
	}
	return false
}

func (f fakeNet) DirectOp() bool { return f.oper }

func TestParseLinkRuleErrors(t *testing.T) {
	for _, tc := range []struct{ rule, want string }{
		{"", "empty"},
		{"   ", "empty"},
		{"connected(", "expected a server mask"},
		{"connected(a", "missing )"},
		{"connected()", "takes one server mask"},
		{"connected(a,b)", "takes one server mask"},
		{"via(a)", "takes two masks"},
		{"directop(x)", "takes no mask"},
		{"directop", "needs ( )"},
		{"linked(a)", `unknown word "linked"`},
		{"connected(a) & connected(b)", "use &&"},
		{"connected(a) | connected(b)", "use ||"},
		{"connected(a) connected(b)", `unexpected "connected"`},
		{"(connected(a)", "missing ) for the ("},
		{"connected(a))", `unexpected ")"`},
		{"!", "expected connected()"},
		{"connected(a) &&", "expected connected()"},
		{strings.Repeat("!", 30) + "directop()", "nests deeper"},
		{strings.Repeat("x", 401), "longer than"},
		{"connected(a\x01)", "control character"},
	} {
		_, err := ParseLinkRule(tc.rule)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseLinkRule(%q) = %v, want an error with %q", tc.rule, err, tc.want)
		}
	}
}

func TestLinkRuleEval(t *testing.T) {
	// hub1 and leafA are linked to us; leafB sits behind hub1.
	net := fakeNet{via: map[string]string{
		"hub1.example.net":  "hub1.example.net",
		"leafa.example.net": "leafa.example.net",
		"leafb.example.net": "hub1.example.net",
	}}
	opNet := fakeNet{via: net.via, oper: true}
	empty := fakeNet{}
	for _, tc := range []struct {
		rule string
		n    fakeNet
		want bool
	}{
		{"connected(hub1.*)", net, true},
		{"connected(HUB1.EXAMPLE.NET)", net, true},
		{"connected(hub2.*)", net, false},
		{"!connected(hub1.*)", net, false},
		{"!connected(hub1.*)", empty, true},
		{"directcon(leafa.*)", net, true},
		{"directcon(leafb.*)", net, false},
		{"via(hub1.*, leafb.*)", net, true},
		{"via(leafa.*, leafb.*)", net, false},
		{"via(hub?.example.net, leafb.*)", net, true},
		{"directop()", net, false},
		{"directop()", opNet, true},
		{"DirectOp()", opNet, true},
		{"connected(hub1.*) && directop()", net, false},
		{"connected(hub1.*) || directop()", net, true},
		// && binds tighter than ||, ! tighter than both.
		{"connected(x) && connected(y) || connected(hub1.*)", net, true},
		{"connected(hub1.*) || connected(x) && connected(y)", net, true},
		{"(connected(hub1.*) || connected(x)) && connected(y)", net, false},
		{"!connected(x) && !connected(y)", net, true},
		{"!(connected(x) || connected(hub1.*))", net, false},
		{"!!directop()", opNet, true},
		{"  connected( hub1.* )  ", net, true},
	} {
		r, err := ParseLinkRule(tc.rule)
		if err != nil {
			t.Errorf("ParseLinkRule(%q): %v", tc.rule, err)
			continue
		}
		if got := r.Allows(tc.n); got != tc.want {
			t.Errorf("%q = %v, want %v", tc.rule, got, tc.want)
		}
	}
	if r, _ := ParseLinkRule("  !connected(hub1.*) "); r.String() != "!connected(hub1.*)" {
		t.Errorf("String() = %q", r.String())
	}
}

func TestLinkServerRuleCheck(t *testing.T) {
	b := LinkServer{Name: "hub2.example.net", Host: "hub2.example.net", Port: 7000, Autoconnect: true,
		Fingerprint: "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=", SendQMB: 20}
	if err := b.Check("leaf.example.net"); err != nil {
		t.Fatalf("plain block: %v", err)
	}
	b.Rule, b.RuleFor = "!connected(hub1.*)", "all"
	if err := b.Check("leaf.example.net"); err != nil {
		t.Errorf("good rule: %v", err)
	}
	b.RuleFor = "sometimes"
	if err := b.Check("leaf.example.net"); err == nil || !strings.Contains(err.Error(), "rule_for") {
		t.Errorf("bad rule_for: %v", err)
	}
	b.RuleFor, b.Rule = "auto", "connected(hub1.*"
	if err := b.Check("leaf.example.net"); err == nil || !strings.Contains(err.Error(), `rule "connected(hub1.*"`) {
		t.Errorf("bad rule: %v", err)
	}
}
