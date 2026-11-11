# BeeIRCd features and compatibility

Status words: **Implemented** (built and tested), **Partial**, **Planned**
(in the roadmap), **Experimental** (behind a switch, off by default),
**Proposed** (idea, not planned yet), **Not supported**.

Update this file in the same commit as the code that changes a status. A
capability is advertised to clients only when it is Implemented.

Last updated: 11 Nov 2026 (Stage 1.5.2).

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
| account-extban | Planned (ban engine) |
| STS | Planned (step 7) |
| WebSocket, WebIRC | Planned (Stage 4) |
| netsplit/netjoin batches | Planned (Stage 2) |
| draft/chathistory, draft/account-registration, draft/multiline, draft/read-marker, draft/message-redaction | Planned (Stage 4, behind a switch while draft) |
| draft/metadata, draft/channel-rename, draft/pre-away, extended-isupport, network-icon, oper-tag | Proposed |
| STARTTLS | Not supported (deprecated) |

## Services

| Service | Status |
| --- | --- |
| NickServ (register, identify, SASL, ghost, recover, group, access, set, info, drop, list) | Implemented |
| ChanServ (levels Founder/QOP/SOP/AOP/HOP/VOP/UOP, AKICK, op tools incl. OWNER/PROTECT/HALFOP/UP/DOWN, SET options) | Implemented |
| MemoServ (nick and channel memos, ignore) | Implemented |
| OperServ (stats, global, AKILL, kill, mode, kick, nick and chan tools, forbids, sessions, news) | Implemented |
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
| Oper classes | Planned | phase 4 |
| AKILL (user@host wildcards) | Implemented | |
| CIDR, K/G/Z/Q-line, SHUN, extbans | Planned | ban engine, phase 4 |
| DNSBL, content filter | Planned | |
