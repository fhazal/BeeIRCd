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
	return nil
}
