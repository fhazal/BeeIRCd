package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Reputation is [reputation]: a score per IP that grows while people from
// it chat (UnrealIRCd's reputation module). Security groups, the connect
// throttle and [[restrict]] use it to tell known users from new ones.
type Reputation struct {
	// Enabled: on by default.
	Enabled *bool `toml:"enabled"`
	// ChannelMembers: only people in a channel with at least this many
	// members earn points (3 by default; 0 = everyone).
	ChannelMembers *int `toml:"channel_members"`
}

// On reports whether scores are kept.
func (r Reputation) On() bool { return r.Enabled == nil || *r.Enabled }

// Members is channel_members with its default.
func (r Reputation) Members() int {
	if r.ChannelMembers == nil {
		return 3
	}
	return *r.ChannelMembers
}

// GroupMatch is who a security group takes in (or, under exclude, leaves
// out). Anyone matching one item is in.
type GroupMatch struct {
	Identified bool `toml:"identified"`
	WebIRC     bool `toml:"webirc"`
	WebSocket  bool `toml:"websocket"`
	TLS        bool `toml:"tls"`
	// ReputationScore: a score of at least this; ReputationBelow: less
	// than this.
	ReputationScore int `toml:"reputation_score"`
	ReputationBelow int `toml:"reputation_below"`
	// ConnectTime: connected at least this long; ConnectBelow: less.
	ConnectTime  Duration `toml:"connect_time"`
	ConnectBelow Duration `toml:"connect_below"`
	// Mask: nick!user@host masks or extbans, like "*@*.example.net" or
	// "~C:NL" (country) and "~a:*" (logged in).
	Mask []string `toml:"mask"`
	// IP: addresses or ranges, like "192.0.2.0/24".
	IP []string `toml:"ip"`
	// ServerPort: the port they connected to (people of this server).
	ServerPort []int `toml:"server_port"`
	// SecurityGroup: in one of these other groups.
	SecurityGroup []string `toml:"security_group"`
	// Rule: a user rule that comes out true, like UnrealIRCd's
	// "reputation() < 10 && !is_tls()" (see ParseUserRule).
	Rule string `toml:"rule"`
}

func (m GroupMatch) empty() bool {
	return !m.Identified && !m.WebIRC && !m.WebSocket && !m.TLS && m.ReputationScore == 0 && m.ReputationBelow == 0 &&
		m.ConnectTime.Duration == 0 && m.ConnectBelow.Duration == 0 && len(m.Mask) == 0 && len(m.IP) == 0 &&
		len(m.ServerPort) == 0 && len(m.SecurityGroup) == 0 && strings.TrimSpace(m.Rule) == ""
}

// UserRule is the parsed rule item, nil when there is none.
func (m GroupMatch) UserRule() *UserRule {
	if strings.TrimSpace(m.Rule) == "" {
		return nil
	}
	return CachedUserRule(m.Rule)
}

// SecurityGroup is a [[security_group]] block: a named set of people
// (UnrealIRCd's security-group). ~G:name bans and exceptions, the connect
// throttle, [[restrict]] and [spamfilter] use groups.
type SecurityGroup struct {
	Name string `toml:"name"`
	GroupMatch
	// Exclude: people matching any of these are never in, whatever else
	// matches.
	Exclude GroupMatch `toml:"exclude"`
}

// UnknownUsers is everyone not in known-users; it can't be changed.
const UnknownUsers = "unknown-users"

// BuiltinGroups are there without any block (a block with the same name
// replaces one; unknown-users is always everyone not in known-users).
var BuiltinGroups = []SecurityGroup{
	{Name: "webirc-users", GroupMatch: GroupMatch{WebIRC: true}},
	{Name: "websocket-users", GroupMatch: GroupMatch{WebSocket: true}},
	{Name: "known-users", GroupMatch: GroupMatch{Identified: true, ReputationScore: 25}},
	{Name: "tls-users", GroupMatch: GroupMatch{TLS: true}},
}

// Groups returns every security group, built-in ones first (in
// UnrealIRCd's order), then the rest in the file's order; unknown-users
// is not in the list.
func (c *Config) Groups() []SecurityGroup {
	var out []SecurityGroup
	used := map[string]bool{}
	for _, b := range BuiltinGroups {
		for _, g := range c.SecurityGroups {
			if strings.EqualFold(g.Name, b.Name) {
				b = g
				break
			}
		}
		used[strings.ToLower(b.Name)] = true
		out = append(out, b)
	}
	for _, g := range c.SecurityGroups {
		if !used[strings.ToLower(g.Name)] {
			out = append(out, g)
		}
	}
	return out
}

// GroupKnown reports whether name is a group (unknown-users included).
func (c *Config) GroupKnown(name string) bool {
	if strings.EqualFold(name, UnknownUsers) {
		return true
	}
	for _, g := range c.Groups() {
		if strings.EqualFold(g.Name, name) {
			return true
		}
	}
	return false
}

var groupNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,29}$`)

// ValidGroupName reports whether name can be a security group name.
func ValidGroupName(name string) bool { return groupNameRe.MatchString(name) }

func (c *Config) checkGroups() error {
	seen := map[string]bool{}
	for _, g := range c.SecurityGroups {
		key := strings.ToLower(g.Name)
		switch {
		case !ValidGroupName(g.Name):
			return fmt.Errorf("[[security_group]] name %q must be a word of up to 30 letters, digits, _ or -", g.Name)
		case key == UnknownUsers:
			return fmt.Errorf("[[security_group]] unknown-users is everyone not in known-users and can't be changed")
		case seen[key]:
			return fmt.Errorf("[[security_group]] %q is there twice", g.Name)
		case g.GroupMatch.empty():
			return fmt.Errorf("[[security_group]] %s takes in nobody: give it at least one item, like identified = true", g.Name)
		}
		seen[key] = true
		for _, m := range []GroupMatch{g.GroupMatch, g.Exclude} {
			if err := c.checkMatch(g.Name, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) checkMatch(name string, m GroupMatch) error {
	for _, v := range []int{m.ReputationScore, m.ReputationBelow} {
		if v < 0 || v > 10000 {
			return fmt.Errorf("[[security_group]] %s: reputation_score and reputation_below are 1 to 10000", name)
		}
	}
	if m.ConnectTime.Duration < 0 || m.ConnectBelow.Duration < 0 {
		return fmt.Errorf("[[security_group]] %s: connect_time can't be negative", name)
	}
	for _, mask := range m.Mask {
		if mask == "" || strings.ContainsAny(mask, " ,") {
			return fmt.Errorf("[[security_group]] %s: mask %q is not a mask", name, mask)
		}
	}
	for _, ip := range m.IP {
		if _, err := ParseCIDR(ip); err != nil {
			return fmt.Errorf("[[security_group]] %s: ip %q is not an address or range", name, ip)
		}
	}
	for _, p := range m.ServerPort {
		if p < 1 || p > 65535 {
			return fmt.Errorf("[[security_group]] %s: server_port %d is not a port", name, p)
		}
	}
	others := m.SecurityGroup
	if strings.TrimSpace(m.Rule) != "" {
		r, err := ParseUserRule(m.Rule)
		if err != nil {
			return fmt.Errorf("[[security_group]] %s: rule %q: %v", name, strings.TrimSpace(m.Rule), err)
		}
		others = append(slices.Clip(others), r.Groups()...)
	}
	for _, o := range others {
		switch {
		case strings.EqualFold(o, name):
			return fmt.Errorf("[[security_group]] %s can't be in itself", name)
		case !c.GroupKnown(o):
			return fmt.Errorf("[[security_group]] %s: there is no group %q", name, o)
		}
	}
	return nil
}

// ConnThrottle is [connthrottle]: a cap on how many new people (not in
// known-users) may connect per minute, so a bot flood from many IPs is
// slowed down (UnrealIRCd's connthrottle).
type ConnThrottle struct {
	// Enabled: on by default.
	Enabled *bool `toml:"enabled"`
	// Local: new people per period on this server ("20:60"); Global: on
	// the whole network ("30:60"). "off" turns one off.
	Local  string `toml:"local"`
	Global string `toml:"global"`
	// Except: groups that are never throttled (known-users by default).
	Except []string `toml:"except"`
	// StartDelay: off for this long after the server starts (3m), while
	// people reconnect.
	StartDelay Duration `toml:"start_delay"`
	// Gathering: off until reputation has been gathered this long (7d),
	// so known people are known first.
	Gathering Duration `toml:"gathering"`
	// Reason is what a throttled person is told.
	Reason string `toml:"reason"`
}

// On reports whether the throttle runs.
func (t ConnThrottle) On() bool { return t.Enabled == nil || *t.Enabled }

// Limits are the local and global rates (ok false = off).
func (t ConnThrottle) Limits() (ln int, lper time.Duration, lok bool, gn int, gper time.Duration, gok bool) {
	l, g := t.Local, t.Global
	if l == "" {
		l = "20:60"
	}
	if g == "" {
		g = "30:60"
	}
	ln, lper, lok = ParseRate(l)
	gn, gper, gok = ParseRate(g)
	return
}

// ExceptGroups is except with its default.
func (t ConnThrottle) ExceptGroups() []string {
	if t.Except == nil {
		return []string{"known-users"}
	}
	return t.Except
}

// Delay and GatherTime are start_delay and gathering with defaults.
func (t ConnThrottle) Delay() time.Duration {
	if t.StartDelay.Duration == 0 {
		return 3 * time.Minute
	}
	return t.StartDelay.Duration
}

func (t ConnThrottle) GatherTime() time.Duration {
	if t.Gathering.Duration == 0 {
		return 7 * 24 * time.Hour
	}
	return t.Gathering.Duration
}

// Message is reason with its default.
func (t ConnThrottle) Message() string {
	if t.Reason == "" {
		return "Throttled: Too many users trying to connect, please wait a while and try again"
	}
	return t.Reason
}

func (c *Config) checkConnThrottle() error {
	t := c.ConnThrottle
	for _, v := range []string{t.Local, t.Global} {
		if v == "" || v == "off" {
			continue
		}
		if _, _, ok := ParseRate(v); !ok {
			return fmt.Errorf("[connthrottle] %q must be count:seconds like 20:60, or off", v)
		}
	}
	for _, g := range t.Except {
		if !c.GroupKnown(g) {
			return fmt.Errorf("[connthrottle] except: there is no group %q", g)
		}
	}
	if len(t.Reason) > 200 {
		return fmt.Errorf("[connthrottle] reason is at most 200 characters")
	}
	return nil
}

// Restrict is a [[restrict]] block: a command new people may not use yet
// (UnrealIRCd's restrict-commands). Opers are never restricted.
type Restrict struct {
	// Command: a command (like "knock") or one of RestrictKinds.
	Command string `toml:"command"`
	// ConnectTime: allowed once connected this long. 0 = only people in
	// the except groups may use it.
	ConnectTime Duration `toml:"connect_time"`
	// Except: groups that may always use it (none by default; add
	// known-users to let identified and known people through).
	Except []string `toml:"except"`
}

// RestrictKinds are the names that cover more than one command.
var RestrictKinds = []string{"private-message", "private-notice", "channel-message", "channel-notice", "channel-create", "join", "list", "invite"}

var commandNameRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,19}$`)

func (c *Config) checkRestrict() error {
	seen := map[string]bool{}
	for _, r := range c.Restricts {
		cmd := strings.ToLower(r.Command)
		switch {
		case !slices.Contains(RestrictKinds, cmd) && !commandNameRe.MatchString(cmd):
			return fmt.Errorf("[[restrict]] command %q is not a command", r.Command)
		case seen[cmd]:
			return fmt.Errorf("[[restrict]] %s is there twice", cmd)
		case r.ConnectTime.Duration < 0 || r.ConnectTime.Duration > 24*time.Hour:
			return fmt.Errorf("[[restrict]] %s: connect_time is 0 to 24h", cmd)
		}
		if slices.Contains([]string{"cap", "nick", "user", "pass", "ping", "pong", "quit", "oper", "authenticate", "webirc"}, cmd) {
			return fmt.Errorf("[[restrict]] %s can't be restricted (it is needed to connect)", cmd)
		}
		seen[cmd] = true
		for _, g := range r.Except {
			if !c.GroupKnown(g) {
				return fmt.Errorf("[[restrict]] %s: there is no group %q", cmd, g)
			}
		}
	}
	return nil
}

// RestrictFor is the [[restrict]] block for command or kind, if any.
func (c *Config) RestrictFor(name string) (Restrict, bool) {
	for _, r := range c.Restricts {
		if strings.EqualFold(r.Command, name) {
			return r, true
		}
	}
	return Restrict{}, false
}

// DenyDCC is a [[deny_dcc]] block: DCC SEND of files with a matching name
// is refused (UnrealIRCd's deny dcc).
type DenyDCC struct {
	// Filename: a file name with * and ?, like "*.exe".
	Filename string `toml:"filename"`
	// Reason is shown to the sender.
	Reason string `toml:"reason"`
	// Soft: the receiver can still allow it from someone they trust with
	// /DCCALLOW +nick. Without it the file type is refused for everyone.
	Soft bool `toml:"soft"`
}

// AllowDCC is an [[allow_dcc]] block: an exception to the [[deny_dcc]]
// blocks of the same kind (soft or not), like "*.txt" under a deny of "*".
type AllowDCC struct {
	Filename string `toml:"filename"`
	Soft     bool   `toml:"soft"`
}

func (c *Config) checkDCC() error {
	for _, d := range c.DenyDCC {
		if strings.TrimSpace(d.Filename) == "" {
			return fmt.Errorf("[[deny_dcc]] needs a filename, like \"*.exe\"")
		}
	}
	for _, a := range c.AllowDCC {
		if strings.TrimSpace(a.Filename) == "" {
			return fmt.Errorf("[[allow_dcc]] needs a filename, like \"*.txt\"")
		}
	}
	return nil
}
