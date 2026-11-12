package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// adminCmd is one AdminServ command.
type adminCmd struct {
	syntax, short, long string
	run                 func(*Server, *Client, []string)
}

func adminCommands() map[string]adminCmd {
	return map[string]adminCmd{
		"SHOW": {"SHOW [setting]", "the settings you can change", "Shows every setting AdminServ can change, or one.", asShow},
		"SET": {"SET <setting> <value>", "change a setting now",
			"Changes a setting at once and saves it to beeircd.conf (comments stay).\nExample: /as set max-users 500   /as set cloak off   /as set nick-expire 120d\nSee them all: /as show", asSet},
		"MOTD": {"MOTD SHOW|SET|ADD|DEL|CLEAR [text]", "edit the message of the day",
			"MOTD SHOW            the MOTD with line numbers\nMOTD SET <text>      replace it with one line\nMOTD ADD <text>      add a line at the end\nMOTD DEL <number>    remove a line\nMOTD CLEAR           empty it", asMotd},
		"PORT": {"PORT ADD|DEL|LIST [port] [TLS]", "open or close a port",
			"PORT LIST\nPORT ADD <port> [TLS]   opens it now, on the same address as the first port\nPORT ADD <address:port> [TLS]\nPORT DEL <port|address:port>\nPeople already connected through a closed port stay connected.", asPort},
		"OPER": {"OPER LIST | OPER <nick> ADD|SET|DEL|DISABLE|ENABLE ...", "who is an IRC operator",
			"OPER LIST\nOPER <nick> ADD CLASS <class>|FLAGS <letters> HOST <mask> [PASSWORD <password>] [TITLE <text>]\nOPER <nick> SET CLASS <class>|OFF\nOPER <nick> SET FLAGS <letters>|OFF   flags on top of the class\nOPER <nick> SET HOST <mask> [mask ...]   globs or CIDR, like *@192.168.50.0/24\nOPER <nick> SET PASSWORD <password>\nOPER <nick> SET TITLE|VHOST <text>|OFF   instead of the class's\nOPER <nick> SET SWHOIS <line> [| <line> ...]|OFF\nOPER <nick> DISABLE [reason]   keep the entry, refuse /oper\nOPER <nick> ENABLE\nOPER <nick> DEL\nClasses: /as class list (helper, globop, admin, netadmin are built in).\nFlags: a admin, c links, g global, h hosts, k kill, s services.\nThe nick must be registered. Opers oper up with /oper <nick> <oper password>\nfrom a matching host; the oper password is their own, not the NickServ one.\nYou can only give flags (and classes) you have.\nExample: /as oper ember add class globop host *@192.168.50.* password Hive-2026", asOper},
		"CLASS": {"CLASS LIST | CLASS <name> ADD|SET|DEL ...", "oper classes",
			"CLASS LIST\nCLASS <name> ADD FLAGS <letters>\nCLASS <name> SET FLAGS <letters>\nCLASS <name> SET TITLE <text>|OFF   WHOIS line, like: is a Network Administrator\nCLASS <name> SET VHOST <host>|OFF   shown host at /oper, like staff.example.org\nCLASS <name> SET SWHOIS <line> [| <line> ...]|OFF\nCLASS <name> DEL   a built-in class goes back to its defaults\nClasses are saved as [[operclass]] blocks in beeircd.conf.", asClass},
		"HISTORY": {"HISTORY [count]", "the changes AdminServ made",
			"Lists the latest changes made with AdminServ (settings, MOTD, ports, opers,\nclasses), newest first. The last 200 are kept.", asHistory},
		"DIFF": {"DIFF <number>", "one change, before and after", "Shows what change <number> (from HISTORY) was before and after.", asDiff},
		"UNDO": {"UNDO <number>", "put a change back",
			"Puts back how things were before change <number> (from HISTORY): settings,\nthe MOTD, classes and oper entries. Ports are changed back with PORT ADD/DEL,\npasswords with a new one. The undo is itself a change in the history.", asUndo},
		"DNSBL": {"DNSBL LIST | CHECK <ip> | ENABLE|DISABLE <name>", "the DNS blocklists",
			"DNSBL LIST              the [[dnsbl]] lists of beeircd.conf, with their hits\nDNSBL CHECK <ip>        look an IP up in every list now, with its answers\nDNSBL DISABLE <name>    stop using a list until the server restarts\nDNSBL ENABLE <name>     use it again\nNew connections are looked up when they connect; LAN IPs, ELINEs and session\nexceptions are skipped. Each list says what a hit does: reject (refuse the\nconnection), or a zline, akill or shun for its ban_time.", asDnsbl},
		"SERVER": {"SERVER LIST|ADD|SET|DEL|LINK|UNLINK ... | SERVER <name> STATUS|<command>", "linked servers",
			serverHelp + "\nSERVER <name> STATUS       version, uptime, users and links of that server\nSERVER <name> <command>    run an AdminServ command there (SET, MOTD, OPER,\n                           CLASS, SHOW, HISTORY, DIFF, UNDO, DNSBL); it must\n                           allow it with [link] remote_admin = true", asServer},
		"RESTART": {"RESTART [reason]", "restart the server",
			"Disconnects everyone and starts the server again. You are asked to repeat it\nwith a code first.", asStop(stopRestart)},
		"DIE": {"DIE [reason]", "stop the server",
			"Disconnects everyone and stops the server. You are asked to repeat it with a\ncode first. Under systemd or Docker it may be started again by them.", asStop(stopDie)},
	}
}

// adminServCommands are the AdminServ commands for the services bot
// framework; each runs on the connection of whoever sent it.
func (s *Server) adminServCommands() map[string]services.BotCommand {
	out := make(map[string]services.BotCommand)
	for name, cmd := range adminCommands() {
		run := cmd.run
		name := name
		out[name] = services.BotCommand{Syntax: cmd.syntax, Short: cmd.short, Long: cmd.long, Run: func(uid string, args []string) {
			if c := s.clients[uid]; c != nil {
				if c.user != nil {
					run(s, c, args)
				}
				return
			}
			// Someone on another server: SERVER (to reach their own
			// server) always; the rest only with [link] remote_admin.
			s.runFor(uid, func(c *Client) {
				if name != "SERVER" && !s.cfg.Link.RemoteAdmin {
					s.as(c, "AdminServ on %s only takes commands from people on it ([link] remote_admin = false). For your own server: /as server <its name> <command>", s.cfg.Server.Name)
					return
				}
				run(s, c, args)
			})
		}}
	}
	return out
}

func (s *Server) as(c *Client, format string, a ...any) {
	s.botReply(c, s.adminServ, fmt.Sprintf(format, a...))
}

// audit notes who changed what through bot, for the log and opers with +s.
func (s *Server) audit(c *Client, bot *state.User, format string, a ...any) {
	s.svc.Audit(c.uid, bot, fmt.Sprintf(format, a...))
}

// dbErr reports a database failure without details to the user.
func (s *Server) dbErr(c *Client, bot *state.User, err error) {
	s.log.Printf("services database: %v", err)
	s.botReply(c, bot, "Something went wrong on the server. Please try again in a moment.")
}

var (
	argAt      = services.ArgAt
	onOff      = services.OnOff
	plural     = services.Plural
	randomCode = services.RandomCode
)

// confirmFor is how long a RESTART or DIE code stays valid.
const confirmFor = 2 * time.Minute

// A setting AdminServ can change.
type setting struct {
	name  string
	table string
	key   string
	help  string
	kind  byte // 'i' number, 'd' time, 'b' on/off, 't' text
	lo    int64
	hi    int64 // numbers: limits; times: in seconds
	get   func(*config.Config) string
	apply func(dst, src *config.Config)
	check func(string) string // extra check for text, returns the problem
	offOK bool                // text: OFF empties it
	after func(s *Server, old string)
}

func limitsApply(dst, src *config.Config)   { dst.Limits = src.Limits }
func servicesApply(dst, src *config.Config) { dst.Services = src.Services }

var guestRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,9}$`)

func settings() []setting {
	num := func(f func(*config.Config) int) func(*config.Config) string {
		return func(c *config.Config) string { return strconv.Itoa(f(c)) }
	}
	dur := func(f func(*config.Config) time.Duration) func(*config.Config) string {
		return func(c *config.Config) string { return config.FormatDuration(f(c)) }
	}
	onoff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	day := int64(24 * 3600)
	return []setting{
		{name: "description", table: "server", key: "description", help: "shown in WHOIS and LINKS", kind: 't', hi: 100,
			get: func(c *config.Config) string { return c.Server.Description }, apply: func(d, s *config.Config) { d.Server.Description = s.Server.Description }},
		{name: "max-users", table: "limits", key: "max_users", help: "people connected at once", kind: 'i', lo: 1, hi: 100000,
			get: num(func(c *config.Config) int { return c.Limits.MaxUsers }), apply: limitsApply},
		{name: "session-limit", table: "limits", key: "session_limit", help: "connections from one IP", kind: 'i', lo: 1, hi: 1000,
			get: num(func(c *config.Config) int { return c.Limits.SessionLimit }), apply: limitsApply},
		{name: "connect-rate", table: "limits", key: "connect_rate", help: "connections one IP may open, like 5:60 (OFF for no limit)", kind: 't', hi: 9,
			get: func(c *config.Config) string { return strings.ToLower(c.Limits.ConnectRate) }, apply: limitsApply,
			check: func(v string) string {
				if _, _, ok := config.ParseRate(v); !ok && !strings.EqualFold(v, "off") {
					return "connect-rate takes connections:seconds like 5:60, or OFF."
				}
				return ""
			}},
		{name: "max-unregistered", table: "limits", key: "max_unregistered", help: "connections logging in at once", kind: 'i', lo: 10, hi: 100000,
			get: num(func(c *config.Config) int { return c.Limits.MaxUnregistered }), apply: limitsApply},
		{name: "max-unregistered-per-ip", table: "limits", key: "max_unregistered_per_ip", help: "connections logging in at once from one IP", kind: 'i', lo: 1, hi: 100,
			get: num(func(c *config.Config) int { return c.Limits.MaxUnregisteredPerIP }), apply: limitsApply},
		{name: "max-channels", table: "limits", key: "max_channels", help: "channels one person can join", kind: 'i', lo: 1, hi: 500,
			get: num(func(c *config.Config) int { return c.Limits.MaxChannels }), apply: limitsApply},
		{name: "topic-length", table: "limits", key: "topic_length", help: "new connections see it in 005", kind: 'i', lo: 20, hi: 390,
			get: num(func(c *config.Config) int { return c.Limits.TopicLen }), apply: limitsApply},
		{name: "away-length", table: "limits", key: "away_length", kind: 'i', lo: 20, hi: 390,
			get: num(func(c *config.Config) int { return c.Limits.AwayLen }), apply: limitsApply},
		{name: "kick-length", table: "limits", key: "kick_length", kind: 'i', lo: 20, hi: 390,
			get: num(func(c *config.Config) int { return c.Limits.KickLen }), apply: limitsApply},
		{name: "flood-burst", table: "limits", key: "flood_burst", help: "lines at once before slowing down (new connections)", kind: 'i', lo: 1, hi: 1000,
			get: num(func(c *config.Config) int { return c.Limits.FloodBurst }), apply: limitsApply},
		{name: "flood-rate", table: "limits", key: "flood_rate", help: "lines per second after that (new connections)", kind: 'i', lo: 1, hi: 1000,
			get: num(func(c *config.Config) int { return c.Limits.FloodRate }), apply: limitsApply},
		{name: "ping-interval", table: "limits", key: "ping_interval", kind: 'd', lo: 15, hi: 1800,
			get: dur(func(c *config.Config) time.Duration { return c.Limits.PingInterval.Duration }), apply: limitsApply},
		{name: "ping-timeout", table: "limits", key: "ping_timeout", kind: 'd', lo: 15, hi: 1800,
			get: dur(func(c *config.Config) time.Duration { return c.Limits.PingTimeout.Duration }), apply: limitsApply},
		{name: "cloak", table: "cloak", key: "enabled", help: "hide hosts for new connections", kind: 'b',
			get: func(c *config.Config) string { return onoff(c.CloakEnabled()) }, apply: func(d, s *config.Config) { d.Cloak = s.Cloak }},
		{name: "resolve-hosts", table: "server", key: "resolve_hosts", help: "look up hostnames for new connections", kind: 'b',
			get: func(c *config.Config) string { return onoff(c.ResolveHosts()) }, apply: func(d, s *config.Config) { d.Server.ResolveHosts = s.Server.ResolveHosts }},
		{name: "find-place-by-ip", table: "server", key: "find_place_by_ip", help: "ask ipinfo.io once where this server's own IP is, for the network map (city level)", kind: 'b',
			get: func(c *config.Config) string { return onoff(c.FindPlaceByIP()) }, apply: func(d, s *config.Config) { d.Server.FindPlaceByIP = s.Server.FindPlaceByIP },
			after: func(s *Server, _ string) { s.startPlaceLookup(); s.announcePlace() }},
		{name: "nick-expire", table: "services", key: "nick_expire", help: "unused accounts are dropped after", kind: 'd', lo: day, hi: 3650 * day,
			get: dur(func(c *config.Config) time.Duration { return c.Services.NickExpire.Duration }), apply: servicesApply},
		{name: "chan-expire", table: "services", key: "chan_expire", help: "unused channels are dropped after", kind: 'd', lo: day, hi: 3650 * day,
			get: dur(func(c *config.Config) time.Duration { return c.Services.ChanExpire.Duration }), apply: servicesApply},
		{name: "identify-within", table: "services", key: "identify_within", help: "time to identify before the Guest rename", kind: 'd', lo: 10, hi: 600,
			get: dur(func(c *config.Config) time.Duration { return c.Services.IdentifyWithin.Duration }), apply: servicesApply},
		{name: "kill-quick", table: "services", key: "kill_quick", help: "time NickServ SET KILL QUICK gives", kind: 'd', lo: 5, hi: 300,
			get: dur(func(c *config.Config) time.Duration { return c.Services.KillQuick.Duration }), apply: servicesApply},
		{name: "allow-kill-immed", table: "services", key: "allow_kill_immed", help: "people may choose NickServ SET KILL IMMED", kind: 'b',
			get: func(c *config.Config) string { return onoff(c.Services.AllowKillImmed) }, apply: servicesApply},
		{name: "ban-expiry", table: "services", key: "ban_expiry", help: "network bans without +expiry last", kind: 'd', lo: 60, hi: 3650 * day,
			get: dur(func(c *config.Config) time.Duration { return c.Services.BanExpiry.Duration }), apply: servicesApply},
		{name: "guest-prefix", table: "services", key: "guest_prefix", help: "start of Guest12345 nicks", kind: 't', hi: 10,
			get: func(c *config.Config) string { return c.Services.GuestPrefix }, apply: servicesApply,
			check: func(v string) string {
				if !guestRe.MatchString(v) {
					return "A guest prefix is 1 to 10 letters, digits, _ or -, starting with a letter."
				}
				return ""
			}},
		{name: "log-channel", table: "services", key: "log_channel", help: "services log every command here (OFF for none)", kind: 't', hi: 50, offOK: true,
			get: func(c *config.Config) string {
				if c.Services.LogChannel == "" {
					return "off"
				}
				return c.Services.LogChannel
			}, apply: servicesApply,
			check: func(v string) string {
				if !irc.ValidChannel(v, 50) {
					return "log-channel takes a channel like #services, or OFF."
				}
				return ""
			},
			after: func(s *Server, old string) {
				if old == "off" {
					old = ""
				}
				s.svc.SyncLogChannel(old)
			}},
		{name: "log-connects", table: "services", key: "log_connects", help: "also log connects and quits there", kind: 'b',
			get: func(c *config.Config) string { return onoff(c.Services.LogConnectsOn()) }, apply: servicesApply},
		{name: "flood-profile", table: "channels", key: "flood_profile", help: "+F profile for channels without their own (off for none)", kind: 't', hi: 20,
			get: func(c *config.Config) string { return c.Channels.FloodProfile }, apply: func(d, s *config.Config) { d.Channels = s.Channels },
			check: func(v string) string {
				if _, ok := config.FloodProfiles[strings.ToLower(v)]; !ok {
					return "flood-profile is one of " + strings.Join(config.ProfileNames(), ", ") + "."
				}
				return ""
			}},
	}
}

func findSetting(name string) (setting, bool) {
	name = strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	for _, st := range settings() {
		if st.name == name {
			return st, true
		}
	}
	return setting{}, false
}

// tomlValue checks value for st and returns it written as TOML.
func (st setting) tomlValue(value string) (string, string) {
	switch st.kind {
	case 'i':
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < st.lo || n > st.hi {
			return "", fmt.Sprintf("%s takes a number from %d to %d.", st.name, st.lo, st.hi)
		}
		return strconv.FormatInt(n, 10), ""
	case 'd':
		d, err := config.ParseDuration(strings.ToLower(value))
		lo, hi := time.Duration(st.lo)*time.Second, time.Duration(st.hi)*time.Second
		if err != nil || d < lo || d > hi {
			return "", fmt.Sprintf("%s takes a time from %s to %s, like 90s, 5m or 30d.", st.name, config.FormatDuration(lo), config.FormatDuration(hi))
		}
		return config.Quote(config.FormatDuration(d)), ""
	case 'b':
		on, ok := onOff(value)
		if !ok {
			return "", st.name + " takes ON or OFF."
		}
		return strconv.FormatBool(on), ""
	}
	if st.offOK && strings.EqualFold(value, "off") {
		return `""`, ""
	}
	if value == "" || len(value) > int(st.hi) {
		return "", fmt.Sprintf("%s takes text of up to %d characters.", st.name, st.hi)
	}
	if st.check != nil {
		if p := st.check(value); p != "" {
			return "", p
		}
	}
	return config.Quote(value), ""
}

// applySetting changes one setting, saves it and tells c (as sv).
func (s *Server) applySetting(c *Client, sv *state.User, name, value string) {
	st, ok := findSetting(name)
	if !ok {
		s.botReply(c, sv, fmt.Sprintf("There is no setting %s. See them all: /as show", name))
		return
	}
	old, now, _, problem := s.setSetting(c.user.Nick, st, value)
	if problem != "" {
		s.botReply(c, sv, problem)
		return
	}
	s.audit(c, sv, "SET %s %s (was %s)", st.name, now, old)
	s.svc.LogAdmin(c.uid, sv, fmt.Sprintf("to change the configuration value of %s:%s to %s", st.table, st.key, now)) // os_config
	s.botReply(c, sv, fmt.Sprintf("%s is now %s (was %s). Saved to %s.", st.name, now, old, filepath.Base(s.cfg.Path)))
}

// setSetting checks value for st, saves it to beeircd.conf, applies it and
// records it for HISTORY as made by who. problem says (in words for the
// person) why nothing changed; id is the HISTORY entry.
func (s *Server) setSetting(who string, st setting, value string) (old, now string, id int64, problem string) {
	lit, problem := st.tomlValue(value)
	if problem != "" {
		return "", "", 0, problem
	}
	old = st.get(s.cfg)
	if s.cfg.Path == "" {
		return "", "", 0, "This server wasn't started from a config file, so settings can't be saved."
	}
	fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		d.Set(st.table, st.key, lit)
		return nil
	})
	if err != nil {
		s.log.Printf("AdminServ: %v", err)
		return "", "", 0, "Not saved: " + err.Error()
	}
	st.apply(s.cfg, fresh)
	if st.after != nil {
		st.after(s, old)
	}
	now = st.get(s.cfg)
	id = s.recordBy(who, "set", st.name, old, now)
	return old, now, id, ""
}

func asShow(s *Server, c *Client, args []string) {
	all := settings()
	if len(args) > 0 {
		st, ok := findSetting(args[0])
		if !ok {
			s.as(c, "There is no setting %s. See them all: /as show", args[0])
			return
		}
		all = []setting{st}
	}
	s.as(c, "Settings of %s:", s.cfg.Server.Name)
	for _, st := range all {
		line := fmt.Sprintf("  %-16s %-12s", st.name, st.get(s.cfg))
		if st.help != "" {
			line += " " + st.help
		}
		s.as(c, "%s", strings.TrimRight(line, " "))
	}
	if len(args) == 0 {
		s.as(c, "Change one: /as set <setting> <value>. Ports: /as port list. MOTD: /as motd show")
	}
}

func asSet(s *Server, c *Client, args []string) {
	if len(args) < 2 {
		s.as(c, "Syntax: SET <setting> <value>. See them all: /as show")
		return
	}
	s.applySetting(c, s.adminServ, args[0], strings.Join(args[1:], " "))
}

// MOTD

func asMotd(s *Server, c *Client, args []string) {
	sub := strings.ToUpper(argAt(args, 0))
	lines := append([]string(nil), s.cfg.MOTDLines()...)
	before := strings.Join(lines, "\n")
	text := strings.Join(args[min(1, len(args)):], " ")
	switch sub {
	case "SHOW", "":
		if len(lines) == 0 {
			s.as(c, "The MOTD is empty.")
			return
		}
		for i, l := range lines {
			s.as(c, "  %-3d %s", i+1, l)
		}
		s.as(c, "%s.", plural(len(lines), "line"))
		return
	case "SET":
		if text == "" {
			s.as(c, "Syntax: MOTD SET <text>")
			return
		}
		lines = []string{text}
	case "ADD":
		if text == "" {
			s.as(c, "Syntax: MOTD ADD <text>")
			return
		}
		if len(lines) >= 100 {
			s.as(c, "The MOTD has 100 lines, the most.")
			return
		}
		lines = append(lines, text)
	case "DEL":
		n, err := strconv.Atoi(argAt(args, 1))
		if err != nil || n < 1 || n > len(lines) {
			s.as(c, "Syntax: MOTD DEL <number> (see /as motd show)")
			return
		}
		lines = append(lines[:n-1], lines[n:]...)
	case "CLEAR":
		lines = nil
	default:
		s.as(c, "Syntax: MOTD SHOW|SET|ADD|DEL|CLEAR [text]")
		return
	}
	if err := s.writeMotd(lines); err != nil {
		s.log.Printf("AdminServ: %v", err)
		s.as(c, "Not saved: %v", err)
		return
	}
	s.audit(c, s.adminServ, "MOTD %s", strings.TrimSpace(sub+" "+text))
	s.record(c, "motd", "motd", before, strings.Join(lines, "\n"))
	s.svc.LogAdmin(c.uid, s.adminServ, strings.TrimSpace(sub+" "+text))
	s.as(c, "The MOTD has %s now. See it: /motd", plural(len(lines), "line"))
}

// writeMotd saves the MOTD file, creating motd.txt next to the config when
// there is no file yet.
func (s *Server) writeMotd(lines []string) error {
	path := s.cfg.MOTD.File
	if path == "" {
		if s.cfg.Path == "" {
			return fmt.Errorf("no config file to save next to")
		}
		path = filepath.Join(filepath.Dir(s.cfg.Path), "motd.txt")
	}
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if s.cfg.MOTD.File == "" {
		if _, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
			d.Set("motd", "file", config.Quote("motd.txt"))
			return nil
		}); err != nil {
			return err
		}
		s.cfg.MOTD.File = path
	}
	return nil
}

// Ports

// portAddress turns "6697" into an address on the first port's host.
func (s *Server) portAddress(arg string) (string, bool) {
	if _, err := strconv.Atoi(arg); err == nil {
		host := ""
		s.lmu.Lock()
		if len(s.listeners) > 0 {
			host, _, _ = net.SplitHostPort(s.listeners[0].addr)
		}
		s.lmu.Unlock()
		return net.JoinHostPort(host, arg), true
	}
	if _, port, err := net.SplitHostPort(arg); err == nil {
		if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 65536 {
			return arg, true
		}
	}
	return "", false
}

func samePort(l listener, addr string) bool {
	if l.addr == addr {
		return true
	}
	_, p1, _ := net.SplitHostPort(l.addr)
	_, p2, _ := net.SplitHostPort(addr)
	h2, _, _ := net.SplitHostPort(addr)
	return h2 == "" && p1 == p2
}

func asPort(s *Server, c *Client, args []string) {
	sub := strings.ToUpper(argAt(args, 0))
	switch sub {
	case "LIST", "":
		s.lmu.Lock()
		ls := append([]listener(nil), s.listeners...)
		s.lmu.Unlock()
		s.as(c, "Open ports:")
		for _, l := range ls {
			kind := "plain"
			if l.tls {
				kind = "TLS"
			}
			s.as(c, "  %-28s %s", l.addr, kind)
		}
		s.as(c, "%s.", plural(len(ls), "port"))
	case "ADD":
		addr, ok := s.portAddress(argAt(args, 1))
		useTLS := strings.EqualFold(argAt(args, 2), "TLS")
		if !ok || argAt(args, 2) != "" && !useTLS {
			s.as(c, "Syntax: PORT ADD <port|address:port> [TLS]")
			return
		}
		s.lmu.Lock()
		for _, l := range s.listeners {
			if l.addr == addr {
				s.lmu.Unlock()
				s.as(c, "%s is open already.", addr)
				return
			}
		}
		s.lmu.Unlock()
		if useTLS && s.tlsConf == nil {
			if s.cfg.TLS.Cert == "" || s.cfg.TLS.Key == "" {
				s.as(c, "A TLS port needs a certificate. Set [tls] cert and key in beeircd.conf first.")
				return
			}
			certs, err := tlsutil.NewReloader(s.cfg.TLS.Cert, s.cfg.TLS.Key, s.cfg.TLS.SelfSigned, s.cfg.Server.Name)
			if err != nil {
				s.as(c, "The certificate didn't load: %v", err)
				return
			}
			s.certs, s.certChecked, s.tlsConf = certs, time.Now(), certs.Config()
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			s.as(c, "Can't open %s: %v", addr, err)
			return
		}
		fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
			d.AddListen(addr, useTLS)
			return nil
		})
		if err != nil {
			ln.Close()
			s.as(c, "Not saved, so the port stays closed: %v", err)
			return
		}
		l := listener{Listener: ln, tls: useTLS, addr: addr, tlsConf: s.tlsConf}
		s.lmu.Lock()
		s.listeners = append(s.listeners, l)
		s.lmu.Unlock()
		s.cfg.Listen = fresh.Listen
		s.wg.Add(1)
		go s.acceptLoop(l)
		kind := "plain"
		if useTLS {
			kind = "TLS"
		}
		s.audit(c, s.adminServ, "PORT ADD %s %s", addr, kind)
		s.record(c, "port", "ADD "+addr+" "+kind, "", addr+" "+kind)
		s.svc.LogAdmin(c.uid, s.adminServ, "ADD "+addr+" "+kind)
		s.as(c, "%s is open (%s) and saved.", addr, kind)
	case "DEL":
		arg := argAt(args, 1)
		addr, ok := s.portAddress(arg)
		if !ok {
			s.as(c, "Syntax: PORT DEL <port|address:port>")
			return
		}
		if _, err := strconv.Atoi(arg); err == nil {
			addr = ":" + arg // any host with this port
		}
		s.lmu.Lock()
		at := -1
		for i, l := range s.listeners {
			if samePort(l, addr) {
				at = i
				break
			}
		}
		if at < 0 || len(s.listeners) == 1 {
			s.lmu.Unlock()
			if at < 0 {
				s.as(c, "No open port %s. See them: /as port list", arg)
			} else {
				s.as(c, "That is the last open port. Add another one first.")
			}
			return
		}
		l := s.listeners[at]
		s.lmu.Unlock()
		fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
			if !d.DelListen(l.addr) {
				return fmt.Errorf("%s isn't in the config file", l.addr)
			}
			return nil
		})
		if err != nil {
			s.as(c, "Not saved, so the port stays open: %v", err)
			return
		}
		s.lmu.Lock()
		s.listeners = append(s.listeners[:at:at], s.listeners[at+1:]...)
		s.lmu.Unlock()
		l.Close()
		s.cfg.Listen = fresh.Listen
		s.audit(c, s.adminServ, "PORT DEL %s", l.addr)
		s.record(c, "port", "DEL "+l.addr, l.addr, "")
		s.svc.LogAdmin(c.uid, s.adminServ, "DEL "+l.addr)
		s.as(c, "%s is closed and saved. People connected through it stay.", l.addr)
	default:
		s.as(c, "Syntax: PORT ADD|DEL|LIST [port] [TLS]")
	}
}

// Opers

// operMask turns "host" into "*@host" and checks the form.
func operMask(m string) (string, bool) {
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" || strings.ContainsAny(m, "! ") {
		return "", false
	}
	if !strings.Contains(m, "@") {
		m = "*@" + m
	}
	user, host, _ := strings.Cut(m, "@")
	return m, user != "" && host != "" && !strings.Contains(host, "@")
}

func operMasks(args []string) ([]string, bool) {
	var out []string
	for _, a := range args {
		for _, p := range strings.Split(a, ",") {
			if p == "" {
				continue
			}
			m, ok := operMask(p)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
	}
	return out, len(out) > 0 && len(out) <= 20
}

func asOper(s *Server, c *Client, args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "LIST") {
		asOperList(s, c)
		return
	}
	if len(args) < 2 {
		s.as(c, "Syntax: OPER LIST | OPER <nick> ADD|SET|DEL ... (/as help oper)")
		return
	}
	a, err := s.store.AccountByNick(args[0])
	if err != nil {
		s.dbErr(c, s.adminServ, err)
		return
	}
	if a == nil {
		s.as(c, "%s isn't a registered nick. They register first: /ns register <password>", args[0])
		return
	}
	if s.isAdminAccount(a) {
		if strings.EqualFold(args[1], "SET") && strings.EqualFold(argAt(args, 2), "PASSWORD") && c.operAccount == a.ID {
			pw := argAt(args, 3)
			if len(pw) < minOperPassword {
				s.as(c, "Syntax: OPER %s SET PASSWORD <password> (at least %d characters)", a.Name, minOperPassword)
				return
			}
			s.hashPassword(c, pw, func(hash string) {
				if err := s.saveAdminPassword(hash); err != nil {
					s.as(c, "Not saved: %v", err)
					return
				}
				s.audit(c, s.adminServ, "OPER %s SET PASSWORD", a.Name)
				s.svc.LogAdmin(c.uid, s.adminServ, "to change the oper password of "+a.Name)
				s.as(c, "Your oper password is changed and saved to beeircd.conf.")
			})
			return
		}
		s.as(c, "%s is the server admin from beeircd.conf; change it there.", a.Name)
		return
	}
	cur, err := s.store.OperByAccount(a.ID)
	if err != nil {
		s.dbErr(c, s.adminServ, err)
		return
	}
	before := encodeOper(cur)
	// flagsOK checks that c only gives flags it has itself.
	flagsOK := func(raw string) (string, bool) {
		if strings.EqualFold(raw, "OFF") || raw == "-" {
			return "", true
		}
		flags, bad := cleanFlags(strings.TrimPrefix(raw, "+"))
		switch {
		case bad != 0:
			s.as(c, "%c isn't an oper flag. Flags: a admin, c links, g global, h hosts, k kill, s services.", bad)
			return "", false
		case flags == "":
			s.as(c, "Give at least one flag, like ghks.")
			return "", false
		}
		for i := 0; i < len(flags); i++ {
			if !c.can(flags[i]) {
				s.as(c, "You can't give flag %c: you don't have it yourself.", flags[i])
				return "", false
			}
		}
		return flags, true
	}
	// classOK checks a class exists and c has all of its flags.
	classOK := func(name string) (string, bool) {
		oc, ok := s.cfg.Class(name)
		if !ok {
			s.as(c, "There is no class %s. See them: /as class list", name)
			return "", false
		}
		for i := 0; i < len(oc.Flags); i++ {
			if !c.can(oc.Flags[i]) {
				s.as(c, "You can't give class %s: it has flag %c and you don't.", oc.Name, oc.Flags[i])
				return "", false
			}
		}
		return oc.Name, true
	}
	switch strings.ToUpper(args[1]) {
	case "DISABLE", "ENABLE":
		if cur == nil {
			s.as(c, "%s isn't an oper.", a.Name)
			return
		}
		if strings.EqualFold(args[1], "DISABLE") {
			cur.Disabled = irc.Truncate(strings.Join(args[2:], " "), 100)
			if cur.Disabled == "" {
				cur.Disabled = "disabled by " + c.user.Nick
			}
			cur.DisabledBy = c.user.Nick
		} else {
			if cur.Disabled == "" {
				s.as(c, "%s isn't disabled.", a.Name)
				return
			}
			cur.Disabled, cur.DisabledBy = "", ""
		}
		if err := s.store.SaveOper(cur); err != nil {
			s.dbErr(c, s.adminServ, err)
			return
		}
		verb := strings.ToUpper(args[1])
		s.record(c, "oper", a.Name, before, encodeOper(cur))
		s.audit(c, s.adminServ, "OPER %s %s", a.Name, verb)
		s.svc.LogAdmin(c.uid, s.adminServ, strings.ToLower(verb)+" "+a.Name)
		s.recheckOpers(a.ID)
		if cur.Disabled != "" {
			s.as(c, "%s may not /oper now (%s). Undo: /as oper %s enable", a.Name, cur.Disabled, a.Name)
		} else {
			s.as(c, "%s may /oper again.", a.Name)
		}
	case "ADD":
		if cur != nil {
			s.as(c, "%s is an oper already (%s). Change it: /as oper %s set flags <letters>", a.Name, cur.Flags, a.Name)
			return
		}
		var flagArg, classArg, title, pass string
		var hostArgs []string
		for i := 2; i < len(args); i++ {
			switch strings.ToUpper(args[i]) {
			case "CLASS":
				if i+1 < len(args) {
					classArg = args[i+1]
					i++
				}
			case "FLAGS":
				if i+1 < len(args) {
					flagArg = args[i+1]
					i++
				}
			case "PASSWORD":
				if i+1 < len(args) {
					pass = args[i+1]
					i++
				}
			case "HOST":
				for i+1 < len(args) && !strings.EqualFold(args[i+1], "TITLE") && !strings.EqualFold(args[i+1], "FLAGS") && !strings.EqualFold(args[i+1], "PASSWORD") && !strings.EqualFold(args[i+1], "CLASS") {
					hostArgs = append(hostArgs, args[i+1])
					i++
				}
			case "TITLE":
				title = strings.Join(args[i+1:], " ")
				i = len(args)
			}
		}
		if flagArg == "" && classArg == "" || len(hostArgs) == 0 {
			s.as(c, "Syntax: OPER <nick> ADD CLASS <class>|FLAGS <letters> HOST <mask> [PASSWORD <password>] [TITLE <text>]")
			return
		}
		class := ""
		if classArg != "" {
			var ok bool
			if class, ok = classOK(classArg); !ok {
				return
			}
		}
		if pass != "" && len(pass) < minOperPassword {
			s.as(c, "An oper password needs at least %d characters.", minOperPassword)
			return
		}
		flags := ""
		if flagArg != "" {
			var ok bool
			if flags, ok = flagsOK(flagArg); !ok {
				return
			}
		}
		hosts, ok := operMasks(hostArgs)
		if !ok {
			s.as(c, "A host mask looks like *@192.168.50.* or ~ember@*.example.net (up to 20).")
			return
		}
		o := &store.Oper{AccountID: a.ID, Account: a.Name, Class: class, Flags: flags, Title: irc.Truncate(title, 50), Hosts: hosts, AddedBy: c.user.Nick, AddedAt: time.Now()}
		finish := func() {
			if err := s.store.SaveOper(o); err != nil {
				s.dbErr(c, s.adminServ, err)
				return
			}
			audited := ""
			if class != "" {
				audited += "class " + class + " "
			}
			if flags != "" {
				audited += "flags " + flags + " "
			}
			s.audit(c, s.adminServ, "OPER %s ADD %shosts %s", a.Name, audited, strings.Join(hosts, " "))
			s.record(c, "oper", a.Name, "", encodeOper(o))
			s.svc.LogAdmin(c.uid, s.adminServ, "ADD "+a.Name+" with flags "+flags) // os_oper
			what := describeFlags(s.viewOf(o).flags)
			if class != "" {
				what = "class " + class + ", " + what
			}
			if o.PassHash == "" {
				s.as(c, "%s is an oper now: %s, from %s. Set their oper password: /as oper %s set password <password>", a.Name, what, strings.Join(hosts, " "), a.Name)
			} else {
				s.as(c, "%s is an oper now: %s, from %s. They oper up with /oper %s <password>", a.Name, what, strings.Join(hosts, " "), a.Name)
			}
		}
		if pass == "" {
			finish()
			return
		}
		s.hashPassword(c, pass, func(hash string) {
			o.PassHash = hash
			finish()
		})
	case "SET":
		if cur == nil {
			s.as(c, "%s isn't an oper. Add them: /as oper %s add class <class> host <mask>", a.Name, a.Name)
			return
		}
		what, rest := strings.ToUpper(argAt(args, 2)), args[min(3, len(args)):]
		if len(rest) == 0 {
			s.as(c, "Syntax: OPER <nick> SET CLASS|FLAGS|HOST|PASSWORD|TITLE|VHOST|SWHOIS <value>")
			return
		}
		if what == "PASSWORD" {
			if len(rest[0]) < minOperPassword {
				s.as(c, "An oper password needs at least %d characters.", minOperPassword)
				return
			}
			s.hashPassword(c, rest[0], func(hash string) {
				fresh, err := s.store.OperByAccount(a.ID)
				if err != nil || fresh == nil {
					s.as(c, "%s isn't an oper any more.", a.Name)
					return
				}
				fresh.PassHash = hash
				if err := s.store.SaveOper(fresh); err != nil {
					s.dbErr(c, s.adminServ, err)
					return
				}
				s.svc.ClearLockout(operLockKey(a.ID))
				s.record(c, "oper", a.Name, "password", "password")
				s.audit(c, s.adminServ, "OPER %s SET PASSWORD", a.Name)
				s.svc.LogAdmin(c.uid, s.adminServ, "to change the oper password of "+a.Name)
				s.as(c, "%s has a new oper password.", a.Name)
			})
			return
		}
		val := strings.Join(rest, " ")
		switch what {
		case "CLASS":
			if strings.EqualFold(val, "OFF") {
				if cur.Flags == "" {
					s.as(c, "Give %s flags first (/as oper %s set flags <letters>), or another class.", a.Name, a.Name)
					return
				}
				cur.Class = ""
			} else {
				class, ok := classOK(val)
				if !ok {
					return
				}
				cur.Class = class
			}
		case "VHOST":
			if strings.EqualFold(val, "OFF") {
				cur.Vhost = ""
			} else if !s.validOperVhost(val) {
				s.as(c, "A vhost is a hostname like staff.example.org.")
				return
			} else {
				cur.Vhost = val
			}
		case "SWHOIS":
			if strings.EqualFold(val, "OFF") {
				cur.Swhois = nil
			} else if lines, ok := splitSwhois(val); ok {
				cur.Swhois = lines
			} else {
				s.as(c, "Up to 5 swhois lines of 100 characters, split with |.")
				return
			}
		case "FLAGS":
			flags, ok := flagsOK(rest[0])
			if !ok {
				return
			}
			if flags == "" && cur.Class == "" {
				s.as(c, "Without a class, %s needs flags.", a.Name)
				return
			}
			for i := 0; i < len(cur.Flags); i++ {
				if !c.can(cur.Flags[i]) && strings.IndexByte(flags, cur.Flags[i]) < 0 {
					s.as(c, "You can't take flag %c away: you don't have it yourself.", cur.Flags[i])
					return
				}
			}
			cur.Flags = flags
		case "HOST":
			hosts, ok := operMasks(rest)
			if !ok {
				s.as(c, "A host mask looks like *@192.168.50.* or ~ember@*.example.net (up to 20).")
				return
			}
			cur.Hosts = hosts
		case "TITLE":
			cur.Title = irc.Truncate(strings.Join(rest, " "), 50)
			if strings.EqualFold(cur.Title, "OFF") {
				cur.Title = ""
			}
		default:
			s.as(c, "Syntax: OPER <nick> SET CLASS|FLAGS|HOST|PASSWORD|TITLE|VHOST|SWHOIS <value>")
			return
		}
		if err := s.store.SaveOper(cur); err != nil {
			s.dbErr(c, s.adminServ, err)
			return
		}
		s.audit(c, s.adminServ, "OPER %s SET %s %s", a.Name, what, strings.Join(rest, " "))
		s.record(c, "oper", a.Name, before, encodeOper(cur))
		s.svc.LogAdmin(c.uid, s.adminServ, "to change the "+strings.ToLower(what)+" of "+a.Name+" to "+strings.Join(rest, " "))
		s.recheckOpers(a.ID)
		v := s.viewOf(cur)
		reply := a.Name + ": "
		if cur.Class != "" {
			reply += "class " + cur.Class + ", "
		}
		reply += "flags " + orNone(v.flags) + ", hosts " + strings.Join(cur.Hosts, " ") + ", title " + orNone(v.title)
		if v.vhost != "" {
			reply += ", vhost " + v.vhost
		}
		s.as(c, "%s.", reply)
	case "DEL":
		if cur == nil {
			s.as(c, "%s isn't an oper.", a.Name)
			return
		}
		if eff := s.viewOf(cur).flags; true {
			for i := 0; i < len(eff); i++ {
				if !c.can(eff[i]) {
					s.as(c, "You can't remove %s: they have flag %c and you don't.", a.Name, eff[i])
					return
				}
			}
		}
		if _, err := s.store.DeleteOper(a.ID); err != nil {
			s.dbErr(c, s.adminServ, err)
			return
		}
		s.audit(c, s.adminServ, "OPER %s DEL", a.Name)
		s.record(c, "oper", a.Name, before, "")
		s.svc.LogAdmin(c.uid, s.adminServ, "DEL "+a.Name) // os_oper
		s.recheckOpers(a.ID)
		s.as(c, "%s is no longer an oper.", a.Name)
	default:
		s.as(c, "Syntax: OPER LIST | OPER <nick> ADD|SET|DEL|DISABLE|ENABLE ... (/as help oper)")
	}
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

func asOperList(s *Server, c *Client) {
	list, err := s.store.Opers()
	if err != nil {
		s.dbErr(c, s.adminServ, err)
		return
	}
	online := map[int64]bool{}
	for _, oc := range s.clients {
		if oc.isOper() {
			online[oc.operAccount] = true
		}
	}
	width := 6
	for _, o := range list {
		width = max(width, len(o.Account))
	}
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	s.as(c, "  %-*s  %-10s  %-6s  %-6s  %-8s  %s", width, "Nick", "Class", "Flags", "Online", "Password", "Hosts")
	if admin := s.cfg.Services.Admin; admin != "" {
		a, _ := s.store.AccountByNick(admin)
		where := "any (server admin from beeircd.conf)"
		if len(s.cfg.Services.AdminHosts) > 0 {
			where = strings.Join(s.adminHosts(), " ") + " (server admin from beeircd.conf)"
		}
		s.as(c, "  %-*s  %-10s  %-6s  %-6s  %-8s  %s", width, admin, "netadmin", allFlags, yesNo(a != nil && online[a.ID]), yesNo(s.cfg.Services.AdminPassword != ""), where)
	}
	for _, o := range list {
		hosts := strings.Join(o.Hosts, " ")
		if o.Disabled != "" {
			hosts += "  DISABLED: " + o.Disabled
		}
		s.as(c, "  %-*s  %-10s  %-6s  %-6s  %-8s  %s", width, o.Account, describeClass(o), orNone(s.viewOf(&o).flags), yesNo(online[o.AccountID]), yesNo(o.PassHash != ""), hosts)
	}
	n := len(list)
	if s.cfg.Services.Admin != "" {
		n++
	}
	s.as(c, "%s.", plural(n, "oper"))
}

func asServer(s *Server, c *Client, args []string) {
	switch strings.ToUpper(argAt(args, 0)) {
	case "", "LIST", "LINK", "UNLINK":
	case "ADD", "SET", "DEL":
		if !s.ours(c.uid) && !s.cfg.Link.RemoteAdmin {
			s.as(c, "Link blocks on %s are changed by people on it ([link] remote_admin = false).", s.cfg.Server.Name)
			return
		}
	default:
		if !s.needFlag(c, s.adminServ, 'a') {
			return
		}
		s.asServerOn(c, args[0], args[1:])
		return
	}
	s.serverCmd(c, s.adminServ, args)
}

// RESTART and DIE

func asStop(action int) func(*Server, *Client, []string) {
	word := "RESTART"
	if action == stopDie {
		word = "DIE"
	}
	return func(s *Server, c *Client, args []string) {
		key := strings.ToLower(word) + ":"
		code, reason := "", ""
		if len(args) > 0 && c.confirm == key+args[0] && time.Now().Before(c.confirmTill) {
			code, reason = args[0], strings.Join(args[1:], " ")
		}
		if code == "" {
			fresh := randomCode()
			c.confirm, c.confirmTill = key+fresh, time.Now().Add(confirmFor)
			people := s.localUsers()
			what := "restarts the server"
			if action == stopDie {
				what = "stops the server"
			}
			s.as(c, "This %s and disconnects %s.", what, plural(people, "person"))
			s.as(c, "To go ahead within 2 minutes: /as %s %s [reason]", strings.ToLower(word), fresh)
			return
		}
		c.confirm = ""
		s.audit(c, s.adminServ, "%s %s", word, reason)
		s.svc.LogAdmin(c.uid, s.adminServ, "") // os_shutdown
		s.as(c, "Going now.")
		s.stopAfter, s.stopReason = action, irc.Truncate(reason, 200)
	}
}
