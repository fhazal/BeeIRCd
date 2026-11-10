// Package config reads and checks beeircd.conf (TOML).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration written as "90s", "5m" in the file.
type Duration struct{ time.Duration }

// UnmarshalText reads "90s" and the like.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("%q is not a time like 90s or 5m", string(b))
	}
	d.Duration = v
	return nil
}

// Config is the whole file.
type Config struct {
	Server Server   `toml:"server"`
	Listen []Listen `toml:"listen"`
	TLS    TLS      `toml:"tls"`
	MOTD   MOTD     `toml:"motd"`
	Limits Limits   `toml:"limits"`
}

// Server names this server and its network.
type Server struct {
	Name        string `toml:"name"`
	Network     string `toml:"network"`
	Description string `toml:"description"`
	// SID is the 3-character server ID used for linking, like "0BE".
	SID string `toml:"sid"`
	// ResolveHosts looks up hostnames for connecting IPs. On by default.
	ResolveHosts *bool `toml:"resolve_hosts"`
}

// Listen is one port.
type Listen struct {
	Address string `toml:"address"`
	TLS     bool   `toml:"tls"`
}

// TLS points at the certificate. With SelfSigned, missing files are made.
type TLS struct {
	Cert       string `toml:"cert"`
	Key        string `toml:"key"`
	SelfSigned bool   `toml:"self_signed"`
}

// MOTD is read from File; Text is used when there is no file.
type MOTD struct {
	File string `toml:"file"`
	Text string `toml:"text"`
}

// Limits holds timeouts and sizes. Zero means the default.
type Limits struct {
	PingInterval    Duration `toml:"ping_interval"`
	PingTimeout     Duration `toml:"ping_timeout"`
	RegisterTimeout Duration `toml:"register_timeout"`
	SendQ           int      `toml:"sendq"`
	MaxChannels     int      `toml:"max_channels"`
	NickLen         int      `toml:"nick_length"`
	ChannelLen      int      `toml:"channel_length"`
	TopicLen        int      `toml:"topic_length"`
	AwayLen         int      `toml:"away_length"`
	KickLen         int      `toml:"kick_length"`
}

// ResolveHosts reports whether hostname lookups are on.
func (c *Config) ResolveHosts() bool {
	return c.Server.ResolveHosts == nil || *c.Server.ResolveHosts
}

// NeedsTLS reports whether any port uses TLS.
func (c *Config) NeedsTLS() bool {
	for _, l := range c.Listen {
		if l.TLS {
			return true
		}
	}
	return false
}

// Load reads, fills in defaults and checks the file at path. Relative paths
// inside it are taken from the file's folder.
func Load(path string) (*Config, error) {
	var c Config
	meta, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if extra := meta.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown setting %s", path, strings.Join(keys, ", "))
	}
	dir := filepath.Dir(path)
	for _, p := range []*string{&c.TLS.Cert, &c.TLS.Key, &c.MOTD.File} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(dir, *p)
		}
	}
	c.SetDefaults()
	if err := c.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// SetDefaults fills in every value left empty.
func (c *Config) SetDefaults() {
	if c.Server.Description == "" {
		c.Server.Description = c.Server.Network + " IRC"
	}
	l := &c.Limits
	setDur(&l.PingInterval, 90*time.Second)
	setDur(&l.PingTimeout, 90*time.Second)
	setDur(&l.RegisterTimeout, 60*time.Second)
	setInt(&l.SendQ, 1<<20)
	setInt(&l.MaxChannels, 30)
	setInt(&l.NickLen, 30)
	setInt(&l.ChannelLen, 50)
	setInt(&l.TopicLen, 390)
	setInt(&l.AwayLen, 200)
	setInt(&l.KickLen, 390)
}

func setDur(d *Duration, v time.Duration) {
	if d.Duration <= 0 {
		d.Duration = v
	}
}

func setInt(i *int, v int) {
	if *i <= 0 {
		*i = v
	}
}

var (
	sidRe     = regexp.MustCompile(`^[0-9][A-Z0-9]{2}$`)
	serverRe  = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
	networkRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Check reports the first thing wrong, in words a person can act on.
func (c *Config) Check() error {
	switch {
	case !serverRe.MatchString(c.Server.Name) || len(c.Server.Name) > 63:
		return fmt.Errorf("server name %q must look like irc.example.org", c.Server.Name)
	case !networkRe.MatchString(c.Server.Network):
		return fmt.Errorf("network name %q must be one word (letters, digits, dot, dash or underscore)", c.Server.Network)
	case !sidRe.MatchString(c.Server.SID):
		return fmt.Errorf("server sid %q must be a digit and two capital letters or digits, like 0BE", c.Server.SID)
	case len(c.Listen) == 0:
		return fmt.Errorf("add at least one [[listen]] port")
	}
	for _, l := range c.Listen {
		if l.Address == "" {
			return fmt.Errorf("a [[listen]] entry has no address")
		}
	}
	if c.NeedsTLS() && (c.TLS.Cert == "" || c.TLS.Key == "") {
		return fmt.Errorf("a TLS port needs [tls] cert and key")
	}
	if c.Limits.NickLen > 50 || c.Limits.ChannelLen > 64 {
		return fmt.Errorf("nick_length is at most 50 and channel_length at most 64")
	}
	return nil
}

// MOTDLines returns the message of the day, read fresh from the file.
func (c *Config) MOTDLines() []string {
	text := c.MOTD.Text
	if c.MOTD.File != "" {
		if b, err := os.ReadFile(c.MOTD.File); err == nil {
			text = string(b)
		}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
