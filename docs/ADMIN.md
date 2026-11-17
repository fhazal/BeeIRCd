# Running BeeIRCd

How to run a BeeIRCd server once it is installed ([INSTALL.md](INSTALL.md)):
the `beeircd` command, changing settings, AdminServ, backups, restores and
updates, and every block of `beeircd.conf`.

Paths below are the installer's. Admin commands talk to the running server
through a local socket, so run them as root (`sudo`) on the server.

## The `beeircd` command

| Command | What it does |
| --- | --- |
| `beeircd status` | Version, uptime, ports, people online (and the peak), channels, opers, services, memory and answer time, data sizes, last backup, certificate and its renew date, and a newer release if there is one |
| `beeircd check` | Tests the config, certificate, services database and history file without touching the running server, and says whether a restart is safe |
| `beeircd start` / `stop` | Starts or stops the service. Stopping sends everyone a quit message and saves history first |
| `beeircd restart` | Runs `check` first and refuses if it fails, so a typo can't take the server down |
| `beeircd rehash` | Reads `beeircd.conf` again and applies what can change live; lists what needs a restart |
| `beeircd backup [file]` | Saves config, database, history and certificates to one `.tar.gz` |
| `beeircd mail-test <address>` | Sends a test mail with the `[mail]` settings, says in plain words why it failed if it did, and checks the sender domain's SPF, DKIM and DMARC records (what decides inbox or spam), with the record to add for each one missing |
| `beeircd update` | Installs the newest release (see [Updates](#updates)) |
| `beeircd mkpasswd` | Reads a password and prints its hash, for `admin_password` |
| `beeircd import-anope <anope.db>` | Brings accounts, channels and settings over from Anope 2.0 (see [Moving from Anope](#moving-from-anope)) |
| `beeircd webirc add <name> <ip>` | Lets a web chat (BeeRoom) on that IP pass on each person's real IP; prints its password (see [[[webirc]]](#webirc)) |
| `beeircd install` / `uninstall` | Sets BeeIRCd up as a service, or removes it (keeps your data unless `--purge`); see [INSTALL.md](INSTALL.md) |
| `beeircd link-info` | Prints this server's link fingerprint and the blocks another BeeIRCd or UnrealIRCd server needs to link |
| `beeircd web` | Runs the Hive Panel (what the `beeircd-web` service runs) |
| `beeircd version` | Prints the version |
| `beeircd run` | Runs in the foreground (what the service runs) |

Every command takes `-config <file>` when the config isn't at
`/etc/beeircd/beeircd.conf`. Colour is used only on a terminal; `NO_COLOR=1`
turns it off.

When the server is stopped, `status` shows the last log lines and what to do.
Logs are in journald:

```sh
journalctl -u beeircd -f          # follow
journalctl -u beeircd --since today
```

## Changing settings

There are three ways, all ending in the same file:

1. **Edit `beeircd.conf`**, then `beeircd check` and `beeircd rehash`.
2. **AdminServ on IRC** (opers with flag `a`): `/as set max-users 500`.
   It changes the setting at once and writes it to `beeircd.conf`, keeping
   your comments.
3. **`/REHASH`** on IRC (flag `a`) or `kill -HUP`, same as `beeircd rehash`.

**What rehash applies live:** limits, services settings, the MOTD, history
defaults and caps, the log channel, the flood profile, DNSBL lists, oper
classes, mail, HostServ rules, update settings, the server description, and a
renewed certificate.

**What needs `beeircd restart`:** ports in the file (AdminServ `PORT` opens
and closes ports live), the server name, network, SID and prefixes, the
cloak, the `[tls]` block, the control socket, the `[link]` role, address, port and certificate (not the `[[link.server]]` blocks), the
services database path, and the history file or secret. Rehash says which of
your changes these are, and keeps running with the old values for them.

A config with a mistake is refused as a whole; the server keeps running on
the old one and you get the reason.

## AdminServ

`/msg AdminServ HELP` (or `/as help`) lists everything. Opers with flag `a`
can change things; every oper can look.

| Command | What it does |
| --- | --- |
| `SHOW [setting]` | The settings AdminServ can change, with their values |
| `SET <setting> <value>` | Change one now and save it |
| `MOTD SHOW\|SET\|ADD\|DEL\|CLEAR` | Edit the message of the day |
| `PORT LIST\|ADD\|DEL <port> [TLS]` | Open or close a port now |
| `OPER LIST`, `OPER <nick> ADD\|SET\|DEL\|DISABLE\|ENABLE` | Who is an IRC operator, their class, flags, hosts, password, title, vhost, swhois |
| `CLASS LIST`, `CLASS <name> ADD\|SET\|DEL` | Oper classes |
| `HISTORY`, `DIFF <n>`, `UNDO <n>` | The last 200 changes, one change before and after, and putting one back |
| `DNSBL LIST\|CHECK <ip>\|ENABLE\|DISABLE <name>` | The DNS blocklists and their hits |
| `RESTART`, `DIE` | Restart or stop the server (you repeat it with a code first) |

**Settings for `SET`:** `description`, `max-users`, `session-limit`,
`connect-rate`, `max-unregistered`, `max-unregistered-per-ip`,
`max-channels`, `topic-length`, `away-length`, `kick-length`, `flood-burst`,
`flood-rate`, `ping-interval`, `ping-timeout`, `cloak`, `resolve-hosts`,
`nick-expire`, `chan-expire`, `identify-within`, `kill-quick`,
`allow-kill-immed`, `ban-expiry`, `guest-prefix`, `log-channel`,
`log-connects`, `flood-profile`, `find-place-by-ip`, `members`,
`registration`, `unconfirmed-expire`, `default-language`, `ircv3-drafts`.

### Opers

The admin from `[services] admin` always has every flag and can't be changed
from IRC. Add others with AdminServ:

```
/as oper ember add class globop host *@203.0.113.* password Hive-2026
```

- The nick must be registered with NickServ. Opers `/oper <nick> <oper
  password>` from a matching host; the oper password is their own, not the
  NickServ one.
- Certificate opers (like UnrealIRCd's `certfp` blocks, handy for bots):
  `/as oper guard set cert <fingerprint>` makes `/oper` need that TLS client
  certificate (WHOIS shows its fingerprint). With `set password off` too,
  the certificate alone opers and the password typed doesn't matter. Works
  on a leaf as well.
- Flags: `a` admin (AdminServ, REHASH, SA* commands), `b` bots (BotServ BOT
  ADD/CHANGE/DEL, SET PRIVATE, SET NOBOT), `c` links, `g` global, `h` hosts
  (CHGHOST and friends, HostServ), `k` kill, `s` services (OperServ).
- Built-in classes: `helper` (h), `globop` (hk), `admin` (ghks), `netadmin`
  (abcghks). You can only give flags you have yourself.
- Opers skip the flood slowdown.

## Channel bots (BotServ)

BeeIRCd starts with no bots, like Anope. An oper with flag `b` makes one,
then channel founders can put it in their channel:

```
/bs bot add Buzz bot bots.example.org Channel Bee
/bs assign #mychannel Buzz
```

The bot sits in the channel with op, can talk for it (`/bs say`), greets
people (NickServ `SET GREET`), kicks for bad words, caps or floods (`/bs
kick`, `/bs badwords`) and takes `!op`, `!kick` and the other fantasy
commands (`!mute nick` sets a quiet ban, `~m:`, without a kick; `!mute
+30m nick` lifts itself, `!unmute nick` lifts it; services bots can't be
muted or banned). ChanServ's modes and kicks in that channel then
come from the bot. `/bs help` lists everything.

## Help desk (HelpServ)

HelpServ answers on its own first, then hands questions to people:

```
/msg HelpServ chmodes                 the channel modes (same as /helpop chmodes)
/msg HelpServ register                your own answer if you wrote one, else NickServ REGISTER help
/msg HelpServ nickserv identify       any service's help page
/msg HelpServ how do I get a vhost?   a question in words: it goes to the helpers
```

Helpers are people with user mode `+h`. They get a notice for every
question, take one, message the person themselves and close it:

```
HelpServ: #3 Bee (bee@cloak) asks: how do I get a vhost?  (/msg HelpServ take 3)
/msg HelpServ list            the open questions
/msg HelpServ take 3          Bee is told you are helping
/msg HelpServ close 3 done    Bee sees the note
```

Who is a helper:

- Opers: `/mode yournick +h` to go on duty, `-h` to go off. WHOIS shows
  "is available for help".
- People who aren't opers: `/msg HelpServ helper add <nick>` (oper flag
  `s`). They get `+h` when they identify, on any server of the network,
  and switch it with `/msg HelpServ off` and `/msg HelpServ on`.

Your own answers come before the built-in help. Each `topic add` adds a
line (up to 10):

```
/msg HelpServ topic add rules No spam, be nice.
/msg HelpServ topic add rules English or Malay in #lobby.
/msg HelpServ topic list
/msg HelpServ topic del rules
```

A question waits up to an hour and goes when the person who asked quits;
one open question per person. `/he` is short for `/msg HelpServ`.

## Channel bans and extbans

`+b`, `+e` and `+I` take plain masks (`nick!user@host`) and extbans.
BeeIRCd has the extbans of UnrealIRCd 6 and InspIRCd 4, once each:
people can type either server's name for one (`~quiet:`, `~mute:`,
`~nonick:`...) and BeeIRCd keeps it in its short form. InspIRCd's own
way, without the `~` (`mute:nick!*@*`, `R:account`, with InspIRCd's
letters), works too. `/HELP EXTBANS` lists them all.

| Kind | Extbans |
|---|---|
| Who | `~a:account` (`~a:0` = not logged in), `~c:[@]#chan`, `~r:realname`, `~S:fingerprint`, `~O:operclass`, `~oper:name`, `~unauthed:mask`, `~bot:mask`, `~gateway:name`, `~server:name`, `~realmask:nick!user@host+realname`, `~C:country` (`~C:NL`), `~A:network` (`~A:64500`), `~G:group` (a [security group](#known-and-new-people), like `~G:unknown-users`) |
| What they may not do | `~q:` / `~m:` talk, `~j:` join, `~n:` change nick, `~p:` part message, `~noctcp:`, `~nonotice:`, `~blockcolor:`, `~stripcolor:`, `~nokick:`, `~blockinvite:` |
| Special | `~t:minutes:ban` lifts itself, `~f:#chan:mask` sends them elsewhere, `~T:block:text` / `~T:censor:word`, `+e ~m:type:mask` (skip `external`, `censor`, `moderated`, `color` or `notice`), `+e ~F:mask` (skip `+f`), `~i:#chan` (use that channel's bans or invite exceptions) |

"Who" extbans also go inside the others: `+b ~q:~a:0` mutes everyone
who isn't logged in. As on UnrealIRCd and InspIRCd, a plain ban also
stops talking and nick changes (voice skips both), and someone a ban
keeps quiet leaves without their part message.

On links, UnrealIRCd servers get UnrealIRCd's names and InspIRCd servers
InspIRCd's. Extbans the other server doesn't have (InspIRCd has no
`~j`, `~p`, `~T`, `~m:type`, `~F` or `~i`) stay on the BeeIRCd side, and
InspIRCd's inverted bans (`!R:account`) aren't taken. UnrealIRCd keeps
an extban it doesn't know when another server sets it, so the ones only
InspIRCd and BeeIRCd have (`~unauthed`, `~server`, `~noctcp`...) show on
its ban lists and can be removed there, but only BeeIRCd acts on them.
An InspIRCd server
says which extbans its loaded modules give it, and only those are sent to
it. A forward ban goes to InspIRCd as its `redirect:` extban when it has
that one, and as `mask#channel` (its banredirect module) when not; both
are read back as `~f`.

### Country and network (GeoIP)

`~C:` and `~A:` need to know where each user connects from. BeeIRCd
looks that up on the server itself in two small database files: the
free DB-IP Lite country and ASN files, which it downloads on its own
when they are missing and again once a month (IP data by
[DB-IP](https://db-ip.com), CC BY 4.0). Nothing about your users leaves
the server; the download is the only thing that goes out.

Opers (and the user themselves) see the result in WHOIS:

```
Bee is connecting from Netherlands
Bee is connecting from AS64500 [Example Net]
```

To use MaxMind GeoLite2 instead, put its `GeoLite2-Country.mmdb` and
`GeoLite2-ASN.mmdb` files where you like and set them in `[geoip]` with
`download = false`. `enabled = false` turns GeoIP off.

Network bans can be by place too, as on UnrealIRCd (`GLINE ~country:NL`):
`/os akill add ~C:NL reason` or `~A:64500` keeps a whole country or
network out, SHUN silences it, and `/os exception ban add ~A:64500`
lets a network back in past the other bans. UnrealIRCd's spellings
(`~country:`, `~asn:`) work too, and UnrealIRCd links get them in
theirs; InspIRCd has no such bans, so its links don't get them. A ZLINE
stays an IP only.

Opers whose client has `message-tags` also see the sender's country on
every message, in UnrealIRCd's `unrealircd.org/geoip` tag.

### Known and new people

Bots that flood a network are new: nobody from their IP has chatted
there before. BeeIRCd tells them apart the way UnrealIRCd does.

**Reputation.** Every five minutes each IP with someone in a channel of
three or more people gets a point (two when one of them is logged in),
up to 10000. Scores are saved in the services database and shared with
linked BeeIRCd and UnrealIRCd servers (each keeps the higher one), so a
score earned on one server counts on all. IPs nobody has used for a
while are forgotten: a score of 2 or less after an hour, 6 or less after
a week, 12 or less after a month, any after three months.

Opers with flag `h` see the score in WHOIS (`is using an IP with a
reputation score of 30`) and with `/REPUTATION`: `/REPUTATION nick` or an
IP for one record, `#channel` for its people, `<10` for everyone below
10. Flag `g` sets one: `/REPUTATION nick 50`, on every server.

**Security groups.** A group is a named set of people. These are always
there:

| Group | Who |
| --- | --- |
| `known-users` | Logged in, or a reputation score of 25 or more |
| `unknown-users` | Everyone not in `known-users` |
| `tls-users` | Connected with TLS |
| `webirc-users` | Came through a `[[webirc]]` gateway |
| `websocket-users` | Connected on a WebSocket port |

Add your own with `[[security_group]]` (or replace a built-in one by its
name). Opers see someone's groups in WHOIS (`is in security-groups:
known-users,tls-users`). Groups are used by:

- `~G:group` channel bans and exceptions: `+b ~G:unknown-users` keeps new
  people out, `+e ~G:trusted` lets a group in.
- `[connthrottle]`: when more than 20 new people a minute connect to a
  server (30 on the network), people not in `known-users` are refused
  until it calms down; known people still get in. It waits 3 minutes
  after a start, and a week after reputation began, so known people are
  known first. Opers: `/THROTTLE` (flag `h`) shows it, `/THROTTLE OFF`,
  `ON` and `RESET` (flag `g`) change it until the next start.
- `[[restrict]]`: commands new people may not use yet, like `/LIST` or
  private messages in their first minute. Opers are never restricted, and
  services can always be messaged.
- `[spamfilter] except_groups`: people never filtered.

```toml
# People from the Netherlands, unless they came through a web gateway
[[security_group]]
name = "dutch"
mask = ["~C:NL"]
[security_group.exclude]
webirc = true

# New people wait a minute before private messages and /LIST
[[restrict]]
command = "private-message"
connect_time = "60s"
except = ["known-users"]

[[restrict]]
command = "list"
connect_time = "60s"
```

## Oper tools

Besides `/KILL`, the SA commands and the network bans, opers have the
tools of UnrealIRCd and InspIRCd. `/HELP <command>` explains each one.

| Command | Flag | What it does |
| --- | --- | --- |
| `SAKICK <#channel> <nick> [reason]` | `a` | Kicks someone as the server, whatever their status |
| `SAQUIT <nick> <reason>` | `a` | Makes someone quit with your reason, as if they left by themselves |
| `CLEARCHAN <#channel> [KICK\|KILL\|G\|Z] [reason]` | `a` | Empties a channel: everyone but you, opers and services is kicked (default), killed, or banned for an hour by host (`G`) or IP (`Z`) |
| `NICKLOCK <nick> <newnick>`, `NICKUNLOCK <nick>` | `a` | Changes a nick and holds it until unlocked or until they reconnect |
| `SDESC <text>` | `a` | Changes this server's description (LINKS, WHOIS) until the next restart or rehash |
| `JUMPSERVER <server[:port]> [<tls server[:port]>] <NEW\|ALL> [reason]` | `a` | Sends people to another server; see below |
| `TRACE [server]` | any | This server's connections: people, opers, linked servers |
| `CHECK <nick\|#channel\|mask>` | any | Everything the server knows about a person or channel, or who matches a mask |
| `TLINE <user@host>` | any | Counts who a ban mask would hit, without banning anyone |
| `SETIDLE <time>` | any | Sets your own idle time (`90`, `30m`, `2h`) |
| `TSCTL ALLTIME` or `ALLTIME` | any | The clock of every server, to spot one that is off |

SAJOIN, SAPART, SANICK, SAQUIT, NICKLOCK and NICKUNLOCK also work on
people connected to another server: the command travels to their server,
which carries it out. NICKLOCK and SAQUIT need that server to be BeeIRCd
(on other software SAQUIT becomes a KILL). SAJOIN, SAPART and SANICK work
both ways with InspIRCd servers that have those modules loaded, and
ALLTIME asks them for their clock too. UnrealIRCd has no SANICK: someone
on an UnrealIRCd server is renamed the way services do it, which works
when this server is in that server's `ulines`.

**Moving people before maintenance.** `/JUMPSERVER irc2.example.org NEW
back in an hour` sends everyone who connects from then on to
irc2.example.org (numeric 010; most clients follow it by themselves) and
closes their connection. `ALL` instead of `NEW` also moves everyone
connected now, except opers. A second address is for TLS connections:
`/JUMPSERVER irc2.example.org:6667 irc2.example.org:6697 ALL`.
`/JUMPSERVER` alone shows the setting and `/JUMPSERVER OFF` ends it; a
restart ends it too. It applies to the server you type it on.

### For everyone

- **Caller ID.** `/MODE yournick +g` lets only people on your accept list
  message you. `/ACCEPT nick` adds someone, `/ACCEPT -nick` removes them,
  `/ACCEPT *` shows the list (30 people, until you disconnect). People
  you message yourself are added for you; someone who changes nick drops
  off the list (as on InspIRCd). A refused sender is told, and
  you are told who tried, at most once a minute for each sender. Opers
  and services always get through.
- **Notify lists.** `WATCH` and `MONITOR` both tell a client when a nick
  comes online or leaves; clients pick the one they know.
- `CYCLE #channel` leaves and rejoins, `REMOVE #channel nick [reason]`
  (channel ops) makes someone leave as a PART instead of a KICK, and
  `UNINVITE nick #channel` takes an invite back.
- WHOWAS remembers people for a week, also across a restart.

### DCC file rules

Like UnrealIRCd's `deny dcc`. Nothing is blocked until you add blocks:

```toml
# Never: programs
[[deny_dcc]]
filename = "*.exe"
reason = "Programs can carry viruses"

# Only from people the receiver trusts
[[deny_dcc]]
filename = "*.zip"
reason = "Archive"
soft = true
```

A `soft` block holds the file back until the receiver types `/DCCALLOW
+nick` for the sender (10 people, until they disconnect; `/DCCALLOW LIST`,
`/DCCALLOW HELP`). The receiver is told who tried to send what. Blocks
without `soft` refuse the file type for everyone, in private and in
channels. `[[allow_dcc]]` makes exceptions, and opers are never blocked.
The rules look at the file name in the DCC SEND request; the file itself
travels between the two people, not through the server.

### More spam protection

Four more guards, each off until you switch it on. Opers are never
stopped by them, and messages to services always get through.

- `[antirandom]` looks at the nick, username and real name of someone
  connecting. Long runs of consonants, vowels or digits and letter pairs
  that don't occur in real names (like `qx`) add to a score; at 7 the
  connection is refused and opers with `+s` see why. People in
  `known-users` and web chat visitors are not checked. Try it with
  `action = "warn"` first: nobody is refused, opers only see who would
  have been.
- `[antimixedutf8]` stops messages that switch alphabet inside words,
  like Latin text with look-alike Cyrillic letters to slip past the spam
  filter. Someone writing in two languages is fine: only many switches
  inside words reach the score.
- `[targetfloodprot]` caps how many messages one channel or one person
  can get in a few seconds from everyone together (45 messages in 5
  seconds for a channel). What goes over it is not delivered and the
  sender is told. It protects against a flood from many connections at
  once, which `+f` (per person) doesn't catch. It is on by default, like
  in UnrealIRCd; opers are never stopped. On a network with a busy relay
  bot, raise `channel_privmsg`, or set `enabled = false`.
- `[blockamsg]` stops `/amsg` and `/ame`: the same text to several
  channels within 200 ms only reaches the first channel.

```toml
[antirandom]
enabled = true
action = "warn"

[targetfloodprot]
channel_privmsg = "60:5"
```

## Spam filter

Rules that catch spam as it is sent, like UnrealIRCd's spamfilter. Opers
with flag `k` manage them on IRC or on the Hive Panel's **Spam filter**
page. Rules reach every linked server, BeeIRCd and UnrealIRCd 6 alike.

```
/spamfilter add cp gline 1d Spam_is_not_allowed *free nitro*
/spamfilter add -regex u kill - Bot_nicks ^[a-z]{3}[0-9]{5}!
/spamfilter test cp hey, free nitro here
/stats F
/spamfilter del 1
```

- **Targets** say where to look: `c` channel messages, `p` private
  messages, `n` private notices, `N` channel notices, `P` part reasons,
  `q` quit reasons, `t` topics, `a` away messages, `u` the person as
  `nick!user@host:realname` (at connect and on a nick change).
- **Actions**: `warn` (tell opers, let it through), `block` (drop it,
  tell the sender), `kill`, `tempshun` (until they reconnect), `shun`,
  `gline` (an AKILL) and `gzline` (a ZLINE) for the ban time given
  (`-` = until removed). `kline` and `zline` work too and also set
  network bans.
- **Matching** is simple (`*` and `?`) or `-regex` (Go RE2: always fast,
  no backreferences), never case-sensitive, on the text without colours
  and bold. The reason uses `_` for spaces, as on UnrealIRCd.
- **Reason** is what the person sees when a rule catches them. The panel's
  Filters list shows it in its own column; point at a row to see who set
  the rule.
- **Safety net**: a new rule is first tried on about 200 normal chat lines
  (English and Malay). If it would catch any, it is refused and the lines
  are shown; add `-force` (or press **Add anyway** on the panel) if you
  are sure. Start a new rule with `warn` to see what it catches.
- Opers, Exceptions (ELINE) and channels in `[spamfilter] except` are
  never filtered.

## Backups

The `beeircd-backup` timer makes a backup every day at 04:00 server time in
`/var/lib/beeircd/backups/`, named by date (`beeircd-2026-11-12-0400.tar.gz`). The
newest 7 are kept (`[backup] keep`). Make one now:

```sh
sudo beeircd backup                      # dated, in the backups folder
sudo beeircd backup /root/before-change.tar.gz
```

A backup holds `beeircd.conf`, a live copy of `services.db` (taken without
stopping anything), `history.db` (still encrypted with your history secret)
the TLS certificate files and the link certificate (`link/`). Backups are readable only by the `beeircd` user and root. Copy backups off
the machine now and then; they contain password hashes and secrets.

`beeircd status` shows when the last backup was made.

### Restore

```sh
sudo beeircd stop
sudo mkdir /tmp/restore && sudo tar -xzf /var/lib/beeircd/backups/beeircd-2026-11-12-0400.tar.gz -C /tmp/restore
sudo cp /tmp/restore/beeircd.conf /etc/beeircd/beeircd.conf
sudo cp /tmp/restore/services.db /tmp/restore/history.db /var/lib/beeircd/
sudo cp /tmp/restore/tls/* /etc/beeircd/tls/            # only if the certificate is lost
sudo cp /tmp/restore/link/link-*.pem /var/lib/beeircd/   # hub or leaf: or where [link] cert/key point
sudo chown -R beeircd:beeircd /etc/beeircd /var/lib/beeircd
sudo rm -f /var/lib/beeircd/services.db-wal /var/lib/beeircd/services.db-shm
sudo beeircd check && sudo beeircd start
sudo rm -rf /tmp/restore
```

To move to a new machine: install there with the same server name, then
restore the backup the same way. Keep the backup's `beeircd.conf` (its cloak
and history secrets go with the data). On a hub or leaf, also bring the
link certificate (`link/` in the backup): without it the server makes a new
one, and every server that pinned the old fingerprint refuses the link.

## Moving from Anope

`beeircd import-anope` reads the `anope.db` that Anope 2.0 writes (its
default `db_flatfile` module) and brings everything into BeeIRCd's services:
accounts and their nicks, passwords, certificates, auto-joins, vhosts and
vhost requests, memos, channels with founder, successor, topic, settings,
access lists, AKICKs, LEVELS, mode locks and entry messages, BotServ bots
with their settings, kickers and bad words, AKILLs, SQLINEs, SNLINEs,
forbids, session exceptions, news, oper notes, services ignores and seen
entries.

**Passwords keep working.** Accounts keep Anope's password hash (enc_sha256,
enc_bcrypt, enc_md5, enc_sha1 or enc_none). The first right IDENTIFY, SASL
login or Hive Panel sign-in swaps it for BeeIRCd's own. Only Anope's old
`enc_old` hashes can't be checked: those accounts need NickServ RESETPASS
(the report names them).

Steps:

1. Copy `anope.db` from Anope's data folder (Anope can keep running).
2. Look first, nothing is written:
   ```sh
   sudo beeircd import-anope -dry-run /root/anope.db
   ```
   The report lists what comes in, what changes on the way, names that
   exist on both sides, and what is left out and why.
3. Stop Anope (and unlink it from your network), then stop BeeIRCd:
   `sudo beeircd stop`.
4. Copy a fresh `anope.db` and import it:
   ```sh
   sudo beeircd import-anope /root/anope.db
   ```
   It shows the report again, asks, makes a normal backup and a copy of
   `services.db`, then imports. It prints the one command that undoes it.
5. `sudo beeircd start`. Opers come from `beeircd.conf` and AdminServ OPER,
   not from Anope's `oper {}` blocks.

Run it on the server that runs the services (the hub). What comes over
differently:

- A nick that BeeIRCd has already (the admin nick made at install) keeps
  BeeIRCd's account; `-replace` takes Anope's instead. The same goes for
  channels and bots.
- ChanServ FLAGS entries stay FLAGS entries with the same letters. Anope's
  `f` (seeing and changing the access lists) also gets `l`, BeeIRCd's own
  letter for seeing them.
- Access entries on a mask instead of a registered nick, and settings
  BeeIRCd doesn't have (like channel SET EMAIL), are listed and left out.
- Anope's service bots (NickServ, ChanServ, ...) are not imported: BeeIRCd
  has its own.

Coming from UnrealIRCd too? `-unreal-opers /path/to/unrealircd.conf` also
brings its `oper { }` blocks over, after the accounts: argon2id password
hashes as they are (so opers keep their passwords; a plain-text password is
hashed), `certfp` masks as certificate opers, host masks, vhost and swhois
lines. Unreal's own operclasses map to BeeIRCd's (netadmin, admin,
services-admin, globop; locop becomes helper); an operclass of your own
becomes flags from its permissions (kill and server-ban: `k`, chghost: `h`,
...). Each oper's name must be a registered nick, since oper entries belong
to accounts here; the report names any that isn't.

`-yes` skips the question (for scripts).

## Updates

```sh
sudo beeircd update --check   # only look
sudo beeircd update           # install the newest release
```

The server looks once a day for a newer release and shows it in `beeircd
status` and to opers (`[update] check = false` turns that off; nothing about
your server is sent). Updates are never installed by themselves.

`beeircd update`:

1. downloads the release list and checks its signature: every release is
   signed by the BeeIRCd release key, and the public half is inside the
   program. An unsigned or changed release is refused;
2. downloads the program for this machine and checks its checksum;
3. lets the new program check your config (and add new settings if a release
   needs them) on a copy first;
4. makes a backup;
5. swaps the program (the old one is kept as `beeircd.old`), tells everyone
   online "Server updating to BeeIRCd X, back in a few seconds", restarts;
6. waits up to 30 seconds for the new version to answer. If it doesn't, the
   old program goes back by itself and the journal says why.

Options: `--yes` asks no questions (for scripts), `--rc` also takes test
versions (release candidates), `--rollback` goes back to the kept
`beeircd.old`.

## Hive Panel (web admin)

`beeircd-web` is the Hive Panel: the web admin. It talks to the running
server only through the control socket and answers only on this machine
(`127.0.0.1:8787` by default). Reach it from your computer with an SSH
tunnel:

```sh
ssh -N -L 8787:127.0.0.1:8787 you@your-server
```

then open <http://127.0.0.1:8787>. `beeircd install` sets it up as the
`beeircd-web` service when you answer yes; by hand it is
`beeircd web -config /etc/beeircd/beeircd.conf`, run as the beeircd user
(it needs the control socket).

- **Sign in** with your oper nick and oper password, the same as `/oper`.
  The nick must be registered with NickServ first (on a new server: the
  IRC steps the installer prints, then the panel); until then the panel
  answers "Wrong nick or password" like for any unknown nick. With Settings → Members on (`[web] members`), anyone with a registered
  nick can also sign in with their nick and NickServ password, to their
  own pages only (Home, Chat, My nick, My channels, Memos); an oper who
  uses the NickServ password gets those pages too. The oper entry's hosts are checked against the
  address you come from (`*@*` and `*@127.0.0.1` work through a tunnel;
  hostname masks don't, the panel only knows the address).
- Wrong passwords count toward the same lock as `/oper` (5 wrong: 10
  minutes), and the panel also locks an address after 5 wrong tries.
  Every password lock (IDENTIFY, SASL, /oper, the panel) holds one nick
  or oper entry *from one address*: someone guessing wrong locks
  themselves out, never the real owner coming from elsewhere.
- **Every click is checked by the server** against your oper entry and
  flags. Settings, Check and Rehash need flag `a`; Backup is for the
  owner (the `[services] admin`). Take an oper entry away and that oper is
  signed out at their next click.
- **Overview** opens with a greeting that says if anything needs you, then
  **Network status** (up or not, servers, services, version, last backup,
  certificate, uptime) beside **Spotlight** (the busiest room right now,
  the newest member, today's busiest moment, anniversaries, one at a
  time). Below: Numbers, This server, the Network map (flag `c`), What
  happened, Server actions (flag `a` or owner: Check, Rehash, Back up
  now), **Server details** (ports with TLS and the link port, the system
  it runs on, the names on the certificate, when it started, channel
  history), **Where people connect from** (flag `h`, when GeoIP is on:
  people per country or per network, the top 7, the rest and "not known";
  click a row to see them on People), **Health** (quiet in one line;
  otherwise how many were slowed down, Z-lined or stopped by a blocklist
  today), Services and Linked servers. Recent bans, Top channels and Opers online can be switched on
  with Arrange (below).
- **People** lists everyone online on every server: search, filters
  (logged in, guests, opers), 50 at a time. Click a person for their
  details and to send them a notice. Real hosts and addresses show only
  for opers with flag `h` (and only they can search by them). With GeoIP
  on, flag `h` also shows each person's country code and network (AS
  number and owner), and the search takes `MY`, `Malaysia` or `AS4788`. Disconnect
  and Ban need flag `k`, like /KILL and OperServ AKILL; Ban adds an
  AKILL on the person's address (1 hour to until removed) and asks first.
- **Channels**: every channel on the network (secret ones too), biggest
  first, plus suspended ones; search, filters (registered, not
  registered, suspended). Open one for its topic, modes (never the key),
  ban counts, the people in it (ops first) and its ChanServ
  registration. Send everyone in it a notice (flag `g`, from Global as
  "[your nick] text"), set its topic (flag `s`, as ChanServ) or suspend /
  unsuspend it like ChanServ SUSPEND (flag `s`; asks first).
- **Bans** (flag `k`, like OperServ's ban commands) lists the network
  bans by kind (AKILL, ZLINE, SHUN, SNLINE, exceptions) and adds or
  removes them with the same checks as OperServ. While you type a mask it
  says how many people online it matches; masks that match everyone are
  refused. An SNLINE bans by realname: type it with spaces (saved with
  `?` for each space, as OperServ writes it), as a `/regex/`, or as the
  nick of someone online to take their realname. SNLINEs reach linked
  BeeIRCd servers only. SQLINE (nick bans) stays in OperServ (flag `s`).
- **Opers** (flag `a`, like AdminServ OPER): every oper entry with its
  class, flags and hosts; add an oper (a registered nick), change their
  class, own flags, hosts or oper password, or remove them. Same rules as
  AdminServ: you give and take only flags you have, remove only opers
  whose flags you all have, and the owner is locked (beeircd.conf). You
  can't remove yourself or take your own flag `a` in the panel. Every
  change is in AdminServ HISTORY; the panel's Undo puts it back (a
  removed oper comes back with their password).
- **Linked servers** (flag `c`, like /CONNECT and /SQUIT): a map of the
  network with people per server, every link block with its state,
  Connect (for blocks with an address) and Split (asks first, optional
  reason). Add, change or remove `[[link.server]]` blocks: saved to
  beeircd.conf and applied with a rehash at once; the link password is
  never shown (leave it empty to keep it). The card "This server, for the
  other side" shows the name, link port and fingerprint the other
  server's block needs (the same as `beeircd link-info`).
- **Services** (flag `a`): registered nicks, channels and memos, the
  bots, and the services settings from AdminServ SET in plain words
  (account and channel expiry, identify time, guest prefix, KILL QUICK /
  IMMED, how long network bans last, the log channel). Saved, applied
  and undoable like Settings.
- **Network map** (Linked servers, flag `c`): every server as a small
  icon on a world map you drag round (it wraps like a globe) and zoom
  into with the wheel, a pinch or + and −. Yellow lines are links, a red
  dashed one is split; hubs have a hexagon. Servers close together share
  a chip with their count until you zoom in. Find a server, or show only
  split servers or hubs. Click an icon for its card (people, lag, Split or
  Connect, Move). Each BeeIRCd server places itself from its time zone
  (a machine set to Asia/Kuala_Lumpur is in Kuala Lumpur; the city list
  comes from the IANA tz database and ships with BeeIRCd) and tells the
  servers it links to. A server set to UTC, or another IRC server, you
  place yourself: Move or Place, click the map, name the place. Your
  places are `[[map.place]]` blocks in beeircd.conf (`server`, `name`,
  `lat`, `lon`), always win over what a server says about itself, and
  can be written by hand. A server you haven't placed also asks
  ipinfo.io once where its own IP address is (city level, rounded to about
  10 km) and uses that over its time zone; `[server] find_place_by_ip =
  false` turns that off. No person's address is ever looked up, and no
  person is ever on the map.
  Lite turns off the glow and the moving signals (it switches on by itself
  when drawing is slow, and the signals stop with reduced motion). The
  outlines are Natural Earth (public domain), shipped with the panel.
- **Mail** (Settings, owner only) sets up how services send mail
  (password resets, memo mails, account confirmation) without editing
  `[mail]` by hand. Pick Gmail / Google Workspace, Outlook / Microsoft
  365 or Zoho and the server, port and TLS are filled in: type the
  address and an *app password* (not the normal password; the card links
  to where you make one). Or pick your own mail server (host, port,
  login, password), or Off. The password is saved in `beeircd.conf` and
  never shown again; leave it empty to keep it. **Send test mail** sends
  a real mail to any address, also before saving, and a failure shows the
  mail server's own words plus what they mean (wrong app password, port
  blocked by the hosting company, TLS on the wrong port). **Will it land
  in the inbox?** looks up the sender domain's SPF, DKIM and DMARC; each
  red row says why and gives the DNS record to add, with Copy. Free
  Gmail, Outlook and Zoho addresses are always green; an own domain
  without DKIM and DMARC is what usually sends mails to spam. **New
  NickServ accounts** sets `[services] registration`: Ready at once,
  Confirm by email, or An oper confirms. Confirm by email can only be
  picked after a test mail with the saved settings went through (the
  server remembers the last good test until it restarts). Each
  change and test goes to "What happened". The same check on the
  command line: `beeircd mail-test you@example.org`.
- **Backups** (Settings, owner only) lists the dated backups in the
  backup folder, newest first, with who made the ones made in the panel.
  Back up now makes one. The download button asks for your oper password
  again first: the file holds every password hash and the server's
  private keys, so a browser left signed in is not enough. Each download
  (and each wrong password) goes to "What happened" and the server
  notices. The panel reads the file itself, so the backup folder must be
  readable by the user `beeircd-web` runs as (the installer's
  `/var/lib/beeircd/backups` is).
- Settings changed in the panel are saved to `beeircd.conf`, applied at
  once and kept in AdminServ HISTORY (Undo works there too). The panel's
  own "What happened" list keeps sign-ins and changes with who and from
  where.
- A session ends after `session_idle` without a click (30 minutes) and
  after 12 hours in any case.
- **Members** (`[web] members = true`, off by default): everyone with a
  registered nick may sign in with their NickServ password and gets only
  the YOU pages: **Home** (their start page: new memos, their channels
  right now, the newest access list and AKICK additions they may see on
  IRC, where they are connected, and "Needs you" when something is to
  do), **Chat** (only with `chat_url`, below), **My nick** (NickServ SET
  KILL, HIDE, AUTOOP, PRIVATE and GREET as switches, run as the real SET
  with NickServ's own answer), **My channels** (where they are founder or
  on the access list; each channel opens its own page: People now,
  Access, AKICK, Settings with topic, switches, description and
  successor, the channel's Memo box, and for the founder Hand over and
  Drop, asked twice. Each tab shows only when their ChanServ level allows
  it on IRC, and every change is the real ChanServ ACCESS, AKICK, SET,
  TOPIC or DROP with ChanServ's own answer and log line; the panel never
  uses oper rights there) and **Memos** (read, reply, delete and send: the
  real MemoServ SEND with its limits, ignores and send delay). Opers
  start on Overview and find the YOU pages at the top of the menu. Opers sign
  in with their oper password for the admin pages, or with their NickServ
  password for the member pages only. An oper with no oper password yet
  (and no certificate) who signs in with their NickServ password sees a
  note on Home that says how to get one: the owner's first `/oper` from
  `[services] admin_hosts`, `admin_password` in beeircd.conf when there
  are no admin_hosts, or `/as oper <name> set password` by an admin for
  everyone else. Wrong NickServ passwords count on
  the same lock as IDENTIFY on IRC. A new NickServ password, a
  suspension or turning members off (Settings → Members, or `members` in
  the file then rehash) ends member sessions.
- Behind a reverse proxy on the same machine, list the proxy in
  `[web] trusted_proxy` so the panel believes its `X-Forwarded-For` and
  `X-Forwarded-Proto` (https makes the session cookie Secure).

### Let members reach the Hive Panel

Opers reach the panel through an SSH tunnel. Normal users can't, so to let
them sign in from anywhere, put the panel behind HTTPS on the web server
of the same machine:

1. A name for it, like `panel.example.org`, pointing at the server.
2. Your web server sends it to the panel. Caddy:

   ```
   panel.example.org {
       reverse_proxy 127.0.0.1:8787
   }
   ```

   nginx: a `server` block for that name with your certificate and
   `location / { proxy_pass http://127.0.0.1:8787; proxy_set_header Host $host;
   proxy_set_header X-Forwarded-For $remote_addr; proxy_set_header
   X-Forwarded-Proto https; }`.
3. In `beeircd.conf`: `[web] trusted_proxy = ["127.0.0.1"]`, then
   `sudo systemctl restart beeircd-web` (the panel only, IRC stays up).
4. Settings → **Members** → **Let members sign in**.
5. With BeeRoom: `[web] chat_url = "https://chat.example.org"` (then
   restart beeircd-web), and add `"https://panel.example.org"` to
   `frameAncestors` in BeeRoom's `config.js` (`BEEROOM_PANEL` in Docker), so
   the Chat tab may show it.

The panel still answers only on 127.0.0.1; the web server is the only way
in. Opers keep signing in with their oper password; every page checks the
person's own rights on the server.


**Find your way round.** The menu on the left holds every page your flags
allow. In the top bar: **Jump to** (Ctrl K, ⌘K on a Mac) finds a page or
runs Check, Rehash, Back up now (owner) and more; **Theme** picks Bee,
Catppuccin Latte (light), Catppuccin Mocha, One Dark Pro or Dracula (kept in
that browser only); the pill says whether the server answers.

**Arrange the Overview.** Click **Arrange**: switch blocks on and off
(Recent bans, Top channels and Opers online are off at first), drag a block
by its **Drag** handle, or focus the handle and use the arrow keys (up/down
moves, left/right changes column). **Reset** puts it back. The layout is
kept per oper account in that browser; nothing is saved on the server.

## Logins from LDAP or a database

When your people already have a password somewhere else (a company
directory, a forum, a game's website), BeeIRCd can check their NickServ
password there. It is off until you add an `[auth]` section.

How it works:

- Someone logs in the usual way: `/msg NickServ IDENTIFY <password>`, SASL
  in their client, or the Hive Panel's member sign-in. BeeIRCd asks the
  outside source whether the password is right for that name.
- The first time a name the outside source knows logs in on IRC, its
  NickServ account is made (with the email address the source gave).
  `create = false` keeps it to accounts that exist already.
- A password set in BeeIRCd keeps working next to the outside one, so
  accounts registered before stay as they are. With `local_passwords =
  false` only the outside password counts, and `REGISTER` and `SET
  PASSWORD` are switched off.
- When the outside source can't be reached, passwords set in BeeIRCd still
  work; everyone else is told to try again in a moment. Those tries are
  not counted as wrong passwords.
- Wrong passwords are counted and locked as always (5 from one address
  lock the name for 10 minutes), so the outside source is not hammered.
- With `opers = true` an oper's `/oper` password (and their Hive Panel
  sign-in) is asked from the outside source too, under their account
  name. Who is an oper is still decided in BeeIRCd.

**LDAP** (OpenLDAP, Active Directory, FreeIPA, Authentik, ...):

```toml
[auth]
source = "ldap"

[auth.ldap]
url = "ldaps://ldap.example.org"
bind_dn = "cn=irc,ou=services,dc=example,dc=org"
bind_password = "..."
base_dn = "ou=people,dc=example,dc=org"
filter = "(uid=%s)"
```

BeeIRCd signs in as `bind_dn`, looks for the one entry that matches
`filter` under `base_dn`, and then tries the person's password on that
entry. Put more rules in the filter to let in only some people, like
`"(&(uid=%s)(memberOf=cn=irc,ou=groups,dc=example,dc=org))"`. For Active
Directory use `"(sAMAccountName=%s)"`. When every entry sits at a known
place you can skip the search with `user_dn = "uid=%s,ou=people,dc=example,dc=org"`.
Use `ldaps://`, or `ldap://` with `starttls = true`: over plain `ldap://`
passwords cross the network unprotected.

**SQL** (MySQL / MariaDB, PostgreSQL, SQLite):

```toml
[auth]
source = "sql"

[auth.sql]
driver = "mysql"
dsn = "irc:secret@tcp(127.0.0.1:3306)/forum"
query = "SELECT password, email FROM users WHERE name = $name"
```

`query` gives the stored password as its first column and, if you like,
the email address as the second. `$name` is filled in safely by the
database driver, never pasted into the text. bcrypt (`$2y$...`, what PHP
and most forums write) and argon2id hashes are recognised by themselves.
For older tables set `hash` to `sha256`, `sha512`, `md5` (unsalted, as
hex) or `plain`. When the database must compare the password itself, set
`hash = "query"` and use `$password`: a row back means the password is
right.

```toml
# PostgreSQL
driver = "postgres"
dsn = "postgres://irc:secret@127.0.0.1/forum?sslmode=require"

# keep a line for every login try
log_query = "INSERT INTO irc_logins (at, name, ip, result) VALUES ($time, $name, $ip, $result)"
```

`log_query` runs after every try with `$time` (Unix seconds), `$name`,
`$ip` and `$result` (`ok`, `wrong` or `unknown`). The password is never
given to it.

Good to know: the name is the nick people log in with, so names in the
outside source should be usable as nicks. The outside source's own
account lock-outs still apply. `[auth]` belongs on the server that runs
services; a rehash applies changes.

## Admin API (JSON-RPC)

Tools and scripts can read and manage the network through a JSON-RPC 2.0
API. It uses UnrealIRCd's method names, parameters and answers, so tools
written for UnrealIRCd's API (like its web panel) can talk to BeeIRCd.
It is off until you add an `[rpc]` section:

```toml
[rpc]
listen = "127.0.0.1:8600"

[[rpc.user]]
name = "statsbot"
password = "$argon2id$..."        # from: beeircd mkpasswd
allow = ["user.list", "channel.*", "stats.*"]

[[rpc.user]]
name = "panel"
password = "$argon2id$..."
ips = ["192.0.2.10"]              # may call from this address too
```

**Three ways in**

- **HTTPS**: `POST https://server:8600/api` with HTTP Basic auth (the
  name and password of an `[[rpc.user]]` block), one call per request
  (JSON-RPC batches, several calls in one list, are not taken; every call
  needs an `id`). It uses the server's certificate from `[tls]`.
- **WebSocket** on the same address (path `/api` or `/`), with the same
  sign-in: many calls on one connection, and the live log. This is how
  unrealircd-webpanel connects.
- **A socket file** (`socket = "/run/beeircd/rpc.sock"`): one JSON object
  per line. No password: whoever may open the file (the beeircd user and
  its group) may call everything.

```sh
curl -sk -u statsbot:password https://127.0.0.1:8600/api \
  -d '{"jsonrpc":"2.0","id":1,"method":"user.list"}'
```

**Who may do what.** `allow` lists the methods a user may call: `"*"`
(the default when left out), a whole group like `"user.*"`, or single
methods. Without `ips` a user may only call from the server's own
machine. Five wrong passwords from one address lock it out for a minute,
and opers with `+s` see each wrong one. Every call that changes something
is shown to opers with `+s` with the name of the caller. A tool that acts
for several people says who is behind a call with `rpc.set_issuer`, and
that name is used in bans and notices. `[[rpc.user]]` blocks apply on a
rehash; `listen` and `socket` need a restart.

**Methods**

| Group | Methods |
| --- | --- |
| `rpc` | `info` (the method list), `set_issuer`, `add_timer` and `del_timer` (run a call again every `every_msec`, 250 or more, on a WebSocket or the socket file; at most 32 per connection; unrealircd-webpanel's Overview uses it) |
| `stats` | `get`: people, opers, channels, servers and bans in numbers |
| `user` | `list`, `get`, `set_nick`, `set_username`, `set_realname`, `set_vhost`, `set_mode`, `join`, `part`, `kill`, `quit` |
| `channel` | `list`, `get` (`object_detail_level` 4: each member as a whole user), `set_mode`, `set_topic`, `kick` |
| `server` | `list`, `get`, `rehash`, `disconnect` (SQUIT: `link` is a server name or SID, `reason` is optional; a server further away is split off too), `module_list` (one entry, `beeircd`: everything is built in) |
| `server_ban` | `list`, `get`, `add`, `del` for the types `gline` (AKILL), `kline`, `gzline` / `zline`, `shun` |
| `server_ban_exception` | `list`, `get`, `add`, `del` (ELINE: it lifts every kind of ban, so `exception_types` is not used) |
| `name_ban` | `list`, `get`, `add`, `del`: the nick and channel forbids (`/qline`, `/os sqline`, `/os forbid chan`). A name starting with `#` is a channel name. Adding one moves people off the nick or out of the channel; Q-lines from a linked UnrealIRCd server are listed but removed there |
| `spamfilter` | `list`, `get`, `add`, `del` |
| `whowas` | `get` |
| `log` | `subscribe`, `unsubscribe` (the live log, on a WebSocket or the socket file), `list` (the last 500 lines) |

Not there (yet): `user.set_oper`, `user.set_snomask`,
`server.connect`, `message.*`. A spam filter rule
that would also catch normal chat is refused, as the Hive Panel warns
about. The live log carries the server notices opers see, with the
subsystems `connect`, `oper`, `link`, `rpc` and `server`.

Changing someone on another server (`user.set_nick`, `user.join`, ...)
works when the API is called on the server that runs services, because
those changes travel as services commands.

## Linking servers

Servers on one network share users, channels, bans and one set of
services. One server is the **hub**; each **leaf** keeps one link to it.
A link is TLS on its own port (7000) and is locked by **fingerprint**:
each server has its own link certificate, and a server only links with
one holding the key it expects. No passwords are needed.

1. **The hub.** Install it as a hub (question 9, answer 2). It runs the
   services. At the end it prints its fingerprint and the block a leaf
   needs; `sudo beeircd link-info` prints it again any time.
2. **Each leaf.** Install it as a leaf (answer 3) and give the hub's
   name, address, fingerprint and cloak secret (`[cloak] secret` in the
   hub's beeircd.conf; not shown while typed; every server needs the same
   one or cloaks differ per server). The installer writes the hub's
   `[[link.server]]` block with `autoconnect = true`, turns the leaf's
   own services off (it uses the hub's) and prints the block the hub
   needs. Opers /oper on a leaf as anywhere: their entries live with the
   accounts on the hub, so the leaf asks the hub over the link, and the
   hub checks the entry, host and password (wrong-password lockout
   included) and sends back the flags. The admin from the leaf's
   beeircd.conf also opers there with its own admin password, once
   identified to the account of that name. Changing, disabling or
   deleting an entry (or its class) on the hub reaches the leaf at once:
   new flags, or no longer an operator.
3. **Back on the hub,** paste the leaf's block into
   `/etc/beeircd/beeircd.conf` and run `sudo beeircd rehash`:

        [[link.server]]
        name = "leaf1.example.net"
        fingerprint = "the leaf's fingerprint"
        # no host: this leaf connects to the hub

4. **Start the leaf** (`sudo beeircd start`). Within seconds `beeircd
   status` on both shows the link `● up` with its lag, and `/MAP` shows
   the tree. If it stays down, the Links row says why (wrong fingerprint,
   port closed, name mismatch) and when it tries again.

Every server needs its own server name and SID (`[server] sid`, a digit
and two capitals or digits, like `0EX`; the installer makes one from the name, check the
others don't have it). The hub's port 7000 must be open to the leaves.

Linking a server already installed as standalone: set `[link] role`,
restart once (the link certificate is made then), and follow steps 3–4
with `beeircd link-info` on each side. For an UnrealIRCd 6 server on
the other end, `link-info` also prints the `link { }` block for
unrealircd.conf, and for InspIRCd 4 the `<link>` block for
inspircd.conf; see [link] below for services and SASL there.

## Security notes

- Connections are throttled per IP: 5 connects per 60 s (`connect_rate`),
  3 still logging in per IP, 500 logging in in total. An IP refused 3 times in
  10 minutes gets a 10-minute ZLINE. LAN IPs and OperServ exceptions skip it.
- `session_limit` (3) caps connections per IP; OperServ `EXCEPTION` raises it
  for an IP, a CIDR range or a host name (`*.example.org`, checked once the
  hostname is known). A limit of 0 means no limit, as in Anope.
- `[[dnsbl]]` lists check new connections against DNS blocklists.
- `[[webirc]]` gateways are trusted to tell the real IP; list only your own
  gateway's IPs. A plain gateway password in a file others can read is warned
  about at start.
- TLS: 1.2 and 1.3 only; on 1.2 only forward secret AEAD suites (ECDHE with
  AES-GCM or ChaCha20), as in Mozilla's "intermediate" profile.
- The control socket is a file (`0660`, owner `beeircd`), not a network port.
- Passwords (NickServ and oper) are stored as argon2id hashes. Channel
  history on disk is encrypted with `[history] secret`.

## beeircd.conf

TOML. Times are written like `90s`, `5m`, `8h`, `30d`. Relative paths are read
from the config file's folder. Anything left out takes the default shown.
Unknown settings are refused, so a typo shows up in `beeircd check`.

### [server]

| Setting | Default | Meaning |
| --- | --- | --- |
| `name` | – | The server's name, like `irc.example.org` (restart) |
| `network` | – | One word, like `ExampleNet` (restart) |
| `description` | network + " IRC" | Shown in WHOIS and LINKS |
| `sid` | – | Server ID for linking: a digit and two capitals or digits, like `0EX` (restart) |
| `resolve_hosts` | `true` | Look up hostnames of connecting IPs |
| `find_place_by_ip` | `true` | Ask ipinfo.io once where this server's own IP address is, for the Hive Panel's network map (city level, rounded to about 10 km; the answer is kept next to the database). Only the server's own address is sent. Not asked when a `[[map.place]]` block places this server; `false`: its time zone places it |
| `prefixes` | `"@+"` | `"@+"` or `"~&@%+"` (owner, admin, op, halfop, voice) (restart) |
| `casemapping` | `"ascii"` | How nicks and channels ignore case: `"ascii"` (only A-Z: `Belon` = `belon`, but `[` and `{` differ, like UnrealIRCd 6 and InspIRCd) or `"rfc1459"` (also `[]\~` = `{}|^`). Every linked server needs the same; UnrealIRCd links need ascii. Changing it moves the stored nicks and channels to the new rule at the next start; going to rfc1459 is refused when two stored names would become one (restart) |
| `ident_lookup` | `false` | Ask the ident server (port 113) of every connecting computer for the username; with an answer the username shows without the leading `~`. Off by default: almost nobody runs an ident server any more, and each login waits up to 3 seconds for the answer. Not asked on WebSocket or `proxy` ports |
| `hide_servers` | `false` | Keep the server list from people who are not opers: LINKS and MAP only show the server they are on |
| `connect_modes` | – | User modes everyone gets when they connect, like `"+R"` (on top of `+i`, and `+x` with cloaking) |
| `oper_modes` | – | User modes set at `/oper`, like `"+WH"` |
| `oper_join` | – | Channels an oper joins at `/oper`, like `["#opers"]` |
| `ping_cookie` | `false` | Every new connection must answer a PING with a random word before it is let in; this stops programs that send without reading |
| `web_redirect` | – | A web address, like `"https://chat.example.org/"`: browsers that open an IRC port by mistake are sent there |
| `oper_tag` | – | A short text after every oper's rank in WHOIS, like `"(IRCop)"`: "is a Network Administrator (IRCop)" |
| `ircv3_drafts` | `false` | Offer the IRCv3 drafts that aren't final yet to clients that ask: `draft/read-marker` (read position shared by all your devices), `draft/pre-away` (bouncers and phones set AWAY while connecting), `draft/extended-isupport` (the 005 list before sign-in with `ISUPPORT`), `draft/multiline` (a pasted block of up to 24 lines or 4096 bytes is one message, checked once by the spam filter and flood rules; clients without it and other servers get the lines one by one), `draft/message-redaction` (`REDACT` takes a message back: the sender for 15 minutes, channel ops and IRC operators at any time; clients with it hide the message, +H history forgets it, other BeeIRCd servers pass it on; clients without it still show the message), `draft/account-registration` (`REGISTER <nick|*> <email|*> <password>` and `VERIFY <nick> <code>` from a client's sign-up screen, with NickServ's rules: `force_email`, `registration`, `reg_delay`; offered on the server that runs services, once the connection is complete). Clients that don't ask see no change. AdminServ `SET ircv3-drafts` switches it live (clients with cap-notify hear CAP NEW / DEL) |

### [[listen]]

One block per port.

| Setting | Default | Meaning |
| --- | --- | --- |
| `address` | – | `":6697"`, `"203.0.113.5:6697"`, `"[::]:6697"` |
| `tls` | `false` | Encrypted port |
| `proxy` | `false` | Connections start with a PROXY v1/v2 header ([INSTALL.md](INSTALL.md#behind-a-proxy-or-a-tunnel)) |
| `proxy_from` | – | IPs or CIDRs allowed on a proxy port (needed with `proxy`) |
| `websocket` | `false` | IRC over WebSocket for web pages (`ws://`, or `wss://` with `tls`), as in the IRCv3 WebSocket spec: one line per message, subprotocols `binary.ircv3.net` and `text.ircv3.net` (text when the page asks for none). With `proxy`, the web server in front gives the real IP in `X-Forwarded-For` instead of a PROXY header ([INSTALL.md](INSTALL.md#websocket-for-web-clients)) |
| `origins` | any page | WebSocket ports: the web pages that may connect, as `"https://chat.example.org"` or a host like `"*.example.org"`; others get 403, and so do clients that send no Origin (most non-browser WebSocket clients) |

### [tls]

| Setting | Default | Meaning |
| --- | --- | --- |
| `cert`, `key` | `tls/cert.pem`, `tls/key.pem` with acme | Certificate files, picked up again within a minute when they change |
| `self_signed` | `false` | Make a self-signed pair when the files are missing |
| `acme` | `false` | Get and renew a Let's Encrypt certificate |
| `acme_email` | – | Let's Encrypt's expiry mails |
| `acme_domains` | the server name | Names on the certificate |
| `acme_directory` | Let's Encrypt | Another ACME CA (staging, a private CA) |
| `acme_http` | `":80"` | Where the Let's Encrypt check is answered while asking |
| `sts` | on with a real certificate | Tell clients to use TLS from now on |
| `sts_port` | the first TLS port | The TLS port clients are sent to |

### [motd]

| Setting | Default | Meaning |
| --- | --- | --- |
| `file` | – | The message of the day |
| `text` | – | Used when there is no file |
| `oper_file` | – | A message for opers: shown after `/oper` and by `OPERMOTD` |
| `oper_text` | – | Used when there is no `oper_file` |
| `bot_file` | – | A message for bots, shown by `BOTMOTD` |
| `bot_text` | – | Used when there is no `bot_file` |
| `rules_file` | – | The network's rules, shown by `RULES` |
| `rules_text` | – | Used when there is no `rules_file` |
| `quotes_file` | – | A file with one quote per line; everyone gets a random one when they connect |

### [cloak]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Hide people's IPs and hosts (user mode +x) |
| `secret` | – | At least 16 random characters; needed when enabled with method `beeircd`. Changing it changes every cloak; linked servers need the same one |
| `method` | `"beeircd"` | `"beeircd"` (our own cloaks), `"unreal"` (exactly UnrealIRCd 6's cloaks, cloak_sha256) or `"cloak_inspircd"` (exactly the cloaks of the cloak_inspircd UnrealIRCd module). Use the one your UnrealIRCd servers load, so a person has one cloak everywhere and bans on cloaks work on every server (restart) |
| `keys` | – | With `method = "unreal"` or `"cloak_inspircd"`: the three `set::cloak-keys` of your UnrealIRCd servers, in the same order, like `keys = ["...", "...", "..."]` (a-z A-Z 0-9 mixed; 80 to 1000 characters each for `unreal`, 5 to 100 for `cloak_inspircd`, the modules' own rules) |
| `prefix` | `"Clk"` | With `method = "unreal"`: UnrealIRCd's `set::cloak-prefix`, the start of hostname cloaks |

Linked servers compare their cloak keys when they link (NETINFO); a
difference is a warning to opers, not a refusal.

### [services]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Run NickServ, ChanServ and the other bots here. A network has **one** services server: set `false` on every other BeeIRCd; their users reach the services server's bots over the link (`/ns`, `/cs` ... work as before). Two linked servers that both run services are refused, and so is a link to an UnrealIRCd network that has its own services (Anope) while ours run (restart) |
| `server_name`, `server_sid` | `services.<domain>`, our SID with `9` first | The pseudo-server the bots sit on when this server links (`irc.example.net` gives `services.example.net`; SID `0BE` gives `9BE`). UnrealIRCd servers U-line this name. Not used when standalone (restart) |
| `database` | `/var/lib/beeircd/services.db` | Accounts, nicks, channels, memos, bans (restart). With services off it still keeps the network bans that arrive over links |
| `admin` | – | The admin's account: every oper flag, can't be changed from IRC |
| `admin_password` | – | argon2id hash from `beeircd mkpasswd` (the installer sets it). Empty: the admin's first `/oper`, identified to the admin account, sets it, but only when `admin_hosts` is set and they come from one of those hosts; without `admin_hosts` the admin can't /oper until it is in the file |
| `admin_hosts` | anywhere | `user@host` masks or CIDRs the admin may /oper from |
| `admin_vhost` | – | Host the admin shows once opered |
| `log_channel` | – | Services log every command and oper event here, like `#services` |
| `log_connects` | `true` | Also log connects, quits and nick changes there |
| `nick_expire` | `90d` | Accounts unused this long are dropped |
| `chan_expire` | `60d` | Channels no access-list member joined this long are dropped |
| `identify_within` | `60s` | Time to identify on a protected nick |
| `kill_quick` | `20s` | What NickServ `SET KILL QUICK` gives |
| `allow_kill_immed` | `false` | People may choose `SET KILL IMMED` |
| `ban_expiry` | `30d` | AKILL, ZLINE, SHUN and friends added without an expiry |
| `guest_prefix` | `"Guest"` | Start of the nick given to people who didn't identify |
| `max_logins` | `10` | Connections identified to one account at once |
| `reg_delay` | `30s` | Time online before `REGISTER` works (`0s` off) |
| `cert_max` | `5` | Certificate fingerprints per account |
| `ajoin_max` | `10` | Auto-join list length |
| `list_max` | `50` | Entries NickServ `LIST` shows |
| `force_email` | `true` | `REGISTER` needs an email |
| `registration` | `"none"` | How new accounts start, as Anope's `registration`: `"none"` ready at once; `"mail"` REGISTER needs an email and mails a passcode for `/ns confirm <passcode>` (needs `[mail]`; `/ns resend` mails it again); `"admin"` an oper confirms each one with `/ns confirm <nick>`. Until confirmed an account can identify but not register channels |
| `default_language` | `"en"` | Language NickServ, ChanServ, MemoServ, HostServ, BotServ and HelpServ answer in for people who didn't pick one with `/ns set language`: `"en"` English or `"ms"` Bahasa Melayu. OperServ and AdminServ stay English |
| `unconfirmed_expire` | `1d` | Unconfirmed accounts are dropped this long after REGISTER (`0s` never) |
| `max_memos` | `20` | Memo limit of a new account |
| `memo_send_delay` | `3s` | Wait between two memos (`0s` off; opers don't wait) |

### [limits]

| Setting | Default | Meaning |
| --- | --- | --- |
| `max_users` | `5000` | People connected at once |
| `session_limit` | `3` | Connections from one IP (OperServ exceptions raise it) |
| `connect_rate` | `"5:60"` | Connections one IP may open per seconds; `"off"` for none |
| `max_unregistered` | `500` | Connections still logging in, in total |
| `max_unregistered_per_ip` | `3` | Connections still logging in from one IP |
| `max_channels` | `30` | Channels one person can join |
| `nick_length` | `30` | |
| `channel_length` | `50` | |
| `topic_length`, `kick_length` | `390` | |
| `away_length` | `200` | |
| `flood_burst` | `10` | Lines at once before slowing down |
| `flood_rate` | `2` | Lines per second after that (slowed, not disconnected) |
| `ping_interval`, `ping_timeout` | `90s` | |
| `register_timeout` | `60s` | Time to finish logging in |
| `sendq` | `1048576` | Bytes queued to one client before it is dropped |
| `utf8_only` | `false` | Refuse text that isn't UTF-8 (`FAIL ... INVALID_UTF8`) and tell clients `UTF8ONLY`. Leave off if people use old clients that send Latin-1 |
| `disabled_commands` | – | Commands switched off for everyone but opers, like `["KNOCK", "WHOWAS"]`; the ones needed to connect can't be listed |
| `restrict_msg` | `false` | People can send private messages to opers and services only; opers can write to anyone |
| `abbreviations` | `false` | A command may be shortened by ending it with a full stop, like `WHOW.` for WHOWAS; when more than one command fits, the server lists them |

### [channels]

| Setting | Default | Meaning |
| --- | --- | --- |
| `flood_profile` | `"normal"` | The +F profile for channels without their own: `very-strict`, `strict`, `normal`, `relaxed`, `very-relaxed`, `off` |
| `create` | `"everyone"` | Who may make a new channel: `"everyone"`, `"registered"` (logged in to NickServ) or `"opers"` |
| `hide_bans` | `false` | Only ops (and opers) may read a channel's ban list; the exception and invite lists are for ops only either way |

### [history]

Channel history for `+H` and IRCv3 `CHATHISTORY`. Private messages are not
kept.

| Setting | Default | Meaning |
| --- | --- | --- |
| `default` | – | The `+H` new channels get, like `"50:1d"` (lines:time) |
| `playback_lines` | `15` | Lines shown on join to clients that can't fetch history (`0` off) |
| `playback_time` | `1d` | How old those lines may be |
| `max_lines_registered` | `5000` | Most `+H` lines for a ChanServ channel |
| `max_lines_unregistered` | `200` | For other channels |
| `max_time` | `31d` | Oldest history kept |
| `max_memory_mb` | `32` | Memory cap for all history; the oldest lines go first |
| `secret` | – | Encrypts `history.db`. Empty: history is lost on restart. A new secret can't read the old file (restart) |
| `file` | `history.db` next to the database | (restart) |

### [[dnsbl]]

One block per DNS blocklist.

| Setting | Default | Meaning |
| --- | --- | --- |
| `name` | – | The list's zone, like `"dnsbl.dronebl.org"` |
| `replies` | every answer | Answers (`127.0.0.N`) that count |
| `action` | `"reject"` | `reject`, `zline`, `akill` or `shun` |
| `ban_time` | `24h` | For zline, akill and shun |
| `reason` | "Your IP %i is listed in %b (%r)" | `%i` IP, `%b` list, `%r` answer, `%n %u %h` nick, ident, host, `%N` network |
| `enabled` | `true` | |

### [[webirc]]

One block per trusted web chat gateway (BeeRoom, The Lounge, KiwiIRC). The
gateway sends `WEBIRC` before NICK and USER with each person's real IP, so
Z-lines, bans, `session_limit`, the connect throttle, DNSBL, cloaks and WHOIS
use the person, not the gateway. The gateway's own IP skips the per-IP limits.
WEBIRC from any other IP, or with a wrong password, closes the connection with
an oper notice.

| Setting | Default | Meaning |
| --- | --- | --- |
| `name` | – | Shown to opers in WHOIS and the connect notice, like `"beeroom"` |
| `password` | – | The secret the gateway sends: 16+ random characters, or a `beeircd mkpasswd` hash (slower: checked for every connection) |
| `hosts` | – | The gateway's IPs or CIDRs, like `["127.0.0.1"]`; names are not allowed |

The person counts as TLS (+z) only when the gateway says `secure` and its own
link to BeeIRCd is TLS or on the same machine.

The easy way to add one (the BeeRoom installer does this for you):

```sh
sudo beeircd webirc add beeroom 127.0.0.1
```

It adds the block, makes a password, re-reads the config and prints the
password to put in the web chat's settings (`-q` prints only the password).
A gateway on another machine: give that machine's IP instead.

```toml
[[webirc]]
name = "beeroom"
password = "a-long-random-secret-here"
hosts = ["127.0.0.1"]
```

### [spamfilter]

The rules themselves are in the services database (see [Spam filter](#spam-filter)).

| Setting | Default | Meaning |
| --- | --- | --- |
| `except` | none | Channels whose messages, notices, parts and topics are never filtered, like `["#help"]` |
| `except_groups` | none | [Security groups](#known-and-new-people) whose people are never filtered, like `["known-users"]` |

### [censor]

Words that channel mode `+G` hides. Ops set `+G` on a channel; from then
on these words in its messages become the `replace` text. Halfops and up,
and opers with flag `g`, are not censored. `beeircd rehash` applies changes.

```toml
[censor]
words = ["darn", "*spam*"]
```

| Setting | Default | Meaning |
| --- | --- | --- |
| `words` | none | Single words, matched whole and in any case; `*` stands for any letters (`*spam*` also hides `spammers`). Up to 500. Empty: `+G` changes nothing |
| `replace` | `<censored>` | What each hidden word becomes |

### [mail]

For NickServ `RESETPASS`, account confirmation and MemoServ mail notices.
Off by default. Easiest set up with the Mail card in the Hive Panel
(Settings), or check it with `beeircd mail-test you@example.org`.

| Setting | Default | Meaning |
| --- | --- | --- |
| `use_mail` | `false` | Switch mail on |
| `send_from` | – | From address |
| `from_name` | – | The name people see as the sender, like `"BeeNet Services"` |
| `smtp_server` | – | `"smtp.example.org:587"` (STARTTLS when offered), or port 465 (TLS from the start); empty: sendmail. The Hive Panel's Mail card fills it in for Gmail, Outlook and Zoho |
| `smtp_user`, `smtp_password` | – | |
| `sendmail_path` | `"/usr/sbin/sendmail -t"` | |
| `delay` | `5m` | Wait between two mails per person (`0s` none) |
| `dont_quote_addresses` | `false` | |
| `reset_subject`, `reset_message`, `registration_subject`, `registration_message`, `memo_subject`, `memo_message` | Anope's texts | `%n` nick, `%N` network, `%c` code, `%s` sender, `%d` memo number, `%t` memo text |

### [hostserv]

| Setting | Default | Meaning |
| --- | --- | --- |
| `vhost_chars` | letters, digits, `.` and `-` | Characters a vhost may use |
| `allow_undotted` | `false` | Vhosts without a dot |
| `disallow_start_or_end` | `".-"` | Characters a vhost may not start or end with |
| `host_length` | `64` | |
| `ident_length` | `10` | |
| `activate_on_set` | `false` | A vhost an oper sets shows at once |
| `memo_user` | `false` | Memo the user when a request is approved or rejected |
| `memo_opers` | `false` | Memo opers about new requests |

### [[operclass]]

Usually managed with AdminServ `CLASS`. A block with a built-in name replaces
that class.

| Setting | Meaning |
| --- | --- |
| `name` | Up to 30 letters, digits, `_` or `-` |
| `flags` | Letters from `abcghks` |
| `title` | WHOIS line, like "is a Network Administrator" |
| `vhost` | Host shown after `/oper` |
| `swhois` | Up to 5 extra WHOIS lines |

In WHOIS an oper has one rank line: their own title, else their class's
title, else "is an IRC Operator". Their extra lines come right under it.
An oper with no title whose first extra line reads like one ("is the
Network Overwatch") has that line as their rank, and an extra line that
only repeats the rank is left out. `[server] oper_tag = "(IRCop)"` puts
that text after every rank: "is a Network Administrator (IRCop)". Every
BeeIRCd server on the network shows the same lines.

What only opers see about someone's connection (their modes and real
address near the top; security groups, country, network and reputation
after the secure-connection line) follows UnrealIRCd's order.

### [control]

| Setting | Default | Meaning |
| --- | --- | --- |
| `socket` | `beeircd.sock` next to the database | The socket the admin commands use (restart) |

### [backup]

| Setting | Default | Meaning |
| --- | --- | --- |
| `dir` | `backups` next to the database | |
| `keep` | `7` | Dated backups kept |

### [geoip]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Look up each user's country and network |
| `download` | `true` | Fetch DB-IP Lite when a file is missing or over 35 days old |
| `country_db` | `geoip/country.mmdb` next to the database | Country file (MaxMind DB format) |
| `asn_db` | `geoip/asn.mmdb` next to the database | Network (ASN) file |

### [reputation]

See [Known and new people](#known-and-new-people).

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Keep a reputation score per IP |
| `channel_members` | `3` | Only people in a channel with at least this many members earn points (`0` = everyone) |

### [[security_group]]

Anyone matching one item is in the group, unless they match an item
under `[security_group.exclude]` (the same items).

| Setting | Meaning |
| --- | --- |
| `name` | Up to 30 letters, digits, `_` or `-`; a built-in name replaces that group (`unknown-users` can't be changed) |
| `identified` | `true`: logged in to an account |
| `webirc`, `websocket`, `tls` | `true`: came through a web gateway, on a WebSocket port, with TLS |
| `reputation_score`, `reputation_below` | A score of at least, or less than, this (1 to 10000) |
| `connect_time`, `connect_below` | Connected at least, or less than, this long, like `"1h"` |
| `mask` | Masks or extbans, like `["*@*.example.net", "~C:NL", "~a:*"]` |
| `ip` | Addresses or ranges, like `["192.0.2.0/24"]` |
| `server_port` | The port of this server they connected to |
| `security_group` | In one of these other groups |
| `exclude` | The block of items that keep people out |

### [connthrottle]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Slow down floods of new people |
| `local` | `20:60` | New people (not in `except`) per seconds on this server; `off` for none |
| `global` | `30:60` | The same for the whole network; `off` for none |
| `except` | `["known-users"]` | Groups never throttled |
| `start_delay` | `3m` | Off for this long after the server starts |
| `gathering` | `7d` | Off until reputation has been gathered this long |
| `reason` | UnrealIRCd's text | What a refused person is told |

### [[restrict]]

| Setting | Meaning |
| --- | --- |
| `command` | A command, like `"list"`, `"invite"`, `"knock"`, or `private-message`, `private-notice`, `channel-message`, `channel-notice`, `channel-create`, `join` |
| `connect_time` | Allowed once connected this long (up to `24h`); none: only the `except` groups may use it |
| `except` | Groups that may always use it, like `["known-users"]` |

### [antirandom]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Refuse people whose nick, username and real name look like random letters |
| `threshold` | `7` | The score from which someone is refused; lower catches more and makes more mistakes |
| `action` | `"kill"` | `"kill"` refuses the connection, `"warn"` only tells the opers |
| `reason` | UnrealIRCd's text | What a refused person is told |
| `except` | `["known-users", "webirc-users"]` | Groups never checked |

### [antimixedutf8]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Refuse messages that mix alphabets inside words |
| `score` | `10` | Refused from this score: each change of alphabet counts 1, and 1 more inside a word |
| `reason` | `Possible mixed character spam` | What the sender is told |
| `except` | none | Groups never checked |

### [targetfloodprot]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Cap the messages one channel or person can be sent, by everyone on this server together (each server counts for itself) |
| `channel_privmsg` | `45:5` | Messages per seconds to one channel |
| `channel_notice` | `15:5` | Notices to one channel |
| `channel_tagmsg` | `15:5` | Tag-only messages (like typing notes) to one channel |
| `private_privmsg` | `30:5` | Private messages to one person |
| `private_notice` | `10:5` | Notices to one person |
| `private_tagmsg` | `10:5` | Tag-only messages to one person |

### [blockamsg]

| Setting | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | The same text sent to several channels at once only reaches the first |
| `delay` | `200ms` | The same text to another channel within this time counts as one /amsg (up to `10s`) |

### [auth]

| Setting | Default | Meaning |
| --- | --- | --- |
| `source` | off | Where NickServ passwords are also checked: `"ldap"` or `"sql"` (see "Logins from LDAP or a database") |
| `local_passwords` | `true` | Passwords set in BeeIRCd keep working next to the outside ones; `false` = only the outside source counts, and REGISTER and SET PASSWORD are off |
| `create` | `true` | Make the NickServ account the first time someone the outside source knows logs in |
| `opers` | `false` | Ask the outside source for oper passwords too (`/oper` and the Hive Panel), under the oper's account name |
| `timeout` | `5s` | How long one question to the outside source may take |
| `ldap` | – | The `[auth.ldap]` block: the directory, see the next table |
| `sql` | – | The `[auth.sql]` block: the database, see the last table |

In `[auth.ldap]`:

| Setting | Default | Meaning |
| --- | --- | --- |
| `url` | – | The directory: `"ldaps://host"` (port 636) or `"ldap://host"` (port 389) |
| `starttls` | `false` | Switch an `ldap://` connection to TLS before anything is sent |
| `verify` | `true` | Check the directory's certificate |
| `ca_file` | – | The directory's own CA certificate (PEM), when it is not a public one |
| `bind_dn` | – | The account BeeIRCd searches with; empty = search without signing in |
| `bind_password` | – | Its password |
| `base_dn` | – | Where people are, like `"ou=people,dc=example,dc=org"` |
| `filter` | `(uid=%s)` | How to find someone; `%s` is the name |
| `user_dn` | – | Instead of a search: where an entry is, like `"uid=%s,ou=people,dc=example,dc=org"` |
| `email_attr` | `mail` | The attribute with the email address for new accounts |
| `oper_filter` | – | With `opers`: the filter for oper logins, to let only a group oper |

In `[auth.sql]`:

| Setting | Default | Meaning |
| --- | --- | --- |
| `driver` | – | `"mysql"` (also MariaDB), `"postgres"` or `"sqlite"` |
| `dsn` | – | How to reach the database, in the driver's own form (for SQLite: the file) |
| `query` | – | Gives the stored password and, as a second column, the email; `$name` is the name |
| `hash` | `auto` | How passwords are stored: `auto` (bcrypt and argon2id), `bcrypt`, `argon2`, `sha256`, `sha512`, `md5`, `plain`, or `query` (the query compares `$password` itself) |
| `oper_query` | – | With `opers`: the query for oper logins |
| `log_query` | – | Run after every login try with `$time`, `$name`, `$ip`, `$result` |

### [rpc]

| Setting | Default | Meaning |
| --- | --- | --- |
| `listen` | off | Where the JSON-RPC admin API answers over HTTPS and WebSocket, like `"127.0.0.1:8600"`; needs at least one `[[rpc.user]]` block (restart) |
| `socket` | off | A socket file for the API without a password, like `"/run/beeircd/rpc.sock"`: whoever may open it may call everything (restart) |
| `name` | – | In a `[[rpc.user]]` block: the name a tool signs in with (letters, digits, `_`, `.`, `-`) |
| `password` | – | In a `[[rpc.user]]` block: its password as a hash from `beeircd mkpasswd` |
| `allow` | every method | In a `[[rpc.user]]` block: the methods it may call, like `["user.*", "server_ban.list"]` |
| `ips` | this machine only | In a `[[rpc.user]]` block: other addresses or ranges it may call from; this machine may always call |

### [[showfile]]

A command of your own that shows a text, like `/FAQ`. One block per command; a name BeeIRCd already uses keeps its own meaning.

| Setting | Meaning |
| --- | --- |
| `command` | The command name, one word, like `"FAQ"` |
| `file` | The text file to show |
| `text` | Used when there is no `file` |
| `title` | The first line shown; the command name when not set |
| `opers` | `true` shows it to opers only |

### [extjwt]

Signed tokens (IRCv3 EXTJWT) that tell another service, like a video call or a file upload, who someone is on IRC. People ask with `EXTJWT <#channel|*> [service]`; the other service checks the token with the same secret. Off until a secret is set.

| Setting | Default | Meaning |
| --- | --- | --- |
| `secret` | – | Signs the tokens asked for without a service name; in a service block, the secret of that service |
| `method` | `"HS256"` | `"HS256"`, `"HS384"` or `"HS512"` |
| `expire` | `30s` | How long a token is good for |
| `service` | – | `[[extjwt.service]]` blocks, one per named service, each with the two settings below and its own `method` and `expire` when they differ |
| `name` | – | In a service block: the name people give in `EXTJWT <target> <name>` |

A token holds the nick (`sub`), the server (`iss`), the account, the user modes and when it runs out (`exp`); for a channel also the channel name, when the person joined and their rank there.

### [[deny_dcc]]

| Setting | Default | Meaning |
| --- | --- | --- |
| `filename` | – | File names refused in DCC SEND, with `*` and `?`, like `"*.exe"` |
| `reason` | a standard text | What the sender is told |
| `soft` | `false` | `true`: only held back until the receiver allows the sender with `/DCCALLOW +nick`. `false`: refused for everyone |

### [[allow_dcc]]

| Setting | Default | Meaning |
| --- | --- | --- |
| `filename` | – | File names let through although a `[[deny_dcc]]` block of the same kind (the same `soft`) matches, like `"*.txt"` under a deny of `"*"` |
| `soft` | `false` | Which kind of `[[deny_dcc]]` block this is an exception to |

### [update]

| Setting | Default | Meaning |
| --- | --- | --- |
| `check` | `true` | Look once a day for a newer release |
| `url` | GitHub releases | Folder holding `release.json` |
| `rc_url` | GitHub releases | Where `update --rc` looks |

### [web]

The Hive Panel (`beeircd web`, the `beeircd-web` service). The server reads only `members` and `chat_url`.

| Setting | Default | Meaning |
| --- | --- | --- |
| `listen` | `127.0.0.1:8787` | Where the panel answers; a loopback address only. In Docker the installer writes `:8787` and compose.yaml publishes it on `127.0.0.1` of the Docker machine; leave it so, or the panel can't be reached |
| `trusted_proxy` | none | Reverse proxies (IP addresses) whose forwarded address and https the panel believes |
| `session_idle` | `30m` | Signed out after this long without a click (1m to 24h) |
| `members` | `false` | Registered nicks may sign in with their NickServ password, to the YOU pages only (Home, Chat, My nick, My channels, Memos). Also the Members switch in Settings (saved here, applied at once) |
| `chat_url` | none | BeeRoom's address, like `https://chat.example.org`: the Chat tab shows it, signed in as the person with a key that works once, for one minute. BeeRoom's `frameAncestors` (or `BEEROOM_PANEL` in Docker) must list the panel's address |

The panel's fonts (Inter, Space Grotesk, JetBrains Mono) are bundled
under the SIL Open Font License; the licences are in
`internal/hivepanel/ui/fonts`.

### [map]

Where the servers are on the Hive Panel's network map: one `[[map.place]]`
block per server, written by the panel's Move and Place or by hand. A place
here always wins over what a server says about itself.

| Setting | Default | Meaning |
| --- | --- | --- |
| `server` | – | The server's name, like `"eu.example.net"` |
| `name` | – | What the map calls the place, like `"Frankfurt"` (up to 60 letters) |
| `lat`, `lon` | – | Where it is: latitude -90 to 90, longitude -180 to 180 |

```toml
[[map.place]]
server = "eu.example.net"
name = "Frankfurt"
lat = 50.11
lon = 8.68
```

### [link]

Server linking (part of 1.0; walk-through in [Linking servers](#linking-servers)).
Linked servers share users, channels, every change, network bans and one
set of services. BeeIRCd speaks the UnrealIRCd 6 server protocol, so it
links with BeeIRCd and UnrealIRCd 6, and translates to and from InspIRCd
4's (protocol 1206) for InspIRCd links.

| Setting | Default | Meaning |
| --- | --- | --- |
| `role` | `"standalone"` | `standalone`, `hub` or `leaf` (restart). A leaf keeps one link |
| `address` | all addresses | IP the link port listens on, like `127.0.0.1` (restart) |
| `port` | `7000` for hub/leaf | TLS port for server links (restart) |
| `cert`, `key` | next to services.db | This server's link certificate, made at the first start as a hub or leaf and never renewed, so its fingerprint stays the same. It is in every backup |
| `hide_split` | `false` | In a netsplit, show non-opers `*.net *.split` as the quit reason instead of the two server names (opers always see them) |
| `remote_admin` | `false` | Let admins on other servers run AdminServ commands here (`/as server <this server> SET ...`) and use this server's AdminServ. Off: only people on this server change it; `SERVER <name> STATUS` (read-only) works either way |

Each server we link with gets a `[[link.server]]` block. These apply on
`beeircd rehash`; removing one drops that link.

| Setting | Default | Meaning |
| --- | --- | --- |
| `name` | – | The other server's name |
| `fingerprint` | – | SHA-256 of its public key, base64: `beeircd link-info` on BeeIRCd, `./unrealircd spkifp` on UnrealIRCd. Required; a link showing another key is refused before anything is sent |
| `password` | none | Sent with PASS and must come back the same. Optional and best left out: the fingerprint is the lock (the other server must hold its private key), as UnrealIRCd recommends with `{ spkifp; }`. Use one only when the other side needs it (Anope, an UnrealIRCd block with a plain password). It has to be sent, so it is stored as written; `beeircd check` warns when beeircd.conf holding one is readable by every user, or when the link key is readable by others |
| `host`, `port` | –, `7000` | Where to connect; no host = it connects to us |
| `autoconnect` | `false` | Keep connecting while the link is down (10 s after a lost link, then 30 s doubling to 5 min) |
| `sendq_mb` | `20` | How much may wait for the link before it is dropped |
| `protocol` | `"unrealircd"` | What the other server speaks: `"unrealircd"` (BeeIRCd and UnrealIRCd 6) or `"inspircd"` (InspIRCd 4). Only needed when we connect to it; an InspIRCd connecting to us is recognised by its first line. `"inspircd"` makes `password` required, as InspIRCd's `<link>` needs one |

**Services on a linked network.** One server runs services (the hub,
usually); every other BeeIRCd has `[services] enabled = false`. On the
services server the bots sit on their own pseudo-server,
`services.<domain>` (see `server_name`), linked behind it. People on any
server use them as usual: `/ns`, `/cs` ..., SASL (relayed to the
services server, `904 ... services are unavailable` while it is not
linked), AJOIN, oper rights (each server says who its opers are and with
which flags; an UnrealIRCd oper gets the flags of its oper class:
netadmin, admin, globop, locop). Network bans, SQLINE (sent as UnrealIRCd
Q-lines) and DEFCON apply everywhere; SNLINE reaches BeeIRCd servers only
(UnrealIRCd has no realname ban to send it as).

**Linking an UnrealIRCd 6 server** to a BeeIRCd that runs services: give
it a `link` block with our fingerprint (`beeircd link-info` prints it),
and U-line the services server and name it, or SASL stays off there:

    ulines { services.example.net; }
    set { services-server "services.example.net"; }

A BeeIRCd with services off can also link to an UnrealIRCd network that
runs Anope: logins (SASL) go to Anope.

**Linking an InspIRCd 4 server.** InspIRCd 4 (protocol 1206, tested
with 4.12) links over TLS with a password on both sides and pinned
certificates. On the InspIRCd side load `ssl_openssl`, `spanningtree`,
`services`, `cap` and `sasl`, give links an `<sslprofile>` and a TLS
`<bind type="servers">`, and add the blocks `beeircd link-info` prints:

    <link name="bee.example.net" ipaddr="bee.example.net" port="7000"
          allowmask="*" sslprofile="Servers"
          sendpass="PASSWORD" recvpass="PASSWORD"
          fingerprint="hex SHA-256 of our certificate">
    <uline server="bee.example.net" silent="yes">
    <uline server="services.example.net" silent="yes">
    <sasl target="services.example.net">

InspIRCd's `fingerprint` is the hex SHA-256 of the whole certificate
(`link-info` prints ours). Ours is of its public key, base64; for the
InspIRCd certificate:

    openssl x509 -in cert.pem -pubkey -noout | openssl pkey -pubin -outform der \
      | openssl dgst -sha256 -binary | base64

Then our block, `beeircd rehash`, and `/CONNECT` from either side:

    [[link.server]]
    name = "insp.example.net"
    host = "insp.example.net"
    port = 7005
    protocol = "inspircd"
    password = "PASSWORD"
    fingerprint = "base64 from the command above"
    autoconnect = true

Everything shared on a BeeIRCd link works: users, channels, modes,
bans, topics, away, nick changes and collisions, kills, services
(NickServ, ChanServ, SASL from InspIRCd users), network bans (G, Z, Q,
E-lines, shuns) and CHGHOST/CHGIDENT/CHGNAME. Modes are matched by name,
so letters may differ; a mode one side doesn't have is not sent (BeeIRCd
`+f`, `+F`, `+E`, `+B`, `+V`, `+Z`; owner and admin ranks reach InspIRCd
as op when it has no `customprefix` ranks for them). Load `banexception`
and `inviteexception` there for `+e` and `+I`. Remote `/STATS`, `/MOTD`
and AdminServ `SERVER` don't reach an InspIRCd server.

**Anope as the services server.** Instead of the built-in services, a
BeeIRCd network can use Anope 2.0 (tested with 2.0.20). Anope links to
one BeeIRCd as an UnrealIRCd server. On that BeeIRCd, turn the built-in
services off and add a link block for Anope (`beeircd rehash` picks up
the block; changing `enabled` needs a restart):

    [services]
    enabled = false

    [[link.server]]
    name = "services.example.net"
    password = "PASSWORD"
    fingerprint = "base64 SHA-256 of Anope's certificate key (command above)"

In Anope's services.conf, point the uplink at our link port and use the
`unreal4` protocol module and a TLS certificate:

    uplink { host = "bee.example.net"; port = 7000; ssl = yes; password = "PASSWORD" }
    serverinfo { name = "services.example.net"; id = "00A" ... }
    module { name = "unreal4" }
    module { name = "m_ssl_openssl"; cert = "data/anope.crt"; key = "data/anope.key" }

Keep `m_sasl` loaded for SASL logins. Every Anope service works for
people on any BeeIRCd of the network: NickServ (REGISTER, IDENTIFY,
RECOVER, AJOIN), ChanServ (access lists, mode locks, topics, AKICK),
BotServ bots, MemoServ, HostServ vhosts, OperServ (AKILL becomes a
network ban, MODE, SVSNICK, KILL) and SASL. Anope keeps its own
database; to move an Anope network to the built-in services instead, see
[Moving from Anope](#moving-from-anope).

**Oper tools for links.** OperServ `STATS UPLINK` (each link, its lag
and software), `JUPE <server> [reason]` / `JUPE LIST` / `JUPE DEL`
(blocks a server name, splits it if linked), `NOOP SET|REVOKE <server>`
(nobody may be an IRC operator there), `DEFCON 1-5`, and `SERVER LIST |
ADD | SET | DEL | LINK | UNLINK` (writes the `[[link.server]]` blocks,
also in AdminServ). AdminServ `SERVER <name> STATUS` and
`SERVER <name> <command>` run on another BeeIRCd (SET, MOTD, OPER, CLASS,
SHOW, HISTORY, DIFF, UNDO, DNSBL; it needs `remote_admin = true` there;
ports, restarts and link blocks are changed in that server's shell). Each
remote change is logged on both servers with who and what.

**Link notices.** IRC operators (user mode `+s`) see each step of a
link as a server notice: `Connecting to <server> (host:port)` (once
while it keeps failing; the log has every try), `Link to <server>
failed: <why>`, `Link with <server> (SID, ip:port) established over TLS
1.3 (cipher), certificate pinned: <software>, we connected`, `Link with
<server>: sync done in 178ms: 86 users on 1 server`, `Server <name>
joined the network behind <server>`, a warning when a link hasn't
answered a PING for 30 seconds (and when it answers again), `Link with
<server> lost after 3h40m: <reason>, lost 86 users on 2 servers`, and
refusals (wrong password or fingerprint, unknown server).

**Splits and rejoins.** When a link goes, everyone behind it quits with
"near-server far-server" (or `*.net *.split`, see `hide_split`), inside an
IRCv3 `netsplit` batch for clients that asked for batches; when it comes
back both sides burst again (`netjoin` batch), nick clashes and channel
timestamps settle as on the first link, and for network bans the newest
copy of each mask wins. A leaf with `autoconnect` comes back by itself.

**Oper commands.** `/LINKS [mask]`, `/MAP`, `/CONNECT <server>` and
`/SQUIT <server> [reason]` (flag c), `/STATS l|c|k|u [server]`,
`/ADMIN`, `/MOTD`, `/VERSION`, `/TIME` with a server name ask that
server. `/KLINE [+time] <nick|user@host> [reason]` and `/UNKLINE` ban
from this server only (flag k; never sent over links; `STATS k` lists
them).

`beeircd link-info` prints this server's fingerprint and the blocks the
other side needs. Links use TLS 1.2+ only; plain links are never accepted.

**What a linked server may say.** A server speaks only for itself, the
servers behind it and their users: a line claiming to come from anyone
else is refused (logged once per link and kind). Services commands
(SVSLOGIN, SVSKILL, SVSJOIN, SVSPART, SVSNICK, SVSMODE, SVSNOOP, and
CHGHOST/CHGIDENT/CHGNAME sent by a server) are taken only from the
services server or the server it is linked behind; CHGHOST, SAJOIN and
the like sent by a user only from an IRC operator. A network ban or
Q-line from another server that matches everyone (`*@*`, `*`, `::/0`) is
refused, as OperServ refuses it here.
Opers with `+s` see every link event (established, refused and why, sync
time, split). `BEEIRCD_LINK_DEBUG=1` in the environment logs every line
sent and received on links.
