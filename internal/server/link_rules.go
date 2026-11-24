package server

import (
	"github.com/fhazal/beeircd/internal/config"
)

// ruleNet is the network as a link rule sees it (config.RuleNet): every
// server we know of except ourselves, with built-in services counting as
// a server linked straight to us.
type ruleNet struct{ s *Server }

func (n ruleNet) Connected(mask string) bool {
	if si, ok := n.s.servicesInfo(); ok && config.RuleMatch(mask, si.Name) {
		return true
	}
	for _, r := range n.s.links.servers {
		if config.RuleMatch(mask, r.name) {
			return true
		}
	}
	return false
}

func (n ruleNet) DirectCon(mask string) bool {
	if si, ok := n.s.servicesInfo(); ok && config.RuleMatch(mask, si.Name) {
		return true
	}
	for _, r := range n.s.links.servers {
		if r.hops == 1 && config.RuleMatch(mask, r.name) {
			return true
		}
	}
	return false
}

// Via: a server matching mask is behind our direct link matching via (a
// server linked to us is reached through itself).
func (n ruleNet) Via(via, mask string) bool {
	for _, r := range n.s.links.servers {
		if r.via != nil && config.RuleMatch(mask, r.name) && config.RuleMatch(via, r.via.peer.Name) {
			return true
		}
	}
	return false
}

func (n ruleNet) DirectOp() bool { return len(n.s.opers) > 0 }

// linkRuleSaysNo reports whether block b's link rule refuses linking now;
// rule is the rule's text for the notice. A block without a rule (or with
// one that no longer parses, which the config check stops) never refuses.
func (s *Server) linkRuleSaysNo(b config.LinkServer) (rule string, no bool) {
	r, err := b.LinkRule()
	if err != nil || r == nil {
		return "", false
	}
	return r.String(), !r.Allows(ruleNet{s})
}
