# BeeIRCd features and compatibility

Status words: **Implemented** (built and tested), **Partial**, **Planned**
(in the roadmap), **Experimental** (behind a switch, off by default),
**Proposed** (idea, not planned yet), **Not supported**.

Update this file in the same commit as the code that changes a status. A
capability is advertised to clients only when it is Implemented.

Last updated: 11 Nov 2026 (Stage 1.5.3).

## Core server

| Feature | Status | Notes |
| --- | --- | --- |
| Registration, PING, QUIT, NICK, USER, PASS | Implemented | PASS ignored (no server password) |
| JOIN, PART, KICK, INVITE, TOPIC, NAMES, LIST | Implemented | |
| PRIVMSG, NOTICE, TAGMSG, STATUSMSG (@#, +#) | Implemented | |
| WHO, WHOIS, WHOWAS, ISON, USERHOST, AWAY | Implemented | |
| MOTD, LUSERS, VERSION, TIME | Implemented | |
| User modes i w o s r x | Implemented | |
| Channel modes o v b e I k l i m n p s t R M r | Implemented | |
| Prefix option `@+` (default) or `~&@%+` | Implemented | `[server] prefixes`; +q +a +h with UnrealIRCd rules; restart to change |
| TLS 1.2+ listeners, self-signed or own certificate | Implemented | |
| Let's Encrypt built in, STS | Planned | step 7 |
| Host cloaking (HMAC) | Implemented | |
| Flood control (fake lag) | Implemented | |
| Session limit per IP, max users | Implemented | |
| Event layer (every network change is one event) | Implemented | 1.5.2; local delivery uses it; server links will (Stage 2) |
| Services data in memory, write-through SQLite | Implemented | 1.5.2; one BeeIRCd per database (locked) |
| Loop timing in OperServ STATS | Implemented | 1.5.2 |
| 5,000-client load test | Implemented | 1.5.2; `BEEIRCD_LOAD=1`, gate 99% of loop work < 5 ms |
| Server linking | Planned | Stage 2 |
| WebSocket | Planned | Stage 4 |
| PROXY protocol | Planned | |
| DNS blocklists (`[[dnsbl]]`: DroneBL, EFnetRBL, any list) | Implemented | 1.5.5b: answers per list, reject (default) / zline / akill / shun for ban_time, IPv4 + IPv6, lookups off the loop, registration waits at most 2 s, cache (hit 1 h, clean 10 min), LAN IPs never looked up, ELINE + session exceptions skip it, AdminServ DNSBL LIST/CHECK/ENABLE/DISABLE, hits in OS STATS ALL, Anope M_DNSBL log line |
| REHASH | Planned | step 7 |
| Module system | Proposed | after Stage 4 |

## IRCv3

| Spec | Status |
| --- | --- |
| CAP 302, cap-notify | Implemented |
| message-tags, message-ids, server-time | Implemented |
| sasl (PLAIN) | Partial (EXTERNAL, SCRAM planned) |
| account-notify, account-tag, extended-join, away-notify | Implemented |
| batch, labeled-response, echo-message | Implemented |
| chghost, invite-notify, multi-prefix, userhost-in-names | Implemented |
| client tags typing, reply, react, channel-context | Implemented (relayed) |
| standard-replies, MONITOR, extended-monitor, WHOX, setname | Planned (phase 3) |
| bot mode, UTF8ONLY, no-implicit-names | Planned (phase 3) |
| account-extban | Implemented (1.5.5a, EXTBAN=~,amqrt) |
| STS | Planned (step 7) |
| WebSocket, WebIRC | Planned (Stage 4) |
| netsplit/netjoin batches | Planned (Stage 2) |
| draft/chathistory, draft/account-registration, draft/multiline, draft/read-marker, draft/message-redaction | Planned (Stage 4, behind a switch while draft) |
| draft/metadata, draft/channel-rename, draft/pre-away, extended-isupport, network-icon, oper-tag | Proposed |
| STARTTLS | Not supported (deprecated) |

## Services

| Service | Status |
| --- | --- |
| Services in their own package, per-user records by UID, hooks from the event layer | Implemented (1.5.3) |
| Commands and syntax follow Anope 2.1 (see ANOPE-ALIGNMENT.md), hidden aliases for old names | Implemented (1.5.3) |
| HELP in Anope's layout: groups, HELP ALL, commands you can't use hidden | Implemented (1.5.3) |
| NickServ (register, identify, SASL, recover, group, ungroup, alist, set, saset, info, drop, list), as Anope 2.0.20: ACCESS host list with SET SECURE (recognised connections), STATUS 0-3, SET KILL ON/QUICK/IMMED/OFF, SET HIDE EMAIL/STATUS/USERMASK/QUIT, DROP one nick at a time, UNGROUP makes a nick its own account | Implemented (2.0.20 shape 11 Nov 2026) |
| NickServ CERT and SASL EXTERNAL (auto-identify by certificate) | Planned (1.5.6) |
| ChanServ access levels as permission sets (Anope numbers 1-10000, xOP names UOP 1/VOP 3/HOP 4/AOP 5/SOP 10/QOP 10000, LEVELS with Anope's 32 privileges and defaults, ACCESS ADD/DEL/LIST/VIEW/CLEAR, STATUS/WHY) | Implemented (1.5.4) |
| ChanServ (xOP lists with notes and number lists, AKICK with VIEW/ENFORCE/CLEAR, op tools incl. OWNER/PROTECT/HALFOP/UP/DOWN/SYNC, KICK/BAN/UNBAN/INVITE/GETKEY) | Implemented |
| ChanServ MODE LOCK ADD/DEL/SET/LIST, MODE SET, MODE CLEAR, TOPIC SET/APPEND/PREPEND/LOCK/UNLOCK, ENTRYMSG (5 lines), ENFORCE, LOG (MESSAGE/NOTICE/MEMO, by status), SUSPEND/UNSUSPEND (+expiry) | Implemented (1.5.4) |
| ChanServ SET: DESCRIPTION FOUNDER SUCCESSOR SECURE SECUREFOUNDER KEEPTOPIC KEEPMODES PEACE RESTRICTED SECUREOPS SIGNKICK AUTOOP PERSIST PRIVATE BANTYPE URL NOEXPIRE; CR shortcuts MEMOLEVEL AUTOVOP LEAVEOPS; DROP #chan #chan; UNBAN with no channel (Anope 2.0.20) | Implemented (1.5.4, 2.0.20 shape 11 Nov 2026) |
| ChanServ SET OVERFLOW (send people on when full) | Planned (1.5.9, needs +L) |
| ChanServ FLAGS, CLONE, SEEN; BotServ (ASSIGN, SAY, BADWORDS) | Planned |
| MemoServ (nick memos, number lists, ignore) | Implemented |
| MemoServ channel memo boxes (one box per channel like Anope, MEMO privilege, own limit and ignore list, notice on join and identify) | Implemented (1.5.4) |
| OperServ (STATS AKILL/HASH/UPLINK/UPTIME/ALL/RESET, global, AKILL ZLINE SNLINE SHUN SQLINE with expiry (30 days by default, `ban_expiry`; +0 for good) and number lists, SNLINE mask:reason, EXCEPTION BAN (ELINE), kill, MODE incl. CLEAR [ALL], kick, FORBID nick/chan/email/register with expiry, sessions, exceptions with expiry and CIDR, logon news) | Implemented (ban engine 1.5.5a, 2.0.20 shape 11 Nov 2026) |
| OperServ CHANKILL SVSNICK SVSJOIN SVSPART UMODE USERLIST CHANLIST INFO IGNORE OPERNEWS RANDOMNEWS SEEN, ChanServ SEEN (Anope 2.0.20) | Implemented (1.5.5b) |
| AdminServ (settings saved to config, MOTD, ports, opers, restart, die) | Implemented |
| HostServ (vhosts) | Planned (Stage 1.5) |
| HelpServ | Planned (phase 6) |
| Switch off built-in services, link Anope | Planned (after Stage 4) |

## Opers and bans

| Feature | Status | Notes |
| --- | --- | --- |
| Oper flags a c g h k s | Implemented | |
| /OPER with a separate oper password per oper | Implemented | identifying never opers; admin password hash in beeircd.conf |
| /KILL, /WALLOPS, server notices (+s) | Implemented | |
| Services log channel (`log_channel`, AdminServ `log-channel` / `log-connects`): every bot sits there with op; lines exactly as Anope 2.0.20 (COMMAND/ADMIN/OVERRIDE, USERS connect/disconnect/nick on by default, access denied, expiry), from the service they belong to | Implemented | 1.5.5a-log, Anope-exact 11 Nov 2026 |
| Channel mode +O (opers only; only opers set it, 520 for others) | Implemented | 1.5.5a-log; UnrealIRCd/InspIRCd; the log channel keeps it set |
| Oper classes | Planned | phase 4 |
| Network ban engine: AKILL/G-line (user@host, CIDR), ZLINE (IP/CIDR, before DNS), SQLINE/Q-line (nicks), SNLINE/R-line (realname glob or regex), SHUN, ELINE exemptions, all with expiry | Implemented | 1.5.5a; one list behind OperServ and /GLINE /ZLINE /QLINE /RLINE /SHUN /ELINE; matches real host and IP, never the cloak |
| Channel extbans ~a ~r ~m (~q) ~t, timed bans, CS BAN +expiry, AKICK extbans | Implemented | 1.5.5a |
| K-line (this server only) | Planned | with linking (Stage 2) |
| Content filter (spamfilter) | Planned | |
