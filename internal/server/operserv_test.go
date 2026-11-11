package server

import (
	"context"
	"errors"
	"github.com/fhazal/beeircd/internal/store"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

const fileConf = `# BeeIRCd test config
[server]
name = "irc.test.local"
network = "BeeNet"
sid = "0BE"
resolve_hosts = false

[[listen]]
address = "127.0.0.1:0"

[[listen]]
address = "127.0.0.1:0"
tls = true

[tls]
cert = "cert.pem"
key = "key.pem"
self_signed = true

[motd]
text = "Welcome to BeeNet"

[cloak]
enabled = false
secret = "0123456789abcdef-test"

# room for fast test clients
[limits]
flood_burst = 10000
flood_rate = 10000
session_limit = 100

[services]
database = "services.db"
admin = "Boss"
reg_delay = "0s"
`

// fileServer starts a server from a real config file (AdminServ writes to
// it). extra is added to the file. It returns the server, the file and a
// channel that gets Serve's result.
func fileServer(t *testing.T, extra string) (*Server, string, chan error) {
	t.Helper()
	return fileServerWith(t, extra, nil)
}

// fileServerWith also lets setup change the server before it starts.
func fileServerWith(t *testing.T, extra string, setup func(*Server)) (*Server, string, chan error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "beeircd.conf")
	if err := os.WriteFile(path, []byte(fileConf+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result:
		case <-time.After(5 * time.Second):
		}
	})
	return s, path, result
}

func osLine(nick, text string) string {
	return ":OperServ!services@irc.test.local NOTICE " + nick + " :" + text
}

func asLine(nick, text string) string {
	return ":AdminServ!services@irc.test.local NOTICE " + nick + " :" + text
}

const operPW = "Hive-2026-secret"

// boss registers the admin account from the config; its first /oper sets
// the admin oper password (saved to beeircd.conf) and opers up.
func boss(t *testing.T, s *Server) *tc {
	t.Helper()
	b := registered(t, s, "Boss")
	b.quiet("MODE Boss +o") // identifying alone never opers anyone
	b.send("OPER Boss " + operPW)
	b.expect("Your oper password is set and saved to beeircd.conf.")
	b.expect(":irc.test.local MODE Boss +os")
	b.expect(" 381 Boss :You are now an IRC operator")
	b.expect("Oper flags: acghks (admin, links, global, hosts, kill, services)")
	return b
}

// code pulls the confirm code out of "... /as restart abc123 [reason]".
func code(line string) string {
	m := regexp.MustCompile(` (\S+) \[reason\]$|drop (\S+)$`).FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

func TestOperUpAndWhois(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)
	ember := login(t, s, "ember")

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `admin_password = "$argon2id$`) {
		t.Errorf("admin oper password not saved:\n%s", raw)
	}
	ember.send("WHOIS Boss")
	ember.expect(" 313 ember Boss :is an IRC Operator (Server Admin)")
	ember.send("OS STATS")
	ember.expect(osLine("ember", "OperServ is for IRC operators."))
	ember.send("AS SHOW")
	ember.expect(asLine("ember", "AdminServ is for server admins (oper flag a)."))
	ember.send("KILL Boss bye")
	ember.expect(" 481 ember :Permission denied: you're not an IRC operator")
	ember.send("MODE ember +s")
	ember.sync()
	ember.send("MODE ember")
	ember.expect(" 221 ember +i")
	ember.send("OPER Boss guess-the-pass")
	ember.expect(" 464 ember :Password incorrect")
	b.expect("Notice -- Failed /oper as Boss from ember (~ember@127.0.0.1): wrong password")
	ember.send("OPER nobody " + operPW)
	ember.expect(" 491 ember :No oper entry for you from this host")

	// Opers with h see real hosts and hidden channels.
	ember.send("JOIN #secret")
	ember.send("MODE #secret +s")
	ember.sync()
	b.send("WHOIS ember")
	b.expect(" 319 Boss ember :@#secret")
	b.expect(" 378 Boss ember :is connecting from *@127.0.0.1 127.0.0.1")
	b.send("LIST")
	b.expect(" 322 Boss #secret 1 ")

	b.send("OS STATS")
	b.expect(osLine("Boss", "  People     2 now, peak 2"))
	b.expect(osLine("Boss", "  Opers      1 online"))

	// Oper status doesn't depend on NickServ: logging out keeps it.
	b.send("NS LOGOUT")
	b.expect(" 901 Boss ")
	b.send("OS STATS")
	b.expect(osLine("Boss", "  Opers      1 online"))
	// Dropping your own +o ends it; /oper brings it back.
	b.send("MODE Boss -o")
	b.expect(":Boss MODE Boss -os")
	b.send("OS STATS")
	b.expect(osLine("Boss", "OperServ is for IRC operators."))
	b.send("OPER Boss wrong-password")
	b.expect(" 464 Boss :Password incorrect")
	b.send("OPER Boss " + operPW)
	b.expect(":irc.test.local MODE Boss +os")
	b.send("AS OPER Boss SET PASSWORD New-Hive-2027")
	b.expect(asLine("Boss", "Your oper password is changed and saved to beeircd.conf."))
}

func TestOperEntries(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	ember := registered(t, s, "ember")
	ember.send("NS LOGOUT")
	ember.expect(" 901 ember ")

	b.send("AS OPER nobody ADD FLAGS g HOST *@*")
	b.expect(asLine("Boss", "nobody isn't a registered nick. They register first: /ns register <password>"))
	b.send("AS OPER ember ADD FLAGS gkx HOST *@127.0.0.1")
	b.expect(asLine("Boss", "x isn't an oper flag."))
	b.send("AS OPER ember ADD FLAGS gk HOST *@127.0.0.1 TITLE Night Owl")
	b.expect("Notice -- Boss used AdminServ: OPER ember ADD flags gk hosts *@127.0.0.1")
	b.expect(asLine("Boss", "ember is an oper now: gk (global, kill), from *@127.0.0.1. Set their oper password: /as oper ember set password <password>"))
	b.send("AS OPER Boss SET FLAGS g")
	b.expect(asLine("Boss", "Boss is the server admin from beeircd.conf; change it there."))

	// No oper password yet: /oper says how to get one.
	ember.send("OPER ember " + pw)
	ember.expect(" 491 ember :No oper password is set for ember yet")
	ember.expect("An admin sets it: /as oper ember set password <password>")
	b.send("AS OPER ember SET PASSWORD short")
	b.expect(asLine("Boss", "An oper password needs at least 8 characters."))
	b.send("AS OPER ember SET PASSWORD Ember-oper-1")
	b.expect(asLine("Boss", "ember has a new oper password."))

	// The oper password is its own; the NickServ one doesn't work, and
	// identifying isn't needed.
	ember.send("OPER ember " + pw)
	ember.expect(" 464 ember :Password incorrect")
	b.expect("Notice -- Failed /oper as ember from ember (~ember@127.0.0.1): wrong password")
	ember.send("OPER ember Ember-oper-1")
	ember.expect(":irc.test.local MODE ember +os")
	ember.expect(" 381 ember :You are now an IRC operator")
	b.expect("Notice -- ember (~ember@127.0.0.1) is now an operator as ember: gk")

	ember.send("AS SHOW")
	ember.expect(asLine("ember", "AdminServ is for server admins (oper flag a)."))
	ember.send("OS FORBID LIST")
	ember.expect(osLine("ember", "You need oper flag s (services) for that."))
	ember.send("OS HELP")
	ember.expect(osLine("ember", "OperServ is the toolbox for IRC operators. Commands:"))

	b.send("AS OPER LIST")
	b.expect(asLine("Boss", "  Nick    Flags   Online  Password  Hosts"))
	b.expect(asLine("Boss", "  Boss    acghks  yes     yes       any (server admin from beeircd.conf)"))
	b.expect(asLine("Boss", "  ember   gk      yes     yes       *@127.0.0.1"))
	b.expect(asLine("Boss", "2 opers."))

	// Changes reach an oper at once; a host that no longer matches ends it.
	b.send("AS OPER ember SET FLAGS ghk")
	ember.expect("Your oper flags are now ghk (global, hosts, kill)")
	ember.send("WHOIS ember")
	ember.expect(" 313 ember ember :is an IRC Operator (Night Owl)")
	b.send("AS OPER ember SET HOST *@10.0.0.*")
	ember.expect(":irc.test.local MODE ember -os")
	b.expect(asLine("Boss", "ember: flags ghk, hosts *@10.0.0.*, title Night Owl."))
	ember.send("OPER ember Ember-oper-1")
	ember.expect(" 491 ember :No oper entry for you from this host")
	b.send("AS OPER ember SET HOST 127.0.0.1")
	b.expect(asLine("Boss", "ember: flags ghk, hosts *@127.0.0.1, title Night Owl."))
	ember.send("OPER ember Ember-oper-1")
	ember.expect(":irc.test.local MODE ember +os")
	b.send("AS OPER ember DEL")
	b.expect(asLine("Boss", "ember is no longer an oper."))
	ember.expect(":irc.test.local MODE ember -os")

	// Five wrong oper passwords lock /oper for that entry.
	b.send("AS OPER ember ADD FLAGS g HOST 127.0.0.1 PASSWORD Ember-oper-2")
	b.expect(asLine("Boss", "ember is an oper now: g (global), from *@127.0.0.1. They oper up with /oper ember <password>"))
	for i := 0; i < 5; i++ {
		ember.send("OPER ember nope-nope-nope")
		ember.expect(" 464 ember :Password incorrect")
	}
	ember.send("OPER ember Ember-oper-2")
	ember.expect(" 464 ember :Too many wrong passwords. Try again in 10 minutes")
}

func TestKillWallopsGlobalNews(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	victim := login(t, s, "victim")
	watcher := login(t, s, "watcher")
	watcher.send("MODE watcher +w")
	watcher.sync()

	b.send("KILL NickServ x")
	b.expect(" 483 Boss :You can't kill a service")
	b.send("KILL victim :go away")
	victim.expect(":Boss KILL victim :go away")
	victim.expect("ERROR :Closing Link: 127.0.0.1 (Killed (Boss (go away)))")
	b.expect("Notice -- Boss killed victim (~victim@127.0.0.1): go away")

	b.send("WALLOPS :opers meeting at nine")
	watcher.expect(":Boss!~boss@127.0.0.1 WALLOPS :opers meeting at nine")

	b.send("OS GLOBAL Maintenance at 22:00 UTC")
	watcher.expect(":Global!services@irc.test.local NOTICE watcher :Maintenance at 22:00 UTC")
	b.expect(osLine("Boss", "Sent to everyone online (2 people)."))

	b.send("OS NEWS ADD Welcome to the new server") // NEWS is the old name of LOGONNEWS
	b.expect(osLine("Boss", "Added new logon news item. Everyone sees the newest 3 when they connect, from Global."))
	late := raw(t, s)
	late.send("NICK late")
	late.send("USER late 0 * :Late")
	late.expect(" 376 late ")
	if l := late.expect(":Global!services@irc.test.local NOTICE late :["); !strings.Contains(l, "Logon News") || !strings.HasSuffix(l, "] Welcome to the new server") {
		t.Fatalf("logon news line: %q", l)
	}
	b.send("OS LOGONNEWS LIST")
	b.expect("  1   Welcome to the new server  by Boss")
	b.send("OS LOGONNEWS DEL 1")
	b.expect(osLine("Boss", "Logon news item #1 deleted."))

	victim2 := login(t, s, "victim2")
	b.send("OS KILL victim2 flooding")
	victim2.expect(":OperServ KILL victim2 :flooding [Boss]")
	victim3 := login(t, s, "victim3")
	b.send("OS KILL victim3") // the reason is optional
	victim3.expect(":OperServ KILL victim3 :No reason [Boss]")
}

func TestAkill(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bot := login(t, s, "bot")

	b.send("OS AKILL ADD +1h *@127.0.0.1 nope")
	b.expect(osLine("Boss", "*@127.0.0.1 matches you. Give a narrower one."))
	b.send("OS AKILL ADD *@* nope")
	b.expect(osLine("Boss", "That mask matches everyone. Give a narrower one."))
	b.send("OS AKILL ADD ghost nope")
	b.expect(osLine("Boss", "ghost isn't online. Give a mask like *@203.0.113.5 instead."))
	b.send("OS AKILL ADD +soon ~bot@127.0.0.* spam")
	b.expect(osLine("Boss", "+soon isn't a time. Use +30m, +12h, +7d, or +0 for until removed."))
	b.send("OS AKILL ADD +2h ~bot@127.0.0.* spam bots")
	bot.expect(" 465 bot :You are banned from this network: spam bots")
	bot.expect("ERROR :Closing Link: 127.0.0.1 (Banned: spam bots)")
	b.expect(osLine("Boss", "AKILL added: ~bot@127.0.0.* for 2 hours (spam bots). 1 person removed."))

	again := raw(t, s)
	again.send("NICK bot")
	again.send("USER bot 0 * :Bot")
	again.expect(" 465 bot :You are banned from this network: spam bots")

	b.send("OS AKILL LIST")
	b.expect("  1   ~bot@127.0.0.*  by Boss · 2 hours left · spam bots")
	b.send("OS AKILL VIEW")
	b.expect("  1   ~bot@127.0.0.*  set by Boss, ")
	b.send("OS AKILL DEL 1")
	b.expect(osLine("Boss", "AKILL on ~bot@127.0.0.* removed."))
	login(t, s, "bot")

	b.send("OS AKILL ADD *@198.51.100.1 one")
	b.expect("AKILL added: *@198.51.100.1 for 30 days (one).")
	b.send("OS AKILL ADD *@198.51.100.2 two")
	b.expect("AKILL added")
	b.send("OS AKILL ADD *@198.51.100.3 three")
	b.expect("AKILL added")
	b.send("OS AKILL DEL 1-2")
	b.expect(osLine("Boss", "2 AKILLs removed: *@198.51.100.1, *@198.51.100.2."))
	b.send("OS AKILL CLEAR")
	b.expect(osLine("Boss", "Every AKILL is removed (1)."))

	// Expired AKILLs stop working and get cleaned up.
	b.send("OS AKILL ADD +1m ~old@127.0.0.* old")
	b.expect("AKILL added")
	done := make(chan struct{})
	s.post(func() {
		// Make it run out, then let the hourly clean-up find it.
		s.store.AddNetBan(store.NetBan{Kind: store.BanAkill, Mask: "~old@127.0.0.*", Reason: "old", SetBy: "Boss", SetAt: time.Now().Add(-2 * time.Minute), Expires: time.Now().Add(-time.Second)})
		s.svc.Expire(time.Now())
		close(done)
	})
	<-done
	b.expect("AKILL on ~old@127.0.0.* ran out")
	login(t, s, "old")
}

func TestSessionLimit(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.post(func() { s.cfg.Limits.SessionLimit = 3 })
	b := boss(t, s) // 1
	login(t, s, "two")
	login(t, s, "three")
	fourth := raw(t, s)
	fourth.expect("ERROR :Closing Link: 127.0.0.1 (Too many connections from your IP (the limit is 3))")
	b.expect("Notice -- Too many connections from 127.0.0.1 (4, the limit is 3)")

	b.send("OS EXCEPTION ADD 127.0.0.* 5 test box")
	b.expect(osLine("Boss", "127.0.0.* may now have 5 connections."))
	login(t, s, "four")
	b.send("OS SESSION")
	b.expect(osLine("Boss", "Session limit: 3 connections per IP (1 exception). Change it: /as set session-limit <n>"))
	b.expect("127.0.0.1")
	b.send("OS SESSION LIST 4")
	b.expect(osLine("Boss", "IPs with 4 or more connections:"))
	b.expect("127.0.0.1")
	b.send("OS SESSION VIEW 127.0.0.1")
	b.expect(osLine("Boss", "127.0.0.1 has 4 of 5 connections."))
	b.send("OS EXCEPTION LIST")
	b.expect("  1   127.0.0.*            5    by Boss · test box")
	b.send("OS SESSION EXCEPT DEL 1") // the old form still works
	b.expect(osLine("Boss", "127.0.0.* is back to the normal limit (3)."))
	b.send("AS SET session-limit 6")
	b.expect(asLine("Boss", "session-limit is now 6 (was 3). Saved to beeircd.conf."))
	login(t, s, "five")
}

func TestForbids(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bad := login(t, s, "badnick")
	ember := login(t, s, "ember")
	joinAll("#warez", ember)

	b.send("OS FORBID ADD NICK badnick impersonation")
	bad.expect("That nick is forbidden by the network, so you are now Guest")
	b.expect(osLine("Boss", "badnick is forbidden now."))
	ember.send("NICK BadNick")
	ember.expect(" 432 ember BadNick :Forbidden by the network: impersonation")
	reg := registered(t, s, "taken")
	reg.sync()
	b.send("OS FORBID ADD NICK taken")
	b.expect(osLine("Boss", "taken is registered. Drop it first: /ns drop taken"))

	b.send("OS FORBID ADD CHAN #warez no piracy")
	ember.expect(":OperServ!services@irc.test.local KICK #warez ember :This channel is forbidden: no piracy")
	b.expect(osLine("Boss", "#warez is forbidden now (1 person kicked)."))
	ember.send("JOIN #warez")
	ember.expect(" 479 ember #warez :Cannot join channel (forbidden by the network: no piracy)")

	// Patterns, email and register-only forbids.
	b.send("OS FORBID ADD NICK spam* bots")
	b.expect("spam* is forbidden now.")
	ember.send("NICK spammer")
	ember.expect(" 432 ember spammer :Forbidden by the network: bots")
	b.send("OS FORBID ADD EMAIL *@mailinator.com throwaway")
	b.expect(osLine("Boss", "No account may use *@mailinator.com now."))
	ember.send("NS REGISTER honeypot42 bee@MAILINATOR.com")
	ember.expect(nsLine("ember", "That email can't be used here (throwaway)."))
	b.send("OS FORBID ADD REGISTER Guest* temporary nicks")
	b.expect(osLine("Boss", "Guest* can't be registered now."))
	ember.send("NICK Guest123")
	ember.expect("NICK Guest123")
	ember.send("NS REGISTER honeypot42")
	ember.expect(nsLine("Guest123", "Guest123 can't be registered here (temporary nicks)."))

	b.send("OS FORBID LIST")
	b.expect(osLine("Boss", "Forbidden:"))
	b.expect("#warez")
	b.expect("badnick")
	b.send("OS FORBID LIST EMAIL")
	b.expect("  EMAIL    *@mailinator.com")
	b.send("OS FORBID DEL CHAN #warez")
	b.expect(osLine("Boss", "#warez can be used again."))
	b.send("OS FORBID DEL NICK badnick")
	b.expect(osLine("Boss", "badnick can be used again."))
	ember.send("JOIN #warez")
	ember.expect(" JOIN #warez")
}

func TestOperServNickAndChanTools(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	f := founded(t, s, "founder")
	joinAll("#bchat", b)
	heir := registered(t, s, "heir")

	// Opers use NickServ and ChanServ for accounts and channels, as in Anope.
	b.send("NS SASET PASSWORD founder newpass99")
	b.expect("Boss used NickServ: SASET PASSWORD founder")
	b.expect(nsLine("Boss", "founder's password is changed."))
	f.send("NS LOGOUT")
	f.send("NS IDENTIFY newpass99")
	f.expect("Password accepted.")
	b.send("NS SASET HIDE founder STATUS ON")
	b.expect(nsLine("Boss", "founder's oper status is hidden from others in INFO."))
	b.expect("Boss used NickServ: SASET HIDE founder")
	b.send("NS SASET PASSWORD Boss x")
	b.expect(nsLine("Boss", "Passwords need at least 6 characters."))

	b.send("CS SET FOUNDER #bchat heir")
	b.expect("Boss used ChanServ: SET FOUNDER #bchat heir (was founder)")
	heir.send("CS INFO #bchat")
	heir.expect("Founder       heir")

	b.send("OS MODE #bchat +m")
	f.expect(":OperServ!services@irc.test.local MODE #bchat +m")
	b.expect(osLine("Boss", "#bchat is now +mnrt."))
	b.send("OS KICK #bchat founder be nice")
	f.expect(":OperServ!services@irc.test.local KICK #bchat founder :be nice")

	b.send("CS DROP #bchat")
	b.expect("Type the channel name twice to drop it, as a confirmation: /cs drop #bchat #bchat")
	b.send("CS DROP #bchat #bchat")
	b.expect(csLine("Boss", "#bchat is no longer registered."))
	b.send("NS DROP heir")
	heir.expect(" 901 heir ")
	b.expect(nsLine("Boss", "heir has been dropped."))
	f.send("NS DROP Boss")
	f.expect(nsLine("founder", "Boss isn't part of your account."))
	b.send("OS NICK DROP founder")
	b.expect(osLine("Boss", "Unknown command NICK. Type /os help for a list."))
}

func TestAdminServSettingsAndMotd(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)

	b.send("AS SHOW max-users")
	b.expect(asLine("Boss", "  max-users        5000         people connected at once"))
	b.send("AS SET max-users 500")
	b.expect(asLine("Boss", "max-users is now 500 (was 5000). Saved to beeircd.conf."))
	b.send("AS SET max_users lots")
	b.expect(asLine("Boss", "max-users takes a number from 1 to 100000."))
	b.send("AS SET nick-expire 120d")
	b.expect(asLine("Boss", "nick-expire is now 120d (was 90d)."))
	b.send("AS SET identify-within 2h")
	b.expect(asLine("Boss", "identify-within takes a time from 10s to 10m, like 90s, 5m or 30d."))
	b.send("AS SET guest-prefix 9x")
	b.expect(asLine("Boss", "A guest prefix is 1 to 10 letters, digits, _ or -, starting with a letter."))
	b.send("AS SET guest-prefix Bee")
	b.expect("guest-prefix is now Bee (was Guest)")
	b.send("AS SET flying yes")
	b.expect(asLine("Boss", "There is no setting flying. See them all: /as show"))
	b.send("AS SET cloak on")
	b.expect("cloak is now on (was off)")

	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, want := range []string{"# room for fast test clients\n[limits]", "max_users = 500", `nick_expire = "120d"`, `guest_prefix = "Bee"`, "enabled = true"} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if s.cfg.Services.GuestPrefix != "Bee" || s.cfg.Limits.MaxUsers != 500 || !s.cfg.CloakEnabled() {
		t.Error("settings not live")
	}

	// A change that would break the config is refused and nothing changes.
	s.post(func() { s.cfg.Path = filepath.Join(t.TempDir(), "missing.conf") })
	b.send("AS SET max-users 400")
	b.expect(asLine("Boss", "Not saved: "))
	s.post(func() { s.cfg.Path = path })

	b.send("AS MOTD ADD Line two")
	b.expect(asLine("Boss", "The MOTD has 2 lines now. See it: /motd"))
	b.send("MOTD")
	b.expect(" 372 Boss :- Welcome to BeeNet")
	b.expect(" 372 Boss :- Line two")
	b.send("AS MOTD DEL 1")
	b.send("AS MOTD SHOW")
	b.expect(asLine("Boss", "  1   Line two"))
	if body, err := os.ReadFile(filepath.Join(filepath.Dir(path), "motd.txt")); err != nil || string(body) != "Line two\n" {
		t.Errorf("motd.txt = %q, %v", body, err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), `file = "motd.txt"`) {
		t.Error("config doesn't point at motd.txt")
	}
	b.send("AS MOTD CLEAR")
	b.send("MOTD")
	b.expect(" 422 Boss :MOTD File is missing")
}

func TestAdminServPorts(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close()

	b.send("AS PORT ADD " + addr)
	b.expect(asLine("Boss", addr+" is open (plain) and saved."))
	b.send("AS PORT ADD " + addr)
	b.expect(asLine("Boss", addr+" is open already."))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("new port doesn't answer: %v", err)
	}
	conn.Close()
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `address = "`+addr+`"`) {
		t.Errorf("port not saved:\n%s", raw)
	}
	b.send("AS PORT LIST")
	b.expect(asLine("Boss", "3 ports."))

	b.send("AS PORT DEL " + addr)
	b.expect(asLine("Boss", addr+" is closed and saved. People connected through it stay."))
	b.sync()
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Error("closed port still answers")
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), addr) {
		t.Errorf("port still in config:\n%s", raw)
	}
	b.send("AS PORT DEL 1")
	b.expect(asLine("Boss", "No open port 1. See them: /as port list"))
	b.send("AS SERVER LIST")
	b.expect(asLine("Boss", "Linking BeeIRCd servers comes in Stage 2. This server runs on its own for now."))
}

func TestAdminServRestartAndDie(t *testing.T) {
	for _, word := range []string{"restart", "die"} {
		t.Run(word, func(t *testing.T) {
			s, _, result := fileServer(t, "")
			b := boss(t, s)
			other := login(t, s, "other")
			b.send("AS " + word)
			b.expect("To go ahead within 2 minutes: /as " + word + " ")
			b.send("AS " + word + " wrongcode")
			c := code(b.expect("To go ahead within 2 minutes: /as " + word + " "))
			if c == "" {
				t.Fatal("no code")
			}
			b.send("AS " + word + " " + c + " new build")
			if word == "restart" {
				other.expect("ERROR :Closing Link: 127.0.0.1 (Server restarting (new build))")
			} else {
				other.expect("ERROR :Closing Link: 127.0.0.1 (Server shutting down (new build))")
			}
			select {
			case err := <-result:
				if word == "restart" && !errors.Is(err, ErrRestart) || word == "die" && err != nil {
					t.Fatalf("Serve returned %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server kept running")
			}
		})
	}
}
