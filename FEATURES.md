# BeeIRCd features and compatibility

Status words: **Implemented** (built and tested), **Partial**, **Planned**
(in the roadmap), **Experimental** (behind a switch, off by default),
**Proposed** (idea, not planned yet), **Not supported**.

Update this file in the same commit as the code that changes a status. A
capability is advertised to clients only when it is Implemented.

Last updated: 12 Nov 2026 (Stage 1.5.9b).

## Core server

| Feature | Status | Notes |
| --- | --- | --- |
| Registration, PING, QUIT, NICK, USER, PASS | Implemented | PASS ignored (no server password) |
| JOIN, PART, KICK, INVITE, TOPIC, NAMES, LIST | Implemented | KICK takes `#a,#b nick1,nick2` pairs (RFC 2812) |
| PRIVMSG, NOTICE, TAGMSG, STATUSMSG (@#, +#) | Implemented | |
| WHO, WHOIS, WHOWAS, ISON, USERHOST, AWAY | Implemented | `WHO exactnick` shows +i users; `WHOWAS nick count`; `WHO *` also lists the built-in services (they are on the server, as with Anope) |
| MOTD, LUSERS, VERSION, TIME, INFO | Implemented | INFO 371/374 (conformance step) |
| Input limits | Implemented | lines over 510 bytes or tag sections over 4096 bytes get 417 and are dropped, not cut (IRCv3 message-tags; conformance step) |
| User modes i w o s r x | Implemented | |
| User modes B d D R T p Z (anyone), H W (opers), q (opers, flag a), z (TLS, set by the server) | Implemented | 1.5.9b; UnrealIRCd letters and rules: +B WHOIS 335 and B in WHO, 005 BOT=B; +d no channel messages; +D/+R/+T/+Z refuse PMs with 531 (opers get past +D +R +T); +p hides channels in WHOIS; +H hides oper status from non-opers; +W shows who WHOISes you; +q unkickable (972); -o drops H W q; HELP UMODES |
| KNOCK | Implemented | 1.5.9b; +i, +k or full +l channels; refused on +K, when banned, or after 3 knocks a minute; ops get Unreal's `[Knock] by ...` notice; counts toward +f k |
| SILENCE | Implemented | 1.5.9b; 005 SILENCE=32, UnrealIRCd 271/272/511; drops PMs, notices and invites quietly |
| Channel history +H `lines:time` (like `+H 50:1d`), playback on join, CHATHISTORY | Implemented | 1.5.9c; UnrealIRCd syntax; `[history]` default (+H on every new channel), playback_lines (15, 0 = off) / playback_time (1d) for clients without chathistory, caps 200 lines (5000 on registered channels) and 31d, max_memory_mb 32 (oldest lines go first); with `secret` set it is saved encrypted (AES-256-GCM) to history.db every 5 minutes and at shutdown, so a restart keeps it; history goes with -H or when the channel empties (not +P); @#chan messages and TAGMSG are not kept |
| SETNAME, CHGNAME | Implemented | 1.5.9b; IRCv3 `setname` cap, FAIL SETNAME INVALID_REALNAME over 50; CHGNAME for opers with flag h |
| SETHOST, SETIDENT, CHGHOST, CHGIDENT | Implemented | 1.5.9b; opers with flag h, UnrealIRCd character rules; chghost cap and 396 |
| SAJOIN, SAPART, SANICK, SAMODE, SATOPIC | Implemented | 1.5.9b; opers with flag a; server notices like UnrealIRCd/InspIRCd (not in the Anope log channel: Anope never sees these) |
| Channel modes o v b e I k l i m n p s t R M r | Implemented | |
| Channel modes c S C T N K V Q z Z D P O | Implemented | 1.5.9a; UnrealIRCd letters and rules; +K blocks KNOCK (1.5.9b) |
| Flood guard +f `[10j#R10,40m#M10,...]:15` and +F profiles | Implemented | 1.5.9a; UnrealIRCd syntax, types c j k m n t r; server default profile `[channels] flood_profile` (normal), AS SET flood-profile; no p (paste) type |
| +L link channel, +E repeat, +B caps, +J rejoin delay | Implemented | 1.5.9a; +L on +l +i +k +b +R +z like UnrealIRCd (3 hops at most); +E +B +J from InspIRCd |
| ~f:#chan forward extban | Proposed | not in 1.5.9a |
| +G censor (server word list) | Planned | later (D4) |
| HELP and HELPOP (every command + CHMODES) | Implemented | 704/705/706, 524 for an unknown topic (conformance step; was UnrealIRCd's 292 in 1.5.9a) |
| Prefix option `@+` (default) or `~&@%+` | Implemented | `[server] prefixes`; +q +a +h with UnrealIRCd rules; restart to change |
| TLS 1.2+ listeners, self-signed or own certificate | Implemented | |
| Let's Encrypt built in, STS | Implemented | 7c; `[tls] acme = true` (+ `acme_email`, `acme_domains` when clients use another name than the server name, `acme_directory` for staging/other CAs, `acme_http` when a tunnel forwards port 80 elsewhere); HTTP-01, port 80 open only while asking; TLS ports start at once on a self-signed stand-in, the real certificate is loaded live without a restart; checked twice a day, renewed in the last third of its life (30 days of 90); a failure keeps the current certificate, tells opers and retries after 1 h / 3 h / 12 h; status/check show source, renew date and failures; config refuses private names (.local, IPs) for ACME. STS: see the IRCv3 table |
| Certificate reload without a restart | Implemented | 7b; a changed cert/key file is picked up within a minute (and on REHASH) for new connections; a broken file keeps the old certificate and tells opers |
| Host cloaking (HMAC) | Implemented | |
| Flood control (fake lag) | Implemented | opers are not slowed down (7a) |
| Connect throttle and caps on connections still logging in | Implemented | 7a; per IP `connect_rate` 5:60 (refused with "Throttled", a 10-minute ZLINE after 3 refusals in 10 minutes, posted by OperServ in the log channel as THROTTLE), `max_unregistered_per_ip` 3, `max_unregistered` 500; before any DNS/DNSBL work; LAN, ELINE and session exceptions skip them; AdminServ SET connect-rate / max-unregistered / max-unregistered-per-ip |
| Session limit per IP, max users | Implemented | |
| Event layer (every network change is one event) | Implemented | 1.5.2; local delivery uses it; server links will (Stage 2) |
| Services data in memory, write-through SQLite | Implemented | 1.5.2; one BeeIRCd per database (locked) |
| Loop timing in OperServ STATS | Implemented | 1.5.2 |
| 5,000-client load test | Implemented | 1.5.2; `BEEIRCD_LOAD=1`, gate 99% of loop work < 5 ms |
| Server linking | In progress (Stage 2, v1.1) | 2a built (branch stage2): TLS link port + autoconnect with backoff, own link certificate (never renewed) pinned by fingerprint (= UnrealIRCd spkifp), optional password, UnrealIRCd 6 handshake (tested both directions with a real UnrealIRCd 6.2.7, replay test from its recorded lines), server tree (SID/SQUIT/EOS, loops refused), PING/lag, sendq limit, notices for every link event, `beeircd link-info`, Links row in `status`, link lines in `check`, link cert in backups. 2b: burst both ways (UID, SJOIN with ranks/lists and setters, TOPIC, AWAY), nick collisions (rename between BeeIRCd, UnrealIRCd kill rule otherwise), channel TS merge, IRCv3 netjoin batch, split quits users ("near far"), WHOIS shows the remote server; tested against a real UnrealIRCd 6.2.7. 2c: live changes both ways (nick, join, part, quit, kick, modes, user modes, topic, away, setname, hosts, account login, invite, services nick changes), messages and numerics routed to the right server, KILL both ways, three-server trees, network bans as TKL (AKILL/ZLINE/SHUN/ELINE, burst + live), casemapping setting (ascii default) checked at link time, UnrealIRCd-compatible cloaks (`[cloak] method = "unreal"` for cloak_sha256, `"cloak_inspircd"` for that module), one services server per network (`[services] enabled`, leaves use the hub's bots); tested against a real UnrealIRCd 6.2.7. 2d: services on their own pseudo-server (`services.<domain>`), SASL relayed over links in UnrealIRCd's format (to BeeIRCd services or Anope), SVSJOIN/SVSPART both ways (AJOIN for people elsewhere), SVSKILL, oper class + flags over links (OperServ/AdminServ rights for remote opers; UnrealIRCd classes mapped), remote WHOIS (`WHOIS nick nick`, idle time), SQLINE network-wide (Q-lines), SNLINE between BeeIRCd servers, OperServ JUPE / NOOP / DEFCON / SERVER and a real STATS UPLINK, AdminServ `SERVER <name> STATUS|<command>` with `[link] remote_admin`; tested against a real UnrealIRCd 6.2.7 (SASL, AJOIN, Q-lines, jupes, remote opers). 2e: IRCv3 netsplit batch, `[link] hide_split`, rejoin settles (tested: same state on both sides after changes during a split), newest network ban wins per mask, /LINKS /MAP /CONNECT /SQUIT /STATS l c k u, /ADMIN, remote /MOTD /VERSION /ADMIN /TIME /STATS, K-lines (this server only); tested against a real UnrealIRCd 6.2.7 (remote queries both ways, SQUIT by an UnrealIRCd oper, relink by itself). 2f: the installer asks a leaf for its hub (name, address, fingerprint, cloak secret, not shown), writes the link block, turns a leaf's services off, makes the link certificate at install and prints the block the other server pastes (also `beeircd link-info`, now per role); /oper on a leaf for every oper (asked of the services server over the link: entry, host, password with lockout; `BEEOPER`, password masked in link debug logs; entry or class changes on the services server reach the leaf at once, 2g); ADMIN.md "Linking servers"; 3-server test network on the homelab (`deploy/testnet`, livecheck `net` suite 30/30) and a two-machine install test (`scripts/linkmachines.sh`). Next: 2g gates. PLAN-2.md |
| WebSocket | Planned | Stage 4 |
| PROXY protocol | Implemented | 7a; v1 and v2 on `[[listen]]` ports with `proxy = true`, only from `proxy_from` IPs/CIDRs (anyone else, plain IRC or a bad header: closed without a word); LOCAL/UNKNOWN keep the proxy address; plain and TLS ports |
| DNS blocklists (`[[dnsbl]]`: DroneBL, EFnetRBL, any list) | Implemented | 1.5.5b: answers per list, reject (default) / zline / akill / shun for ban_time, IPv4 + IPv6, lookups off the loop, registration waits at most 2 s, cache (hit 1 h, clean 10 min), LAN IPs never looked up, ELINE + session exceptions skip it, AdminServ DNSBL LIST/CHECK/ENABLE/DISABLE, hits in OS STATS ALL, Anope M_DNSBL log line |
| REHASH | Implemented | 7b; `/rehash` (opers, flag a, 382), `beeircd rehash` and SIGHUP re-read beeircd.conf; applies what is safe live (limits, MOTD, DNSBL, flood profiles, history caps, oper classes, services settings, certificate) and lists what waits for a restart (ports, server name/SID/network/prefixes, cloak, TLS paths, database, history file/secret); a file that fails the checks is refused |
| `beeircd install` / `uninstall` | Implemented | 7d; Debian 12/13, Ubuntu 22.04/24.04 with systemd (others: warned, may continue); 9 questions (server name, network, admin nick, admin oper password typed twice and hashed at once, where the admin may /oper from (defaults to your SSH IP), ports, certificate: Let's Encrypt / self-signed / own files, prefix style, role standalone/hub/leaf + link port; a leaf also gives its hub's name, address, fingerprint and cloak secret, and gets the link block written and its services off; a hub or leaf prints the block the other server pastes), summary, nothing written until confirmed; system user `beeircd`, /opt/beeircd, /etc/beeircd (conf 0640 with every block explained), /var/lib/beeircd, socket in /run/beeircd; random cloak + history secrets; hardened systemd unit (ProtectSystem=strict, writes only /etc/beeircd + /var/lib/beeircd, CAP_NET_BIND_SERVICE only for ports under 1024 or Let's Encrypt); daily backup timer 04:00; ufw/firewalld opened when active; certbot files copied + a renewal hook; `uninstall` keeps data unless `--purge`; `scripts/install.sh` downloads the right binary (amd64/arm64) and runs it. Clean-machine test: `scripts/cleanmachine.sh` (LXD, no network) |
| `beeircd update` and signed releases | Implemented | 7e; releases are signed with an ed25519 key that stays on the maintainer's PC (passphrase-protected, `tools/release`); the public key is built in, so a hacked download site or GitHub account can't ship an accepted update. `update` shows what's new and who gets disconnected, then: signature, download + checksum, the NEW program checks (and upgrades a copy of) your config, backup, swap (old kept as `.old`), restart with the quit message "Server updating to BeeIRCd X", waits for the new version to answer and **rolls back by itself** after 30 s; `--check` only looks, `--rc` test versions, `--yes` no questions, `--rollback` goes back. The server checks once a day (`[update] check`, on by default, nothing about the server sent): an Update row in `beeircd status` + one oper notice. GitHub Actions builds tagged releases as drafts; signing + publishing happen on the PC (RELEASING.md). Tested for real in LXD: fake-signed release refused, update, broken release rolled back, manual rollback (`scripts/updatetest.sh`) |
| `beeircd status`, `check`, `backup`, `rehash`, `start`/`stop`/`restart` | Implemented | 7b; talk to the running server over a unix control socket (`[control] socket`, mode 0660, no network port); status shows load, data, certificate, last backup and a Safety row only when the guards acted today; check tests config, certificate, database (read-only when stopped), history secret, free ports; restart refuses when check fails; backup = one .tar.gz (config, live copy of services.db, history, certificate; 0600) in `[backup] dir`, 7 dated kept; start/stop/restart via systemd |
| Module system | Proposed | after Stage 4 |

## IRCv3

| Spec | Status |
| --- | --- |
| CAP 302, cap-notify | Implemented |
| message-tags, message-ids, server-time | Implemented |
| sasl (PLAIN, EXTERNAL) | Implemented (EXTERNAL 1.5.6a; SCRAM planned) |
| account-notify, account-tag, extended-join, away-notify | Implemented |
| batch, labeled-response, echo-message | Implemented |
| chghost, invite-notify, multi-prefix, userhost-in-names | Implemented |
| client tags typing, reply, react, channel-context | Implemented (relayed) |
| MONITOR (MONITOR=100, numerics 730-734) | Implemented (1.5.6a) |
| setname | Implemented (1.5.9b) |
| standard-replies, extended-monitor, WHOX | Planned (phase 3) |
| bot mode, UTF8ONLY, no-implicit-names | Planned (phase 3) |
| account-extban | Implemented (1.5.5a, EXTBAN=~,amqrt; ACCOUNTEXTBAN=a token since the conformance step) |
| STS | Implemented (7c): `sts=port=<tls port>,duration=2592000` on plain and `sts=duration=2592000` on TLS, to CAP 302 clients, only with a CA certificate (never self-signed or the stand-in); `[tls] sts = false` turns it off, `sts_port` when a tunnel shows another port; REQ sts is NAKed |
| WebSocket, WebIRC | Planned (Stage 4) |
| netsplit/netjoin batches | Implemented (Stage 2b/2e, branch stage2) |
| draft/chathistory | Implemented (1.5.9c) for +H channels you are in: LATEST BEFORE AFTER AROUND BETWEEN TARGETS, msgid and timestamp references, 005 CHATHISTORY=100 MSGREFTYPES=msgid,timestamp; no private-message history |
| draft/account-registration, draft/multiline, draft/read-marker, draft/message-redaction | Planned (Stage 4, behind a switch while draft) |
| draft/metadata, draft/channel-rename, draft/pre-away, extended-isupport, network-icon, oper-tag | Proposed |
| STARTTLS | Not supported (deprecated) |

## Services

| Service | Status |
| --- | --- |
| Services in their own package, per-user records by UID, hooks from the event layer | Implemented (1.5.3) |
| Commands and syntax follow Anope 2.1 (see ANOPE-ALIGNMENT.md), hidden aliases for old names | Implemented (1.5.3) |
| HELP in Anope's layout: groups, HELP ALL, commands you can't use hidden | Implemented (1.5.3) |
| NickServ (register, identify, SASL, recover, group, ungroup, alist, set, saset, info, drop, list), as Anope 2.0.20: ACCESS host list with SET SECURE (recognised connections), STATUS 0-3, SET KILL ON/QUICK/IMMED/OFF, SET HIDE EMAIL/STATUS/USERMASK/QUIT, DROP one nick at a time, UNGROUP makes a nick its own account | Implemented (2.0.20 shape 11 Nov 2026) |
| NickServ CERT and SASL EXTERNAL (auto-identify by TLS client certificate, WHOIS 276) | Implemented (1.5.6a) |
| NickServ AJOIN, GLIST, SUSPEND/UNSUSPEND (+expiry), LIST for everyone (PRIVATE, SUSPENDED, NOEXPIRE), GETEMAIL, UPDATE, SET AUTOOP/DISPLAY/KEEPMODES/PRIVATE/URL, SASET NOEXPIRE, max_logins, reg_delay | Implemented (1.5.6a, Anope 2.0.20) |
| MemoServ CANCEL/CHECK/INFO/SENDALL/STAFF, SET NOTIFY ON/LOGON/NEW/MAIL/NOMAIL/OFF, send delay 3 s, 20 memos | Implemented (1.5.6b, Anope 2.0.20) |
| Mail ([mail]: SMTP with STARTTLS or a sendmail program, Anope's templates): NickServ RESETPASS + CONFIRM, MemoServ NOTIFY MAIL; REGISTER needs an email (force_email) | Implemented (1.5.6b) |
| NickServ BUDDY (friend list with online/offline notices, ConferenceRoom) | Implemented (1.5.6b) |
| ChanServ access levels as permission sets (Anope numbers 1-10000, xOP names UOP 1/VOP 3/HOP 4/AOP 5/SOP 10/QOP 10000, LEVELS with Anope's 32 privileges and defaults, ACCESS ADD/DEL/LIST/VIEW/CLEAR, STATUS/WHY) | Implemented (1.5.4) |
| ChanServ (xOP lists with notes and number lists, AKICK with VIEW/ENFORCE/CLEAR, op tools incl. OWNER/PROTECT/HALFOP/UP/DOWN/SYNC, KICK/BAN/UNBAN/INVITE/GETKEY) | Implemented |
| ChanServ MODE LOCK ADD/DEL/SET/LIST, MODE SET, MODE CLEAR, TOPIC SET/APPEND/PREPEND/LOCK/UNLOCK, ENTRYMSG (5 lines), ENFORCE, LOG (MESSAGE/NOTICE/MEMO, by status), SUSPEND/UNSUSPEND (+expiry) | Implemented (1.5.4) |
| ChanServ SET: DESCRIPTION FOUNDER SUCCESSOR SECURE SECUREFOUNDER KEEPTOPIC KEEPMODES PEACE RESTRICTED SECUREOPS SIGNKICK AUTOOP PERSIST PRIVATE BANTYPE URL NOEXPIRE; CR shortcuts MEMOLEVEL AUTOVOP LEAVEOPS; DROP #chan #chan; UNBAN with no channel (Anope 2.0.20) | Implemented (1.5.4, 2.0.20 shape 11 Nov 2026) |
| ChanServ SET OVERFLOW (send people on when full) | Implemented | 1.5.9a; locks +L in the mode lock |
| ChanServ FLAGS, CLONE, SEEN; BotServ (ASSIGN, SAY, BADWORDS) | Planned |
| MemoServ (nick memos, number lists, ignore) | Implemented |
| MemoServ channel memo boxes (one box per channel like Anope, MEMO privilege, own limit and ignore list, notice on join and identify) | Implemented (1.5.4) |
| OperServ (STATS AKILL/HASH/UPLINK/UPTIME/ALL/RESET, global, AKILL ZLINE SNLINE SHUN SQLINE with expiry (30 days by default, `ban_expiry`; +0 for good) and number lists, SNLINE mask:reason, EXCEPTION BAN (ELINE), kill, MODE incl. CLEAR [ALL], kick, FORBID nick/chan/email/register with expiry, sessions, exceptions with expiry and CIDR, logon news) | Implemented (ban engine 1.5.5a, 2.0.20 shape 11 Nov 2026) |
| OperServ CHANKILL SVSNICK SVSJOIN SVSPART UMODE USERLIST CHANLIST INFO IGNORE OPERNEWS RANDOMNEWS SEEN, ChanServ SEEN (Anope 2.0.20) | Implemented (1.5.5b) |
| OperServ JUPE, NOOP, DEFCON, SERVER; STATS UPLINK | Implemented (Stage 2d, branch stage2) | DEFCON levels fixed (4 no registrations, 3 no memos, 2 no new connections, 1 opers only), Global notice on change |
| AdminServ (settings saved to config, MOTD, ports, opers, restart, die) | Implemented |
| HostServ as Anope 2.0.20: ON, OFF, GROUP, REQUEST; opers (flag h) SET, SETALL, DEL, DELALL, LIST, WAITING, ACTIVATE, REJECT; ident@host vhosts; vhost per nick, shown at identify; [hostserv] rules (vhost_chars, dots, lengths, activate_on_set, request memos) | Implemented (1.5.7) |
| HelpServ | Planned (phase 6) |
| Switch off built-in services, link Anope | In progress | 2c: `[services] enabled = false` (another BeeIRCd runs them); Anope as the services server: planned (after Stage 4) |

## Opers and bans

| Feature | Status | Notes |
| --- | --- | --- |
| Oper flags a c g h k s | Implemented | |
| /OPER with a separate oper password per oper | Implemented | identifying never opers; admin password hash in beeircd.conf |
| /KILL, /WALLOPS, server notices (+s) | Implemented | |
| Services log channel (`log_channel`, AdminServ `log-channel` / `log-connects`): every bot sits there with op; lines exactly as Anope 2.0.20 (COMMAND/ADMIN/OVERRIDE, USERS connect/disconnect/nick on by default, access denied, expiry), from the service they belong to | Implemented | 1.5.5a-log, Anope-exact 11 Nov 2026 |
| Channel mode +O (opers only; only opers set it, 520 for others) | Implemented | 1.5.5a-log; UnrealIRCd/InspIRCd; the log channel keeps it set |
| Oper classes ([[operclass]] + built-in helper/globop/admin/netadmin, AS CLASS), oper vhost and swhois at /oper, admin_hosts/admin_vhost, CIDR oper hosts, OPER DISABLE/ENABLE, AdminServ HISTORY/DIFF/UNDO, OS OPER (Anope names) | Implemented | 1.5.8 |
| Network ban engine: AKILL/G-line (user@host, CIDR), ZLINE (IP/CIDR, before DNS), SQLINE/Q-line (nicks), SNLINE/R-line (realname glob or regex), SHUN, ELINE exemptions, all with expiry | Implemented | 1.5.5a; one list behind OperServ and /GLINE /ZLINE /QLINE /RLINE /SHUN /ELINE; matches real host and IP, never the cloak |
| Channel extbans ~a ~r ~m (~q) ~t, timed bans, CS BAN +expiry, AKICK extbans | Implemented | 1.5.5a |
| K-line (this server only) | Implemented (Stage 2e, branch stage2) | /KLINE /UNKLINE, STATS k |
| Content filter (spamfilter) | Planned | |
