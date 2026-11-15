# BeeIRCd features

✅ = in BeeIRCd today · 🔜 = next release · 📋 = planned

How to set each one up is in [ADMIN.md](ADMIN.md); installing is in [INSTALL.md](INSTALL.md).

## Core server

| Feature | | Notes |
| --- | --- | --- |
| Registration, PING, QUIT, NICK, USER, PASS | ✅ | PASS is ignored (no server password) |
| JOIN, PART, KICK, INVITE, TOPIC, NAMES, LIST | ✅ | KICK takes `#a,#b nick1,nick2` pairs (RFC 2812) |
| PRIVMSG, NOTICE, TAGMSG, STATUSMSG (`@#`, `+#`) | ✅ | |
| WHO, WHOIS, WHOWAS, ISON, USERHOST, AWAY | ✅ | `WHO exactnick` shows +i users; `WHO *` also lists the services |
| MOTD, LUSERS, VERSION, TIME, INFO, HELP, HELPOP | ✅ | HELP covers every command and the channel modes |
| Input limits | ✅ | lines over 510 bytes or tags over 4096 bytes get 417 and are dropped, not cut |
| User modes `i w o s r x` | ✅ | |
| User modes `B d D R T p Z H W q z` | ✅ | UnrealIRCd letters and rules: bot, deaf, private-message filters, hidden channels, hidden oper, WHOIS notice, unkickable, TLS |
| Channel modes `o v b e I k l i m n p s t R M r` | ✅ | |
| Channel modes `c S C T N K V Q z Z D P O` | ✅ | UnrealIRCd letters and rules |
| Flood guard `+f [10j#R10,40m#M10]:15` and `+F` profiles | ✅ | UnrealIRCd syntax; a server-wide default profile |
| `+L` link channel, `+E` repeat, `+B` caps, `+J` rejoin delay | ✅ | |
| Channel history `+H lines:time`, playback on join, CHATHISTORY | ✅ | kept encrypted on disk (AES-256-GCM), so a restart keeps it |
| KNOCK, SILENCE, SETNAME, CHGNAME | ✅ | |
| SETHOST, SETIDENT, CHGHOST, CHGIDENT | ✅ | opers with flag h |
| SAJOIN, SAPART, SANICK, SAMODE, SATOPIC | ✅ | opers with flag a |
| Prefixes `@+` (default) or `~&@%+` | ✅ | `[server] prefixes` |
| TLS 1.2+, self-signed or your own certificate | ✅ | a changed certificate is picked up without a restart |
| Let's Encrypt built in | ✅ | HTTP-01, port 80 open only while asking, renewed by itself, loaded live |
| Host cloaking (HMAC) | ✅ | UnrealIRCd- and InspIRCd-compatible methods for mixed networks |
| Flood control, connect throttle, limits per IP | ✅ | opers are never slowed down |
| PROXY protocol v1 and v2 | ✅ | only from the proxies you list |
| DNS blocklists (DroneBL, EFnetRBL, any list) | ✅ | reject, Z-line, AKILL or SHUN; IPv4 and IPv6 |
| REHASH (`/rehash`, `beeircd rehash`, SIGHUP) | ✅ | applies what is safe live and lists what needs a restart |
| WebSocket | ✅ | `websocket = true` on a port: ws:// or wss://, IRCv3 binary/text subprotocols, allowed origins, real IP from the web server in front |
| `+G` censor (`[censor]` words) | ✅ | |
| Module system | 📋 | |

## Server linking

| Feature | | Notes |
| --- | --- | --- |
| Hub and leaf over TLS on a separate link port | ✅ | locked by certificate fingerprint (like UnrealIRCd's `spkifp`); a password is optional |
| Links to BeeIRCd, UnrealIRCd 6 and InspIRCd 4 | ✅ | UnrealIRCd 6 server protocol; InspIRCd 4 (protocol 1206) translated, modes matched by name |
| One network: users, channels, modes, topics, bans, services and oper entries shared | ✅ | nick collisions and channel timestamps handled like UnrealIRCd |
| Services on their own server, SASL relayed over links | ✅ | to BeeIRCd services or to Anope |
| Network bans (AKILL, ZLINE, SHUN, ELINE, Q-lines) shared live | ✅ | K-lines stay on one server |
| `/MAP`, `/LINKS`, `/CONNECT`, `/SQUIT`, remote `/STATS`, `/MOTD`, `/VERSION`, `/ADMIN`, `/TIME`, `WHOIS nick nick` | ✅ | |
| OperServ JUPE, NOOP, DEFCON, SERVER | ✅ | |
| IRCv3 netsplit and netjoin batches, optional hidden splits | ✅ | |
| `beeircd link-info` prints what the other server needs | ✅ | the installer can set up a leaf for you |
| Checks on every line a linked server sends | ✅ | a broken or hostile leaf can't speak for users or services |
| Anope as the services server | ✅ | Anope 2.0 (unreal4 protocol) links to a BeeIRCd with built-in services off; every service and SASL work network-wide |

## IRCv3

| Spec | |
| --- | --- |
| CAP 302, cap-notify | ✅ |
| message-tags, message-ids, server-time | ✅ |
| sasl (PLAIN, EXTERNAL) | ✅ |
| account-notify, account-tag, extended-join, away-notify | ✅ |
| batch, labeled-response, echo-message | ✅ |
| chghost, invite-notify, multi-prefix, userhost-in-names, setname | ✅ |
| client tags (typing, reply, react, channel-context) relayed | ✅ |
| MONITOR, extended-monitor | ✅ |
| WHOX (`WHO <mask> %<fields>[,<token>]`) | ✅ |
| bot mode (`+B`, `BOT=B`, `bot` tag) | ✅ |
| draft/no-implicit-names | ✅ |
| account-extban | ✅ |
| STS (only with a real CA certificate) | ✅ |
| WebIRC (`[[webirc]]` gateways) | ✅ |
| draft/chathistory | ✅ |
| UTF8ONLY (`[limits] utf8_only`, off by default) | ✅ |
| standard-replies: FAIL, WARN and NOTE with codes for BeeIRCd's own errors (spam filter, K-lines, SETHOST/CHGHOST, OPER, SANICK, SATOPIC, nick collisions); clients without it keep the server notices | ✅ |
| SASL SCRAM, draft/account-registration, draft/multiline, draft/read-marker, draft/message-redaction | 📋 |
| STARTTLS | not supported (deprecated) |

## Services

Commands, syntax and replies follow **Anope 2.0** (the stable line), with a few ConferenceRoom extras on top. HELP uses Anope's layout and hides commands you can't use.

| Service | |
| --- | --- |
| **NickServ**: REGISTER, IDENTIFY, SASL, RECOVER, GROUP, UNGROUP, ALIST, GLIST, INFO, DROP, LIST, ACCESS + SECURE, STATUS, CERT (identify by TLS certificate), AJOIN, SUSPEND, GETEMAIL, RESETPASS, email confirmation of new accounts (registration = "mail" or "admin", CONFIRM, RESEND; off by default), BUDDY list, and the SET / SASET options | ✅ |
| **ChanServ**: access levels and LEVELS (Anope's privileges and defaults), FLAGS (privilege letters as in Anope; ACCESS_LIST has its own letter l), xOP lists, AKICK, MODE LOCK, TOPIC, ENTRYMSG, ENFORCE, LOG, SUSPEND, CLONE, op tools (OWNER, PROTECT, HALFOP, UP, DOWN, KICK, BAN, UNBAN, INVITE, GETKEY), SEEN, and the SET options incl. OVERFLOW | ✅ |
| **MemoServ**: memos to nicks and to channels, number lists, IGNORE, CANCEL, CHECK, RSEND (read receipts), INFO, SENDALL, STAFF, notify by notice or mail | ✅ |
| **HostServ**: ON, OFF, GROUP, REQUEST; opers SET, SETALL, DEL, DELALL, LIST, WAITING, ACTIVATE, REJECT | ✅ |
| **BotServ**: channel bots, ASSIGN, SAY, ACT, GREET, 10 kickers, BADWORDS, fantasy commands (`!op`, `!kick`, `!mute` ...) | ✅ |
| **OperServ**: STATS, GLOBAL, AKILL, ZLINE, SNLINE, SHUN, SQLINE, EXCEPTION, KILL, MODE, KICK, FORBID, SESSION, NEWS, IGNORE, USERLIST, CHANLIST, SEEN, CHANKILL, SVSNICK, SVSJOIN, SVSPART | ✅ |
| **AdminServ**: server settings, MOTD, ports, opers and classes from IRC, with HISTORY, DIFF and UNDO | ✅ |
| Mail (SMTP with STARTTLS or TLS on 465, or a sendmail program) with Anope's templates; easy setup in the Hive Panel (Gmail, Outlook, Zoho presets, test mail with plain-words errors, SPF / DKIM / DMARC check) and `beeircd mail-test` | ✅ |
| Services log channel, lines exactly as Anope writes them | ✅ |
| Services in your language: NickServ `SET LANGUAGE` (English, Bahasa Melayu), a network default (`default_language`); the oper tools stay English | ✅ |
| **HelpServ**: help on any command, channel mode, user mode or services command (`/msg HelpServ chmodes`), your own answers (TOPIC), questions in words go to helpers with user mode +h (LIST, TAKE, CLOSE, CANCEL), a helper list for people who aren't opers (HELPER) | ✅ |

## Opers and bans

| Feature | | Notes |
| --- | --- | --- |
| `/oper <nick> <password>` with a separate oper password per oper | ✅ | identifying to NickServ never opers you |
| Oper by TLS client certificate, with or without a password | ✅ | like UnrealIRCd's certfp oper blocks |
| Oper flags `a b c g h k s` and oper classes | ✅ | built-in helper, globop, admin, netadmin; oper vhost and swhois |
| `/KILL`, `/WALLOPS`, server notices | ✅ | |
| One network ban list: AKILL / G-line, ZLINE, Q-line, R-line (realname), SHUN, ELINE exemptions, all with expiry | ✅ | matches the real host and IP, never the cloak |
| Channel extbans `~a ~r ~m ~t`, `~f:#chan:mask` forward, timed bans | ✅ | UnrealIRCd's named forms (`~account:`, `~forward:`...) are read too |
| Channel mode `+O` (opers only) | ✅ | |
| Spam filter (`/SPAMFILTER`, `STATS F`, UnrealIRCd syntax) | ✅ | shared with BeeIRCd and UnrealIRCd links; page in the Hive Panel |

## Running it

| Feature | | Notes |
| --- | --- | --- |
| One-command install (`beeircd install`) | ✅ | Debian 12/13, Ubuntu 22.04/24.04; hardened systemd service, daily backups, firewall opened when active |
| `beeircd status`, `check`, `backup`, `rehash`, `start`, `stop`, `restart` | ✅ | over a local socket, no network port |
| Signed updates (`beeircd update`) | ✅ | checks the signature, backs up, rolls back by itself if the new version doesn't start; `--rollback` goes back |
| Docker image (amd64, arm64) | ✅ | `ghcr.io/fhazal/beeircd` |
| Moving from Anope 2.0 (`beeircd import-anope`) | ✅ | accounts, passwords (no resets), channels, bots, bans and the rest of anope.db |
| UnrealIRCd oper blocks (`import-anope -unreal-opers`) | ✅ | plain and argon2id passwords kept, certificate opers too; other hashes are named in the report |
| `beeircd uninstall` | ✅ | keeps your data unless `--purge` |

## Hive Panel (web admin)

| Feature | | Notes |
| --- | --- | --- |
| Built in (`beeircd web`), answers only on this machine | ✅ | reach it over an SSH tunnel or an HTTPS proxy; strict CSP, no outside requests |
| Opers sign in with their oper nick and password | ✅ | every request is checked by the server against the oper's flags |
| Overview: network status, Spotlight, server details, health, What happened | ✅ | blocks can be shown, hidden and arranged |
| People, Channels, Bans, Opers, Services, Settings, Backups | ✅ | changes go through AdminServ and can be undone |
| Linked servers with a network map | ✅ | places from `beeircd.conf`, or ipinfo.io asked once for the server's own IP (`[server] find_place_by_ip = false`: its time zone) |
| Themes: Bee, Catppuccin Latte and Mocha, One Dark Pro, Dracula | ✅ | |
| Jump to (Ctrl K / ⌘K) | ✅ | |
| Members: registered nicks sign in to their own pages (Home, Chat, My nick, My channels, Memos) | ✅ | off unless you turn it on; needs HTTPS from outside |
| Redesigned "What happened" page (grouped by day, repeats folded) | ✅ | |

## BeeRoom (web chat)

| Feature | | Notes |
| --- | --- | --- |
| Web chat built on [The Lounge](https://github.com/thelounge/thelounge) (MIT), Bee and Catppuccin Mocha themes | ✅ | [fhazal/BeeRoom](https://github.com/fhazal/BeeRoom) |
| Each person's real IP reaches BeeIRCd over WEBIRC | ✅ | `beeircd webirc add` |
| Signed installer (`beeroom.sh`) for Apache, nginx, Caddy or none; Docker image | ✅ | |
| Inside the Hive Panel's Chat tab, signed in as the member | ✅ | |
