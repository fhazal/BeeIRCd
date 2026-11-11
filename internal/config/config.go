// Package config reads and checks beeircd.conf (TOML).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is a time.Duration written as "90s", "5m" in the file.
type Duration struct{ time.Duration }

// UnmarshalText reads "90s" and the like.
func (d *Duration) UnmarshalText(b []byte) error {
	s := string(b)
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return fmt.Errorf("%q is not a time like 90d, 90s or 5m", s)
		}
		d.Duration = time.Duration(n) * 24 * time.Hour
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a time like 90d, 90s or 5m", s)
	}
	d.Duration = v
	return nil
}

// Config is the whole file.
type Config struct {
	Server   Server   `toml:"server"`
	Listen   []Listen `toml:"listen"`
	TLS      TLS      `toml:"tls"`
	MOTD     MOTD     `toml:"motd"`
	Cloak    Cloak    `toml:"cloak"`
	Services Services `toml:"services"`
	Limits   Limits   `toml:"limits"`
	DNSBL    []DNSBL  `toml:"dnsbl"`
	Mail     Mail     `toml:"mail"`
	HostServ HostServ `toml:"hostserv"`
	Channels Channels `toml:"channels"`
	History  History  `toml:"history"`
	// OperClasses are the [[operclass]] blocks (see Classes).
	OperClasses []OperClass `toml:"operclass"`

	// Path is the file this was read from; AdminServ writes changes there.
	Path string `toml:"-"`
}

// DNSBL is one DNS blocklist new connections are looked up in, like
// UnrealIRCd's blacklist{} and Anope's m_dnsbl.
type DNSBL struct {
	// Name is the list's DNS zone, like "dnsbl.dronebl.org".
	Name string `toml:"name"`
	// Replies are the answers (127.0.0.N) that count; empty = every answer.
	Replies []int `toml:"replies"`
	// Action: "reject" (refuse this connection, the default), "zline",
	// "akill" or "shun" (a network ban for ban_time).
	Action string `toml:"action"`
	// BanTime is how long a zline/akill/shun lasts (24h by default).
	BanTime Duration `toml:"ban_time"`
	// Reason is shown to the person; %i is their IP, %b the list, %r the
	// answer, %n %u %h their nick, ident and host, %N the network.
	Reason string `toml:"reason"`
	// Enabled is on by default.
	Enabled *bool `toml:"enabled"`
}

// DNSBLActions are the actions a [[dnsbl]] list may take.
var DNSBLActions = []string{"reject", "zline", "akill", "shun"}

// Cloak hides people's IPs and hostnames (user mode +x).
type Cloak struct {
	// Enabled is on by default.
	Enabled *bool `toml:"enabled"`
	// Secret makes cloaks impossible to reverse. Every linked server
	// needs the same one, and changing it changes everyone's cloak.
	Secret string `toml:"secret"`
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
	// Prefixes is the channel status style: "@+" (op and voice, the
	// default) or "~&@%+" (owner, admin, op, halfop, voice). Changing it
	// needs a restart.
	Prefixes string `toml:"prefixes"`
}

// FullPrefixes reports whether owner, admin and halfop exist.
func (c *Config) FullPrefixes() bool { return c.Server.Prefixes == "~&@%+" }

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
	// FloodBurst lines go through at once; after that FloodRate lines per
	// second. Faster senders are slowed down, not disconnected.
	FloodBurst int `toml:"flood_burst"`
	FloodRate  int `toml:"flood_rate"`
	// MaxUsers is the most people connected at once.
	MaxUsers int `toml:"max_users"`
	// SessionLimit is the most connections from one IP, unless an
	// OperServ session exception allows more.
	SessionLimit int `toml:"session_limit"`
}

// CloakEnabled reports whether hosts are cloaked.
func (c *Config) CloakEnabled() bool { return c.Cloak.Enabled == nil || *c.Cloak.Enabled }

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
	for _, p := range []*string{&c.TLS.Cert, &c.TLS.Key, &c.MOTD.File, &c.Services.Database, &c.History.File} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(dir, *p)
		}
	}
	c.SetDefaults()
	if err := c.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	return &c, nil
}

// SetDefaults fills in every value left empty.
func (c *Config) SetDefaults() {
	if c.Server.Description == "" {
		c.Server.Description = c.Server.Network + " IRC"
	}
	if c.Server.Prefixes == "" {
		c.Server.Prefixes = "@+"
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
	setInt(&l.FloodBurst, 10)
	setInt(&l.FloodRate, 2)
	setInt(&l.MaxUsers, 5000)
	setInt(&l.SessionLimit, 3)
	sv := &c.Services
	if sv.Database == "" {
		sv.Database = "/var/lib/beeircd/services.db"
	}
	setDur(&sv.NickExpire, 90*24*time.Hour)
	setDur(&sv.IdentifyWithin, 60*time.Second)
	setDur(&sv.KillQuick, 20*time.Second)
	setDur(&sv.BanExpiry, 30*24*time.Hour)
	setDur(&sv.ChanExpire, 60*24*time.Hour)
	setInt(&sv.MaxLogins, 10)
	setInt(&sv.CertMax, 5)
	setInt(&sv.AjoinMax, 10)
	setInt(&sv.ListMax, 50)
	setInt(&sv.MaxMemos, 20)
	c.Mail.setDefaults()
	c.HostServ.setDefaults()
	c.Channels.setDefaults()
	c.History.setDefaults(sv.Database)
	if sv.GuestPrefix == "" {
		sv.GuestPrefix = "Guest"
	}
	for i := range c.DNSBL {
		d := &c.DNSBL[i]
		if d.Action == "" {
			d.Action = "reject"
		}
		setDur(&d.BanTime, 24*time.Hour)
		if d.Reason == "" {
			d.Reason = "Your IP %i is listed in %b (%r)"
		}
	}
}

// Services holds NickServ, ChanServ and MemoServ settings.
type Services struct {
	// Database is the SQLite file with accounts, nicks and memos.
	Database string `toml:"database"`
	// NickExpire drops accounts nobody identified to for this long.
	NickExpire Duration `toml:"nick_expire"`
	// ChanExpire drops channels no access-list member joined for this long.
	ChanExpire Duration `toml:"chan_expire"`
	// IdentifyWithin is how long someone on a protected nick has.
	IdentifyWithin Duration `toml:"identify_within"`
	// KillQuick is how long SET KILL QUICK gives (Anope's killquick, 20 s).
	KillQuick Duration `toml:"kill_quick"`
	// AllowKillImmed lets people choose SET KILL IMMED: renamed at once,
	// without a warning (Anope's allowkillimmed, off by default).
	AllowKillImmed bool `toml:"allow_kill_immed"`
	// BanExpiry is how long an AKILL, SNLINE, SQLINE, ZLINE or SHUN added
	// without +expiry lasts (Anope's autokillexpiry etc., 30 days). +0
	// keeps one until removed.
	BanExpiry Duration `toml:"ban_expiry"`
	// GuestPrefix starts the nick given to people who didn't identify.
	GuestPrefix string `toml:"guest_prefix"`
	// Admin is the account of the server admin. It always has every oper
	// flag and can't be changed from IRC.
	Admin string `toml:"admin"`
	// AdminPassword is the admin's oper password as an argon2id hash
	// (beeircd mkpasswd makes one). Empty: the admin sets it with their
	// first /oper while identified.
	AdminPassword string `toml:"admin_password"`
	// AdminHosts are the user@host masks (globs or CIDR) the admin may
	// /oper from. Empty: anywhere (a warning goes to the log at start).
	AdminHosts []string `toml:"admin_hosts"`
	// AdminVhost is the host the admin shows once opered ("" = none).
	AdminVhost string `toml:"admin_vhost"`
	// LogChannel is where the service bots log every command they run and
	// oper events (like Anope's log { target = "#services" }). The bots sit
	// in it with op and keep it +O (opers only). Empty: no log channel.
	LogChannel string `toml:"log_channel"`
	// LogConnects logs connects, quits and nick changes there (Anope's
	// users = "connect disconnect nick"). On by default; off for busy
	// networks.
	LogConnects *bool `toml:"log_connects"`

	// MaxLogins is how many connections may be identified to one account
	// at once (Anope's maxlogins, 10).
	MaxLogins int `toml:"max_logins"`
	// RegDelay is how long someone must be connected before REGISTER works
	// (Anope's regdelay, 30 s). "0s" switches it off.
	RegDelay *Duration `toml:"reg_delay"`
	// CertMax is how many certificate fingerprints one account may keep
	// (ns_cert max, 5).
	CertMax int `toml:"cert_max"`
	// AjoinMax is how long an auto join list may be (ajoinmax, 10).
	AjoinMax int `toml:"ajoin_max"`
	// ListMax is how many entries NickServ LIST shows (listmax, 50).
	ListMax int `toml:"list_max"`
	// ForceEmail makes REGISTER need an email (Anope's forceemail, on).
	ForceEmail *bool `toml:"force_email"`
	// MaxMemos is the memo limit of a new account (maxmemos, 20).
	MaxMemos int `toml:"max_memos"`
	// MemoSendDelay is how long someone waits between two memos
	// (senddelay, 3 s; opers don't wait). "0s" switches it off.
	MemoSendDelay *Duration `toml:"memo_send_delay"`
}

// ForceEmailOn reports whether REGISTER needs an email (default on).
func (s Services) ForceEmailOn() bool { return s.ForceEmail == nil || *s.ForceEmail }

// MemoSendDelayOr is memo_send_delay with its default (3 s).
func (s Services) MemoSendDelayOr() time.Duration {
	if s.MemoSendDelay == nil {
		return 3 * time.Second
	}
	return s.MemoSendDelay.Duration
}

// RegDelayOr is reg_delay with its default (30 s).
func (s Services) RegDelayOr() time.Duration {
	if s.RegDelay == nil {
		return 30 * time.Second
	}
	return s.RegDelay.Duration
}

// LogConnectsOn reports whether connects, quits and nick changes are
// logged in the log channel (default on).
func (s Services) LogConnectsOn() bool { return s.LogConnects == nil || *s.LogConnects }

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
	if err := c.checkClasses(); err != nil {
		return err
	}
	if err := c.History.check(); err != nil {
		return err
	}
	if err := c.Channels.check(); err != nil {
		return err
	}
	switch {
	case !serverRe.MatchString(c.Server.Name) || len(c.Server.Name) > 63:
		return fmt.Errorf("server name %q must look like irc.example.org", c.Server.Name)
	case !networkRe.MatchString(c.Server.Network):
		return fmt.Errorf("network name %q must be one word (letters, digits, dot, dash or underscore)", c.Server.Network)
	case !sidRe.MatchString(c.Server.SID):
		return fmt.Errorf("server sid %q must be a digit and two capital letters or digits, like 0BE", c.Server.SID)
	case c.Server.Prefixes != "@+" && c.Server.Prefixes != "~&@%+":
		return fmt.Errorf("server prefixes %q must be \"@+\" or \"~&@%%+\"", c.Server.Prefixes)
	case c.Services.LogChannel != "" && (!strings.HasPrefix(c.Services.LogChannel, "#") || len(c.Services.LogChannel) > 50 || strings.ContainsAny(c.Services.LogChannel, " ,\a")):
		return fmt.Errorf("services log_channel %q must be a channel like \"#services\" (or empty for none)", c.Services.LogChannel)
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
	if c.CloakEnabled() && len(c.Cloak.Secret) < 16 {
		return fmt.Errorf("[cloak] secret must be at least 16 characters of random text (or set enabled = false)")
	}
	if p := c.Services.AdminPassword; p != "" && !strings.HasPrefix(p, "$argon2id$") {
		return fmt.Errorf("[services] admin_password must be a hash from `beeircd mkpasswd`, not the password itself")
	}
	if c.Limits.NickLen > 50 || c.Limits.ChannelLen > 64 {
		return fmt.Errorf("nick_length is at most 50 and channel_length at most 64")
	}
	seen := make(map[string]bool)
	for _, d := range c.DNSBL {
		name := strings.ToLower(strings.Trim(d.Name, "."))
		switch {
		case !serverRe.MatchString(name):
			return fmt.Errorf("[[dnsbl]] name %q must be a DNS zone like \"dnsbl.dronebl.org\"", d.Name)
		case seen[name]:
			return fmt.Errorf("[[dnsbl]] %s is in the file twice", name)
		case !slices.Contains(DNSBLActions, d.Action):
			return fmt.Errorf("[[dnsbl]] %s: action %q must be reject, zline, akill or shun", name, d.Action)
		}
		for _, r := range d.Replies {
			if r < 1 || r > 255 {
				return fmt.Errorf("[[dnsbl]] %s: reply %d must be 1 to 255", name, r)
			}
		}
		seen[name] = true
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
