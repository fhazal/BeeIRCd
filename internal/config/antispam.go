package config

import (
	"fmt"
	"time"
)

// AntiRandom is [antirandom]: people whose nick, username and real name
// look like random letters are refused at connect (UnrealIRCd's
// antirandom). Off by default.
type AntiRandom struct {
	Enabled bool `toml:"enabled"`
	// Threshold: the score from which someone is refused (7). Lower
	// catches more and makes more mistakes.
	Threshold int `toml:"threshold"`
	// Action: "kill" refuses the connection (the default), "warn" only
	// tells the opers.
	Action string `toml:"action"`
	// Reason is what a refused person is told.
	Reason string `toml:"reason"`
	// Except: groups never checked (known-users and webirc-users by
	// default).
	Except []string `toml:"except"`
}

// Limit is threshold with its default.
func (a AntiRandom) Limit() int {
	if a.Threshold == 0 {
		return 7
	}
	return a.Threshold
}

// Warn reports whether a hit only warns the opers.
func (a AntiRandom) Warn() bool { return a.Action == "warn" }

// Message is reason with its default.
func (a AntiRandom) Message() string {
	if a.Reason == "" {
		return "You look like a bot. Be sure to fill in your nick/ident/realname properly."
	}
	return a.Reason
}

// ExceptGroups is except with its default.
func (a AntiRandom) ExceptGroups() []string {
	if a.Except == nil {
		return []string{"known-users", "webirc-users"}
	}
	return a.Except
}

// AntiMixedUTF8 is [antimixedutf8]: messages that mix alphabets inside
// words, like Latin with look-alike Cyrillic letters to get past filters,
// are refused (UnrealIRCd's antimixedutf8). Off by default.
type AntiMixedUTF8 struct {
	Enabled bool `toml:"enabled"`
	// Score: refused from this score (10). A change of alphabet counts 1,
	// and 1 more inside a word.
	Score int `toml:"score"`
	// Reason is what the sender is told.
	Reason string `toml:"reason"`
	// Except: groups never checked (none by default).
	Except []string `toml:"except"`
}

// Limit is score with its default.
func (a AntiMixedUTF8) Limit() int {
	if a.Score == 0 {
		return 10
	}
	return a.Score
}

// Message is reason with its default.
func (a AntiMixedUTF8) Message() string {
	if a.Reason == "" {
		return "Possible mixed character spam"
	}
	return a.Reason
}

// TargetFlood is [targetfloodprot]: a cap on how many messages one
// channel or person can be sent in a short time, by everyone together
// (UnrealIRCd's targetfloodprot). What goes over it is not delivered. On
// by default, like UnrealIRCd. Each limit is "messages:seconds".
type TargetFlood struct {
	Enabled        *bool  `toml:"enabled"` // nil = on
	ChannelPrivmsg string `toml:"channel_privmsg"`
	ChannelNotice  string `toml:"channel_notice"`
	ChannelTagmsg  string `toml:"channel_tagmsg"`
	PrivatePrivmsg string `toml:"private_privmsg"`
	PrivateNotice  string `toml:"private_notice"`
	PrivateTagmsg  string `toml:"private_tagmsg"`
}

// On reports whether the cap is used: unless enabled = false.
func (t TargetFlood) On() bool { return t.Enabled == nil || *t.Enabled }

// Limit is the cap for verb (PRIVMSG, NOTICE, TAGMSG) to a channel or a
// person; UnrealIRCd's numbers when a setting is empty.
func (t TargetFlood) Limit(toChannel bool, verb string) (int, time.Duration) {
	v, def := "", ""
	switch {
	case toChannel && verb == "PRIVMSG":
		v, def = t.ChannelPrivmsg, "45:5"
	case toChannel && verb == "NOTICE":
		v, def = t.ChannelNotice, "15:5"
	case toChannel:
		v, def = t.ChannelTagmsg, "15:5"
	case verb == "PRIVMSG":
		v, def = t.PrivatePrivmsg, "30:5"
	case verb == "NOTICE":
		v, def = t.PrivateNotice, "10:5"
	default:
		v, def = t.PrivateTagmsg, "10:5"
	}
	if v == "" {
		v = def
	}
	n, per, _ := ParseRate(v)
	return n, per
}

// BlockAmsg is [blockamsg]: the same text sent to several channels at
// once (/amsg, /ame) only reaches the first one (InspIRCd's blockamsg).
// Off by default.
type BlockAmsg struct {
	Enabled bool `toml:"enabled"`
	// Delay: the same text to another channel within this time counts as
	// one /amsg (200ms).
	Delay Duration `toml:"delay"`
}

// Within is delay with its default.
func (b BlockAmsg) Within() time.Duration {
	if b.Delay.Duration == 0 {
		return 200 * time.Millisecond
	}
	return b.Delay.Duration
}

func (c *Config) checkAntiSpam() error {
	a := c.AntiRandom
	switch {
	case a.Threshold < 0 || a.Threshold > 100:
		return fmt.Errorf("[antirandom] threshold must be between 1 and 100")
	case a.Action != "" && a.Action != "kill" && a.Action != "warn":
		return fmt.Errorf("[antirandom] action %q: use \"kill\" or \"warn\"", a.Action)
	case len(a.Reason) > 200:
		return fmt.Errorf("[antirandom] reason is at most 200 characters")
	}
	for _, g := range a.Except {
		if !c.GroupKnown(g) {
			return fmt.Errorf("[antirandom] except: there is no group %q", g)
		}
	}
	m := c.AntiMixedUTF8
	switch {
	case m.Score < 0 || m.Score > 1000:
		return fmt.Errorf("[antimixedutf8] score must be between 1 and 1000")
	case len(m.Reason) > 200:
		return fmt.Errorf("[antimixedutf8] reason is at most 200 characters")
	}
	for _, g := range m.Except {
		if !c.GroupKnown(g) {
			return fmt.Errorf("[antimixedutf8] except: there is no group %q", g)
		}
	}
	t := c.TargetFlood
	for _, v := range []string{t.ChannelPrivmsg, t.ChannelNotice, t.ChannelTagmsg, t.PrivatePrivmsg, t.PrivateNotice, t.PrivateTagmsg} {
		if v == "" {
			continue
		}
		if _, _, ok := ParseRate(v); !ok {
			return fmt.Errorf("[targetfloodprot] %q must be messages:seconds like 45:5", v)
		}
	}
	if d := c.BlockAmsg.Delay.Duration; d < 0 || d > 10*time.Second {
		return fmt.Errorf("[blockamsg] delay must be between 0 and 10s")
	}
	return nil
}
