// Package config reads and checks beeircd.conf (TOML).
package config

import (
	"github.com/fhazal/beeircd/internal/acmecert"

	"fmt"
	"net"
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
	// WebIRC are the trusted web gateways ([[webirc]], webirc.go).
	WebIRC   []WebIRC `toml:"webirc"`
	Mail     Mail     `toml:"mail"`
	HostServ HostServ `toml:"hostserv"`
	Channels Channels `toml:"channels"`
	History  History  `toml:"history"`
	Control  Control  `toml:"control"`
	Backup   Backup   `toml:"backup"`
	Link     Link     `toml:"link"`
	Update   Update   `toml:"update"`
	Web      Web      `toml:"web"`
	Map      MapConf  `toml:"map"`
	// SpamFilter is [spamfilter] (spamfilter.go).
	SpamFilter SpamFilter `toml:"spamfilter"`
	// Censor is [censor], the words +G hides (censor.go).
	Censor Censor `toml:"censor"`
	GeoIP  GeoIP  `toml:"geoip"`
	// Reputation, SecurityGroups, ConnThrottle and Restricts sort known
	// people from new ones (security.go).
	Reputation     Reputation      `toml:"reputation"`
	SecurityGroups []SecurityGroup `toml:"security_group"`
	ConnThrottle   ConnThrottle    `toml:"connthrottle"`
	Restricts      []Restrict      `toml:"restrict"`
	// RPC is [rpc], the JSON-RPC admin API (rpc.go).
	RPC RPC `toml:"rpc"`
	// Auth is [auth], passwords kept in LDAP or SQL (auth.go).
	Auth Auth `toml:"auth"`
	// ShowFiles are commands of your own that show a text, like FAQ.
	ShowFiles []ShowFile `toml:"showfile"`
	// ExtJWT hands out signed tokens for other services (EXTJWT).
	ExtJWT ExtJWT `toml:"extjwt"`
	// More spam protection, each off by default (antispam.go).
	AntiRandom    AntiRandom    `toml:"antirandom"`
	AntiMixedUTF8 AntiMixedUTF8 `toml:"antimixedutf8"`
	TargetFlood   TargetFlood   `toml:"targetfloodprot"`
	BlockAmsg     BlockAmsg     `toml:"blockamsg"`
	// DenyDCC and AllowDCC say which files may be sent with DCC SEND.
	DenyDCC  []DenyDCC  `toml:"deny_dcc"`
	AllowDCC []AllowDCC `toml:"allow_dcc"`
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
	// Method is "beeircd" (the default), "unreal" (UnrealIRCd 6's
	// cloak_sha256) or "cloak_inspircd" (the cloak_inspircd module), for
	// networks that mix in UnrealIRCd servers. Every server needs the
	// same method and keys.
	Method string `toml:"method"`
	// Keys are UnrealIRCd's three set::cloak-keys (methods "unreal" and
	// "cloak_inspircd"): copy them from unrealircd.conf, in the same order.
	Keys []string `toml:"keys"`
	// Prefix starts hostname cloaks with method "unreal" (UnrealIRCd's
	// set::cloak-prefix, "Clk" by default).
	Prefix string `toml:"prefix"`
}

// UnrealCloak reports whether cloaks are UnrealIRCd's cloak_sha256.
func (c *Config) UnrealCloak() bool { return c.Cloak.Method == "unreal" }

// InspCloak reports whether cloaks are the cloak_inspircd module's.
func (c *Config) InspCloak() bool { return c.Cloak.Method == "cloak_inspircd" }

// KeyedCloak reports whether cloaks come from three UnrealIRCd keys
// (not the secret).
func (c *Config) KeyedCloak() bool { return c.UnrealCloak() || c.InspCloak() }

// Server names this server and its network.
type Server struct {
	Name        string `toml:"name"`
	Network     string `toml:"network"`
	Description string `toml:"description"`
	// SID is the 3-character server ID used for linking, like "0BE".
	SID string `toml:"sid"`
	// ResolveHosts looks up hostnames for connecting IPs. On by default.
	ResolveHosts *bool `toml:"resolve_hosts"`
	// FindPlaceByIP lets the server ask ipinfo.io once where its own IP
	// address is, for the Hive Panel's network map (city level, rounded
	// to about 10 km). Only the server's own address; on by default.
	FindPlaceByIP *bool `toml:"find_place_by_ip"`
	// Prefixes is the channel status style: "@+" (op and voice, the
	// default) or "~&@%+" (owner, admin, op, halfop, voice). Changing it
	// needs a restart.
	Prefixes string `toml:"prefixes"`
	// Casemapping is how nicks and channel names ignore case: "ascii"
	// (the default, like UnrealIRCd 6 and InspIRCd: only A-Z) or
	// "rfc1459" (also []\~ = {}|^). Every linked server needs the same,
	// and links to UnrealIRCd need ascii. Changing it needs a restart.
	Casemapping string `toml:"casemapping"`
	// IRCv3Drafts offers the IRCv3 drafts that aren't final yet
	// (read-marker, multiline, message-redaction, account-registration,
	// pre-away, extended-isupport) to clients that ask. Off by default.
	IRCv3Drafts bool `toml:"ircv3_drafts"`
	// IdentLookup asks the ident server (port 113) of each connecting
	// computer for the username. Off by default: almost nobody runs one
	// any more and waiting for an answer slows every login down.
	IdentLookup bool `toml:"ident_lookup"`
	// HideServers keeps the server list from people who are not opers:
	// LINKS and MAP only show the server they are on.
	HideServers bool `toml:"hide_servers"`
	// ConnectModes are user modes everyone gets when they connect, like
	// "+R" (on top of +i, and +x with cloaking).
	ConnectModes string `toml:"connect_modes"`
	// OperModes are user modes set at /oper, like "+WH".
	OperModes string `toml:"oper_modes"`
	// AutoJoin are channels everyone joins on connect (UnrealIRCd's
	// set::auto-join), like ["#chat"].
	AutoJoin []string `toml:"auto_join"`
	// AutoJoinSkip are security groups that skip AutoJoin, like a
	// relay bridge's ["webirc-users"].
	AutoJoinSkip []string `toml:"auto_join_skip"`
	// OperJoin are channels an oper joins at /oper.
	OperJoin []string `toml:"oper_join"`
	// OperJoinFlag are more channels for opers with one oper flag, like
	// { s = ["#services"] }.
	OperJoinFlag map[string][]string `toml:"oper_join_flag"`
	// PingCookie makes every new connection answer a PING with a random
	// word before it is let in: it proves the other side really reads
	// what the server sends.
	PingCookie bool `toml:"ping_cookie"`
	// WebRedirect is a web address browsers are sent to when they open
	// an IRC port by mistake.
	WebRedirect string `toml:"web_redirect"`
	// OperTag follows every oper's rank in WHOIS, like "(IRCop)".
	OperTag string `toml:"oper_tag"`
}

// FullPrefixes reports whether owner, admin and halfop exist.
func (c *Config) FullPrefixes() bool { return c.Server.Prefixes == "~&@%+" }

// Listen is one port.
type Listen struct {
	Address string `toml:"address"`
	TLS     bool   `toml:"tls"`
	// Proxy: connections start with a PROXY protocol v1/v2 header (from a
	// load balancer or a VPS tunnel) giving the real IP. Only ProxyFrom
	// (IPs or CIDRs) may connect to such a port.
	Proxy     bool     `toml:"proxy"`
	ProxyFrom []string `toml:"proxy_from"`
	// WebSocket: IRC over WebSocket for web pages (wss:// with tls). With
	// proxy, the proxy in front gives the real IP in X-Forwarded-For
	// instead of a PROXY header. Origins limits which pages may connect.
	WebSocket bool     `toml:"websocket"`
	Origins   []string `toml:"origins"`
}

// TLS points at the certificate. With SelfSigned, missing files are made.
// With ACME the server gets and renews a Let's Encrypt certificate into
// Cert and Key itself.
type TLS struct {
	Cert       string `toml:"cert"`
	Key        string `toml:"key"`
	SelfSigned bool   `toml:"self_signed"`
	ACME       bool   `toml:"acme"`
	ACMEEmail  string `toml:"acme_email"`
	// ACMEDomains are the names on the certificate; the server name when
	// empty. Use it when clients connect by another name.
	ACMEDomains []string `toml:"acme_domains"`
	// ACMEDirectory is another ACME CA (Let's Encrypt staging, a private
	// CA); Let's Encrypt when empty.
	ACMEDirectory string `toml:"acme_directory"`
	// ACMEHTTP is where the Let's Encrypt check is answered while a
	// certificate is asked for: ":80" when empty. Set another port when a
	// tunnel or firewall forwards the outside port 80 there.
	ACMEHTTP string `toml:"acme_http"`
	// STS tells clients to use TLS from now on (IRCv3 sts). On by default
	// with a real certificate; never offered with a self-signed one.
	STS *bool `toml:"sts"`
	// STSPort is the TLS port clients are sent to; the first TLS port when
	// 0 (set it when a tunnel shows another port to the outside).
	STSPort int `toml:"sts_port"`
}

// Domains is what a Let's Encrypt certificate is asked for.
func (t TLS) Domains(serverName string) []string {
	if len(t.ACMEDomains) > 0 {
		return t.ACMEDomains
	}
	return []string{serverName}
}

// AccountKey is the ACME account key file, next to the certificate.
func (t TLS) AccountKey() string {
	return filepath.Join(filepath.Dir(t.Cert), "acme-account.key")
}

// STSOn says whether the sts capability may be offered (the certificate
// itself must also be a real one; the server checks that).
func (c *Config) STSOn() bool {
	if !c.NeedsTLS() || (c.TLS.SelfSigned && !c.TLS.ACME) {
		return false
	}
	return c.TLS.STS == nil || *c.TLS.STS
}

// MOTD is read from File; Text is used when there is no file.
type MOTD struct {
	File string `toml:"file"`
	Text string `toml:"text"`
	// OperFile / OperText is shown to opers after /oper and by OPERMOTD.
	OperFile string `toml:"oper_file"`
	OperText string `toml:"oper_text"`
	// BotFile / BotText is shown to bots (user mode +B) and by BOTMOTD.
	BotFile string `toml:"bot_file"`
	BotText string `toml:"bot_text"`
	// RulesFile / RulesText is what RULES shows.
	RulesFile string `toml:"rules_file"`
	RulesText string `toml:"rules_text"`
	// QuotesFile holds one quote per line; everyone gets a random one
	// when they connect.
	QuotesFile string `toml:"quotes_file"`
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
	// ConnectRate is "connections:seconds" one IP may open (5:60), like
	// UnrealIRCd's connect-flood; "off" for none. MaxUnregistered and
	// MaxUnregisteredPerIP cap connections that haven't finished logging
	// in (500 and 3). LAN IPs and exceptions skip all three.
	ConnectRate          string `toml:"connect_rate"`
	MaxUnregistered      int    `toml:"max_unregistered"`
	MaxUnregisteredPerIP int    `toml:"max_unregistered_per_ip"`
	// UTF8Only refuses commands whose text isn't UTF-8 and says UTF8ONLY
	// to clients (IRCv3). Off by default: old clients sending Latin-1
	// keep working.
	UTF8Only bool `toml:"utf8_only"`
	// DisabledCommands are commands switched off for everyone but opers,
	// like ["KNOCK", "WHOWAS"].
	DisabledCommands []string `toml:"disabled_commands"`
	// RestrictMsg lets people send private messages to opers and
	// services only; opers can write to anyone.
	RestrictMsg bool `toml:"restrict_msg"`
	// Abbreviations lets people shorten a command by ending it with a
	// full stop, like "WHOW." for WHOWAS.
	Abbreviations bool `toml:"abbreviations"`
}

// ConnectLimit reads connect_rate: n connections per period; ok false
// when it is off.
func (l Limits) ConnectLimit() (n int, period time.Duration, ok bool) {
	return ParseRate(l.ConnectRate)
}

// ParseRate reads "count:seconds" like 5:60; "off" (or "") gives ok false.
func ParseRate(v string) (n int, period time.Duration, ok bool) {
	a, b, found := strings.Cut(strings.TrimSpace(v), ":")
	if !found {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || x < 1 || y < 1 || x > 1000 || y > 3600 {
		return 0, 0, false
	}
	return x, time.Duration(y) * time.Second, true
}

// CloakEnabled reports whether hosts are cloaked.
func (c *Config) CloakEnabled() bool { return c.Cloak.Enabled == nil || *c.Cloak.Enabled }

// ResolveHosts reports whether hostname lookups are on.
func (c *Config) ResolveHosts() bool {
	return c.Server.ResolveHosts == nil || *c.Server.ResolveHosts
}

// FindPlaceByIP reports whether [server] find_place_by_ip is on (default).
func (c *Config) FindPlaceByIP() bool {
	return c.Server.FindPlaceByIP == nil || *c.Server.FindPlaceByIP
}

// NeedsTLS reports whether any port uses TLS.
func (c *Config) NeedsTLS() bool {
	if c.RPC.Listen != "" {
		return true // the API is HTTPS only
	}
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
	if c.TLS.ACME && c.TLS.Cert == "" && c.TLS.Key == "" {
		c.TLS.Cert, c.TLS.Key = filepath.Join("tls", "cert.pem"), filepath.Join("tls", "key.pem")
	}
	for _, p := range []*string{&c.TLS.Cert, &c.TLS.Key, &c.MOTD.File, &c.Services.Database, &c.History.File, &c.Control.Socket, &c.Backup.Dir} {
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
	c.Web.setDefaults()
	if c.Server.Description == "" {
		c.Server.Description = c.Server.Network + " IRC"
	}
	if c.Server.Prefixes == "" {
		c.Server.Prefixes = "@+"
	}
	if c.Server.Casemapping == "" {
		c.Server.Casemapping = "ascii"
	}
	if c.Cloak.Method == "" {
		c.Cloak.Method = "beeircd"
	}
	if c.Cloak.Prefix == "" {
		c.Cloak.Prefix = "Clk"
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
	if l.ConnectRate == "" {
		l.ConnectRate = "5:60"
	}
	setInt(&l.MaxUnregistered, 500)
	setInt(&l.MaxUnregisteredPerIP, 3)
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
	c.setLinkDefaults()
	c.setAdminDefaults()
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
	// Enabled runs NickServ, ChanServ and the other bots on this server
	// (on by default). A network has one services server: set false on
	// the others, they use the hub's over the link.
	Enabled *bool `toml:"enabled"`
	// ServerName and ServerSID are the pseudo-server the bots sit on when
	// this server links (services.<domain> and our SID with 9 first by
	// default). UnrealIRCd servers U-line this name.
	ServerName string `toml:"server_name"`
	ServerSID  string `toml:"server_sid"`
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
	// Registration is Anope's ns_register registration: "none" (an account
	// is ready at once), "mail" (REGISTER mails a passcode for NickServ
	// CONFIRM) or "admin" (an oper confirms each new account).
	Registration string `toml:"registration"`
	// UnconfirmedExpire drops accounts not confirmed this long after
	// REGISTER (unconfirmedexpire, 1 day; 0s never). See
	// UnconfirmedExpireOr.
	UnconfirmedExpire *Duration `toml:"unconfirmed_expire"`
	// DefaultLanguage is the language services answer in for people who
	// haven't picked one with SET LANGUAGE (Anope's defaultlanguage):
	// "en" (default) or "ms".
	DefaultLanguage string `toml:"default_language"`
}

// UnconfirmedExpireOr is unconfirmed_expire with its default (1 day); 0
// means never.
func (s Services) UnconfirmedExpireOr() time.Duration {
	if s.UnconfirmedExpire == nil {
		return 24 * time.Hour
	}
	return s.UnconfirmedExpire.Duration
}

// RegistrationMode is registration with its default, "none".
func (s Services) RegistrationMode() string {
	if s.Registration == "" {
		return "none"
	}
	return strings.ToLower(s.Registration)
}

// On reports whether this server runs services (default on).
func (s Services) On() bool { return s.Enabled == nil || *s.Enabled }

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
	switch c.Services.RegistrationMode() {
	case "none", "admin":
	case "mail":
		if !c.Mail.On() {
			return fmt.Errorf("[services] registration = \"mail\" needs [mail] use_mail = true and send_from")
		}
	default:
		return fmt.Errorf("[services] registration %q: use \"none\", \"mail\" or \"admin\"", c.Services.Registration)
	}
	if err := c.Web.check(); err != nil {
		return err
	}
	if err := c.Auth.check(); err != nil {
		return err
	}
	if err := c.checkSmall(); err != nil {
		return err
	}
	if err := c.checkDCC(); err != nil {
		return err
	}
	if err := c.Map.check(); err != nil {
		return err
	}
	if err := c.History.check(); err != nil {
		return err
	}
	if err := c.Channels.check(); err != nil {
		return err
	}
	if r := c.Limits.ConnectRate; !strings.EqualFold(r, "off") {
		if _, _, ok := ParseRate(r); !ok {
			return fmt.Errorf("[limits] connect_rate %q: use connections:seconds like 5:60, or off", r)
		}
	}
	for _, l := range c.Listen {
		if len(l.Origins) > 0 && !l.WebSocket {
			return fmt.Errorf("[[listen]] %s: origins is for websocket = true ports", l.Address)
		}
		if !l.Proxy {
			continue
		}
		if len(l.ProxyFrom) == 0 {
			return fmt.Errorf("[[listen]] %s: proxy = true needs proxy_from (the IPs of your proxy)", l.Address)
		}
		for _, m := range l.ProxyFrom {
			if _, err := ParseCIDR(m); err != nil {
				return fmt.Errorf("[[listen]] %s: proxy_from %q is not an IP or CIDR", l.Address, m)
			}
		}
	}
	switch {
	case !serverRe.MatchString(c.Server.Name) || len(c.Server.Name) > 63:
		return fmt.Errorf("server name %q must look like irc.example.org", c.Server.Name)
	case !networkRe.MatchString(c.Server.Network):
		return fmt.Errorf("network name %q must be one word (letters, digits, dot, dash or underscore)", c.Server.Network)
	case !sidRe.MatchString(c.Server.SID):
		return fmt.Errorf("server sid %q must be a digit and two capital letters or digits, like 0BE", c.Server.SID)
	case c.Server.Casemapping != "ascii" && c.Server.Casemapping != "rfc1459":
		return fmt.Errorf("server casemapping %q must be \"ascii\" or \"rfc1459\"", c.Server.Casemapping)
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
		return fmt.Errorf("a TLS port needs [tls] cert and key (or acme = true)")
	}
	if err := c.checkUpdate(); err != nil {
		return err
	}
	if err := c.checkLink(); err != nil {
		return err
	}
	if err := c.checkACME(); err != nil {
		return err
	}
	if err := c.checkCloak(); err != nil {
		return err
	}
	if c.CloakEnabled() && !c.KeyedCloak() && len(c.Cloak.Secret) < 16 {
		return fmt.Errorf("[cloak] secret must be at least 16 characters of random text (or set enabled = false)")
	}
	if p := c.Services.AdminPassword; p != "" && !strings.HasPrefix(p, "$argon2id$") {
		return fmt.Errorf("[services] admin_password must be a hash from `beeircd mkpasswd`, not the password itself")
	}
	if err := c.checkCensor(); err != nil {
		return err
	}
	if err := c.checkSpamFilter(); err != nil {
		return err
	}
	if err := c.checkGroups(); err != nil {
		return err
	}
	if err := c.checkConnThrottle(); err != nil {
		return err
	}
	if err := c.checkRestrict(); err != nil {
		return err
	}
	if err := c.checkAntiSpam(); err != nil {
		return err
	}
	if err := c.CheckRPC(); err != nil {
		return err
	}
	if err := c.checkWebIRC(); err != nil {
		return err
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
func (c *Config) MOTDLines() []string { return TextLines(c.MOTD.File, c.MOTD.Text) }

// TextLines is the lines of file, or of text when there is no file (or
// it can't be read).
func TextLines(file, text string) []string {
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
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

// ParseCIDR reads an IP (as a /32 or /128) or a CIDR.
func ParseCIDR(v string) (*net.IPNet, error) {
	if !strings.Contains(v, "/") {
		ip := net.ParseIP(v)
		if ip == nil {
			return nil, fmt.Errorf("bad IP %q", v)
		}
		bits := 128
		if ip.To4() != nil {
			ip, bits = ip.To4(), 32
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
	}
	_, n, err := net.ParseCIDR(v)
	return n, err
}

func (c *Config) checkACME() error {
	t := c.TLS
	if !t.ACME {
		if t.ACMEEmail != "" || len(t.ACMEDomains) > 0 || t.ACMEDirectory != "" || t.ACMEHTTP != "" {
			return fmt.Errorf("[tls] acme_email, acme_domains, acme_directory and acme_http need acme = true")
		}
	} else {
		switch {
		case t.SelfSigned:
			return fmt.Errorf("[tls] pick acme or self_signed, not both")
		case !c.NeedsTLS():
			return fmt.Errorf("[tls] acme = true, but no [[listen]] port has tls = true")
		case t.ACMEEmail != "" && (!strings.Contains(t.ACMEEmail, "@") || strings.ContainsAny(t.ACMEEmail, " ,<>")):
			return fmt.Errorf("[tls] acme_email %q isn't an email address", t.ACMEEmail)
		case t.ACMEHTTP != "" && !validHostPort(t.ACMEHTTP):
			return fmt.Errorf("[tls] acme_http %q must look like \":80\" or \"10.0.0.2:8080\"", t.ACMEHTTP)
		case t.ACMEDirectory != "" && !strings.HasPrefix(t.ACMEDirectory, "https://"):
			return fmt.Errorf("[tls] acme_directory must be an https:// URL")
		}
		for _, d := range t.Domains(c.Server.Name) {
			if why := acmecert.ValidDomain(d); why != "" {
				if len(t.ACMEDomains) == 0 {
					return fmt.Errorf("[tls] acme: the server name %s %s, so Let's Encrypt can't check it; set acme_domains = [\"irc.example.org\"] to the name clients connect to", d, why)
				}
				return fmt.Errorf("[tls] acme_domains: %s %s", d, why)
			}
		}
	}
	if t.STSPort < 0 || t.STSPort > 65535 {
		return fmt.Errorf("[tls] sts_port must be a port number")
	}
	return nil
}

func validHostPort(a string) bool {
	_, p, err := net.SplitHostPort(a)
	n, perr := strconv.Atoi(p)
	return err == nil && perr == nil && n > 0 && n < 65536
}

// ValidServerName reports whether name may be a server name.
func ValidServerName(name string) bool { return serverRe.MatchString(name) && len(name) <= 63 }

// checkCloak checks [cloak] method, keys and prefix, with
// UnrealIRCd's own rules for the keys.
func (c *Config) checkCloak() error {
	k := c.Cloak
	switch k.Method {
	case "beeircd":
		if len(k.Keys) > 0 {
			return fmt.Errorf("[cloak] keys are for method \"unreal\" or \"cloak_inspircd\"; method \"beeircd\" uses secret")
		}
		return nil
	case "unreal", "cloak_inspircd":
	default:
		return fmt.Errorf("[cloak] method %q must be \"beeircd\", \"unreal\" or \"cloak_inspircd\"", k.Method)
	}
	minLen, maxLen := 80, 1000 // UnrealIRCd's cloak_sha256 rule
	if k.Method == "cloak_inspircd" {
		minLen, maxLen = 5, 100 // the module's own rule
	}
	if !c.CloakEnabled() {
		return nil
	}
	if len(k.Keys) != 3 {
		return fmt.Errorf("[cloak] method %q needs keys = [three keys], the set::cloak-keys of your UnrealIRCd servers in the same order", k.Method)
	}
	for i, key := range k.Keys {
		lower, upper, digit := false, false, false
		for _, r := range key {
			switch {
			case r >= 'a' && r <= 'z':
				lower = true
			case r >= 'A' && r <= 'Z':
				upper = true
			case r >= '0' && r <= '9':
				digit = true
			}
		}
		if len(key) < minLen || len(key) > maxLen || !lower || !upper || !digit {
			return fmt.Errorf("[cloak] key %d must be %d to %d characters mixing a-z, A-Z and 0-9 (the rule of method %s)", i+1, minLen, maxLen, k.Method)
		}
	}
	if k.Keys[0] == k.Keys[1] || k.Keys[1] == k.Keys[2] {
		return fmt.Errorf("[cloak] the three keys must differ")
	}
	if !ValidHostPart(k.Prefix) {
		return fmt.Errorf("[cloak] prefix %q must be letters, digits or '-'", k.Prefix)
	}
	return nil
}

// ValidHostPart reports whether p may start a hostname.
func ValidHostPart(p string) bool {
	if p == "" || len(p) > 20 || p[0] == '-' {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ValidNetwork reports whether name may be a network name.
func ValidNetwork(name string) bool { return networkRe.MatchString(name) }

// portOf is the port number of a listen address like ":6697".
func portOf(addr string) (int, error) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(p)
}
