package server

import "testing"

// Link rules: a rule that says no stops our autoconnect (and, with
// rule_for = "all", an incoming link); a rule that says yes lets it link.
func TestLinkRules(t *testing.T) {
	t.Run("autoconnect skipped", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		hub.expects(leaf, false, "")
		leaf.expects(hub, true, "")
		leaf.blocks[0].Rule = "connected(other.*)"
		hub.start(t)
		leaf.start(t)
		waitLink(t, "the rule notice", func() bool {
			return leaf.logHas("CONNECT: Not connecting to hub.test.local: link rule connected(other.*) says no")
		})
		if len(leaf.servers()) != 0 || hub.logHas("Verified server connection") {
			t.Errorf("leaf linked anyway: %v", leaf.servers())
		}
	})
	t.Run("autoconnect allowed", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		hub.expects(leaf, false, "")
		leaf.expects(hub, true, "")
		leaf.blocks[0].Rule = "!connected(other.*) && !directcon(hub.*)"
		hub.start(t)
		leaf.start(t)
		waitLink(t, "link up", func() bool { return leaf.servers()["0HB"] == "hub.test.local" })
		if leaf.logHas("link rule") {
			t.Errorf("rule notice for a rule that says yes:\n%s", leaf.log)
		}
	})
	t.Run("incoming refused with rule_for all", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		hub.expects(leaf, false, "")
		hub.blocks[0].Rule, hub.blocks[0].RuleFor = "directop()", "all" // no oper on the hub
		leaf.expects(hub, true, "")
		hub.start(t)
		leaf.start(t)
		waitLink(t, "refusal", func() bool {
			return hub.logHas("Server connection from leaf.test.local denied, link rule directop() says no")
		})
		if len(hub.servers()) != 0 {
			t.Errorf("hub linked anyway: %v", hub.servers())
		}
	})
	t.Run("incoming allowed with rule_for auto", func(t *testing.T) {
		hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
		leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
		hub.expects(leaf, false, "")
		hub.blocks[0].Rule, hub.blocks[0].RuleFor = "directop()", "auto" // only our own autoconnect
		leaf.expects(hub, true, "")
		hub.start(t)
		leaf.start(t)
		waitLink(t, "link up", func() bool { return hub.servers()["0LF"] == "leaf.test.local" })
	})
}

// A link that comes up another way (here: the other side dials in) clears
// "the rule said no", so the notice comes again after the next split and
// the Hive Panel doesn't show a stale "link rule says no".
func TestLinkRuleNoClearedOnLinkUp(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	leaf := newLinkNode(t, "leaf.test.local", "0LF", "leaf")
	hub.expects(leaf, true, "")
	hub.blocks[0].Rule = "directop()" // no oper on the hub: says no
	leaf.expects(hub, true, "")
	hub.start(t)
	waitLink(t, "the rule notice", func() bool {
		return hub.logHas("CONNECT: Not connecting to leaf.test.local: link rule directop() says no")
	})
	leaf.start(t)
	waitLink(t, "link up", func() bool { return hub.servers()["0LF"] == "leaf.test.local" })
	ruleNo := true
	hub.s.onLoop(func() {
		if r := hub.s.links.retry["leaf.test.local"]; r != nil {
			ruleNo = r.ruleNo
		}
	})
	if ruleNo {
		t.Error("ruleNo still set after the link came up")
	}
}
