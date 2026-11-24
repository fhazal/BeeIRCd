package config

import (
	"fmt"
	"strings"
	"time"
)

// ConnClass is a [[class]] block: a connect class (UnrealIRCd's class and
// allow, InspIRCd's connect and geoclass). People in one of its security
// groups get its limits; whatever it leaves unset comes from [limits].
// The first class in the file that takes someone in is theirs; nobody is
// in more than one. Everyone else is in the default class: [limits] as it
// is.
type ConnClass struct {
	Name string `toml:"name"`
	// SecurityGroup: who is in the class (in one of these groups).
	SecurityGroup []string `toml:"security_group"`
	// SessionLimit: connections per IP (OperServ session exceptions still
	// win). MaxUsers: the most people in this class at once.
	SessionLimit int      `toml:"session_limit"`
	MaxUsers     int      `toml:"max_users"`
	SendQ        int      `toml:"sendq"`
	PingInterval Duration `toml:"ping_interval"`
	PingTimeout  Duration `toml:"ping_timeout"`
	FloodBurst   int      `toml:"flood_burst"`
	FloodRate    int      `toml:"flood_rate"`
	MaxChannels  int      `toml:"max_channels"`
	// Deny: people in this class may not connect at all (a country block,
	// say); they get the normal ban text.
	Deny bool `toml:"deny"`
}

// DefaultClass is the name of the class of everyone no [[class]] takes in.
const DefaultClass = "default"

// ClassLimits are the limits of one class, [limits] filling in what the
// class leaves unset.
type ClassLimits struct {
	SessionLimit, MaxUsers, SendQ      int
	PingInterval, PingTimeout          time.Duration
	FloodBurst, FloodRate, MaxChannels int
}

// LimitsOf is what someone in class k gets; nil is the default class.
func (c *Config) LimitsOf(k *ConnClass) ClassLimits {
	l := c.Limits
	out := ClassLimits{
		SessionLimit: l.SessionLimit, MaxUsers: l.MaxUsers, SendQ: l.SendQ,
		PingInterval: l.PingInterval.Duration, PingTimeout: l.PingTimeout.Duration,
		FloodBurst: l.FloodBurst, FloodRate: l.FloodRate, MaxChannels: l.MaxChannels,
	}
	if k == nil {
		return out
	}
	pick := func(dst *int, v int) {
		if v > 0 {
			*dst = v
		}
	}
	pick(&out.SessionLimit, k.SessionLimit)
	pick(&out.MaxUsers, k.MaxUsers)
	pick(&out.SendQ, k.SendQ)
	pick(&out.FloodBurst, k.FloodBurst)
	pick(&out.FloodRate, k.FloodRate)
	pick(&out.MaxChannels, k.MaxChannels)
	if k.PingInterval.Duration > 0 {
		out.PingInterval = k.PingInterval.Duration
	}
	if k.PingTimeout.Duration > 0 {
		out.PingTimeout = k.PingTimeout.Duration
	}
	return out
}

// Class sizes a [[class]] may set: below these a person can't even get
// the welcome through, or is pinged all the time.
const (
	classMinSendQ = 8192
	classMinPing  = 10 * time.Second
)

func (c *Config) checkConnClasses() error {
	seen := map[string]bool{}
	for _, k := range c.ConnClasses {
		key := strings.ToLower(k.Name)
		switch {
		case !ValidGroupName(k.Name):
			return fmt.Errorf("[[class]] name %q must be a word of up to 30 letters, digits, _ or -", k.Name)
		case key == DefaultClass:
			return fmt.Errorf("[[class]] default is everyone no other class takes in, set by [limits]; give this class another name")
		case seen[key]:
			return fmt.Errorf("[[class]] %q is there twice", k.Name)
		case len(k.SecurityGroup) == 0:
			return fmt.Errorf("[[class]] %s takes in nobody: give it security_group, like [\"known-users\"]", k.Name)
		}
		seen[key] = true
		for _, g := range k.SecurityGroup {
			if !c.GroupKnown(g) {
				return fmt.Errorf("[[class]] %s: no security group %q (add a [[security_group]] block, or use a built-in one like known-users)", k.Name, g)
			}
		}
		for _, v := range []struct {
			name string
			n    int
		}{{"session_limit", k.SessionLimit}, {"max_users", k.MaxUsers}, {"sendq", k.SendQ},
			{"flood_burst", k.FloodBurst}, {"flood_rate", k.FloodRate}, {"max_channels", k.MaxChannels}} {
			if v.n < 0 {
				return fmt.Errorf("[[class]] %s: %s can't be below 0 (leave it out to use the [limits] value)", k.Name, v.name)
			}
		}
		if k.SendQ > 0 && k.SendQ < classMinSendQ {
			return fmt.Errorf("[[class]] %s: sendq is at least %d bytes", k.Name, classMinSendQ)
		}
		for _, d := range []struct {
			name string
			d    time.Duration
		}{{"ping_interval", k.PingInterval.Duration}, {"ping_timeout", k.PingTimeout.Duration}} {
			if d.d != 0 && d.d < classMinPing {
				return fmt.Errorf("[[class]] %s: %s is at least %s (leave it out to use the [limits] value)", k.Name, d.name, classMinPing)
			}
		}
	}
	return nil
}
