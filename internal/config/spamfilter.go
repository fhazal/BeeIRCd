package config

import (
	"fmt"
	"strings"
)

// SpamFilter is [spamfilter]. The rules themselves live in the services
// database (/SPAMFILTER, the Hive Panel), like the network bans.
type SpamFilter struct {
	// Except are channels whose messages, notices, parts and topics are
	// never filtered, like #help (UnrealIRCd set::spamfilter::except).
	Except []string `toml:"except"`
	// ExceptGroups are security groups whose people are never filtered,
	// like a group of one country (mask = ["~C:NL"]).
	ExceptGroups []string `toml:"except_groups"`
}

// SpamExcept reports whether channel is on the except list.
func (c *Config) SpamExcept(channel string) bool {
	for _, e := range c.SpamFilter.Except {
		if strings.EqualFold(e, channel) {
			return true
		}
	}
	return false
}

func (c *Config) checkSpamFilter() error {
	for _, e := range c.SpamFilter.Except {
		if !strings.HasPrefix(e, "#") || strings.ContainsAny(e, " ,") {
			return fmt.Errorf("[spamfilter] except: %q is not a channel name", e)
		}
	}
	for _, g := range c.SpamFilter.ExceptGroups {
		if !c.GroupKnown(g) {
			return fmt.Errorf("[spamfilter] except_groups: there is no group %q", g)
		}
	}
	return nil
}
