# BeeIRCD spec (draft 2, 11 Nov 2026)

BeeIRCD is one Go program: the IRC server, the five services, and later the Hive Panel (web admin) and BeeRoom (web chat). This spec covers **Stage 1**: one server with all services working, plus install and start. Everything is built so that Stage 2 (linking BeeIRCD to BeeIRCD) can be added without redoing it.

Mockups this spec follows:
- Walkthrough (install, start, BChat): https://claude.ai/artifact/XW22t7xnzRnV5yw8tDJviw
- Hive Panel: https://claude.ai/artifact/9aXWy4GSNFo9eEEPHucBTn

Items marked **DRAFT** still need your OK. Everything else follows what you've already decided.

---

## 1. What people see

### 1.1 Connecting
- Ports: 6667 plain, 6697 TLS (both can be changed in `./install`). Port 7000 is kept for server links and is TLS only. It isn't used until Stage 2.
- On connect: the welcome lines, then the MOTD, then NickServ asks you to identify if your nick is registered.
- **SASL PLAIN** works too, so clients like BChat can identify during connect, before anything shows.
- Hosts are cloaked by default, for example `Bee@beenet-3f9a1c.cloak`. Opers and the person themself see the real host.

### 1.2 Prefixes and modes
Only **@** and **+** exist in channels. There's no ~ & % and there never will be.

| User mode | Meaning |
|---|---|
| `+i` | invisible (on by default) |
| `+o` | IRC operator (set by services on identify, or by /oper) |
| `+r` | identified to NickServ (only services set it) |
| `+w` | sees wallops |
| `+x` | host cloaked (on by default) |
| `+s` | sees server notices (opers only) |

| Channel mode | Meaning |
|---|---|
| `+o nick` / `+v nick` | op / voice |
| `+b` `+e` `+I mask` | ban / ban exception / invite exception |
| `+n` `+t` | no outside messages / only ops change topic (both on by default) |
| `+m` `+i` `+s` `+p` | moderated / invite only / secret / private |
| `+k key` `+l limit` | key / user limit |
| `+r` | registered with ChanServ (only services set it) |
| `+R` | only identified people can join |
| `+M` | only identified people (or voiced) can talk |

### 1.3 IRCv3 (so modern clients work well)
`message-tags`, `server-time`, `sasl`, `account-tag`, `account-notify`, `away-notify`, `extended-join`, `cap-notify`, `multi-prefix`, `echo-message`, `invite-notify`, `chghost`, `batch`, `labeled-response`, `+typing` and `+draft/reply` client tags (BChat already uses those two).

### 1.4 Typing service commands
`/ns`, `/cs`, `/ms`, `/os`, `/as` work in **every** client because the server understands them itself (`NS`, `CS`, `MS`, `OS`, `AS` commands, plus `NICKSERV`, `CHANSERV` and so on). `/msg NickServ ...` also works.

---

## 2. Services: the ConferenceRoom flavour

How every service answers:
- As a **notice** from the service, for example `-NickServ- Password accepted. You are identified as Bee.`
- One short sentence, then the next step if there is one: `Identify within 60 seconds: /ns identify <password>`
- Lists are lined-up columns with a header row and a count at the end: `5 entries.`
- Errors say what's wrong and how to fix it: `#bchat isn't registered. Register it: /cs register #bchat <description>`
- `HELP` lists the commands, one line each. `HELP <command>` explains one command with an example.
- Commands aren't case sensitive. `/cs aop #bchat add ember` and `/cs AOP #bchat ADD ember` do the same thing.

### 2.1 NickServ: keeps a nick yours **DRAFT**

| Command | Who | What it does |
|---|---|---|
| `REGISTER <password> [email]` | anyone | Registers the nick you're using. Email is optional and only used for oper resets in Stage 1. |
| `IDENTIFY [nick] <password>` | anyone | Proves the nick is yours. Sets `+r`, and `+o` if you're an oper (see 2.6). |
| `LOGOUT` | identified | Stops being identified. |
| `GHOST <nick> <password>` | anyone | Disconnects an old connection that's using your nick. |
| `RECOVER <nick> <password>` | anyone | GHOST, then gives you the nick. |
| `GROUP <nick> <password>` | anyone | Adds the nick you're using to an account you already have. |
| `UNGROUP <nick>` | owner | Removes a nick from your account. |
| `ACCESS ADD\|DEL\|LIST [mask]` | owner | Hosts that identify you automatically (off unless you add one). |
| `SET PASSWORD <new>` | owner | Changes your password. |
| `SET EMAIL <email>\|OFF` | owner | |
| `SET ENFORCE ON\|OFF` | owner | Anyone who doesn't identify within 60 seconds gets renamed to Guest12345. On by default. |
| `SET HIDE EMAIL\|HOST ON\|OFF` | owner | What INFO shows to others. |
| `INFO <nick>` | anyone | Registered date, last seen, grouped nicks and options. Opers see more. |
| `DROP` | owner | Unregisters your nick (asks you to repeat with a code to confirm). |
| `LIST <pattern>` | opers | Searches registered nicks. |

- A nick expires after **90 days** without anyone identifying to it (can be changed). The founder's channels go to the successor, or are dropped if there's no successor.
- Passwords are stored as **argon2id** hashes and never in plain text.
- Stage 1 doesn't send email, so a forgotten password is reset by an oper (`/os nick setpass`).

### 2.2 ChanServ: keeps a channel yours **DRAFT**

Access levels, highest first:

| Level | Gets on join | Can also |
|---|---|---|
| **Founder** | @ | everything, including DROP and SET FOUNDER |
| **SOP** | @ | add and remove AOP, VOP, UOP and AKICK; most SET options |
| **AOP** | @ | op, voice, kick, ban, unban, topic, invite |
| **VOP** | + | voice and devoice themself |
| **UOP** | – | invite and unban themself, join when the channel is `+i` or `+k` |

| Command | Who | What it does |
|---|---|---|
| `REGISTER #chan <description>` | identified, opped in the channel | Registers the channel. You become Founder. |
| `DROP #chan` | Founder | Unregisters it (with a confirm code). |
| `INFO #chan` | anyone | Founder, successor, registered date, description, mode lock, topic lock. |
| `SOP\|AOP\|VOP\|UOP #chan ADD\|DEL\|LIST\|CLEAR [nick]` | see the table above | Edits a level's list. Adding a nick to a new level moves them out of the old one. |
| `ACCESS #chan LIST` | VOP+ | Every level in one list (as in the walkthrough). |
| `AKICK #chan ADD\|DEL\|LIST <mask> [reason]` | SOP+ | Kicks and bans that mask on join, every time. |
| `OP\|DEOP\|VOICE\|DEVOICE #chan [nick]` | AOP+ (VOP for their own voice) | |
| `KICK #chan <nick> [reason]` | AOP+ | |
| `BAN\|UNBAN #chan <nick\|mask>` | AOP+ (UOP can unban themself) | |
| `INVITE #chan` | UOP+ | Invites you. |
| `TOPIC #chan <text>` | AOP+ | |
| `CLEAR #chan BANS\|MODES\|OPS\|VOICES` | SOP+ | |
| `SET #chan DESC <text>` | SOP+ | |
| `SET #chan FOUNDER\|SUCCESSOR <nick>` | Founder | |
| `SET #chan MLOCK <modes>` | SOP+ | Locks modes, for example `+nt-s`. |
| `SET #chan KEEPTOPIC ON\|OFF` | SOP+ | Puts the topic back after the channel empties. On by default. |
| `SET #chan TOPICLOCK ON\|OFF` | SOP+ | Only AOP+ can change the topic, even through /topic. |
| `SET #chan SECURE ON\|OFF` | SOP+ | Only people on the access list may be opped. |
| `SET #chan PRIVATE ON\|OFF` | SOP+ | Hidden from LIST and INFO for outsiders. |
| `SET #chan ENTRYMSG <text>\|OFF` | SOP+ | A notice sent to everyone who joins. |
| `SET #chan URL <url>\|OFF` | SOP+ | |
| `LIST <pattern>` | anyone (opers see private ones) | |

- Access is by **account**, not by nick. Grouped nicks count, and you must be identified.
- A channel expires after **60 days** with no access-list member joining (can be changed).

### 2.3 MemoServ: messages for later **DRAFT**

| Command | Who | What it does |
|---|---|---|
| `SEND <nick\|#chan> <text>` | identified | Sends to a nick. Sending to a channel reaches its Founder and SOPs. |
| `LIST` | identified | Number, sender, date, new or read. |
| `READ <num\|NEW\|LAST>` | identified | |
| `DEL <num\|ALL\|READ>` | identified | |
| `SET NOTIFY ON\|OFF` | identified | Get a notice the moment a memo arrives, not only on identify. |
| `SET LIMIT <n>` | identified | Up to 50 (opers can set more). |
| `IGNORE ADD\|DEL\|LIST <nick>` | identified | Memos from that nick are silently dropped. |

### 2.4 OperServ: network-wide oper tools **DRAFT**

| Command | Flag | What it does |
|---|---|---|
| `STATS` | any oper | People, peak, channels, opers, servers, uptime. |
| `GLOBAL <text>` | g | A notice to everyone, from `-Global-`. |
| `AKILL ADD <mask> <time> <reason>` | k | A ban from the whole network (`7d`, `12h`, `0` = until removed). |
| `AKILL DEL <mask\|num>` / `AKILL LIST` | k | |
| `KILL <nick> <reason>` | k | |
| `MODE #chan <modes>` | s | Sets modes as services. |
| `KICK #chan <nick> <reason>` | s | |
| `NICK SETPASS <nick> <password>` | s | Password reset. |
| `NICK DROP\|FORBID <nick>` | s | FORBID stops anyone registering or using that nick. |
| `CHAN DROP\|FORBID\|SETFOUNDER #chan [nick]` | s | |
| `SESSION LIMIT <n>` / `SESSION EXCEPT ADD\|DEL\|LIST <host> <n>` | k | Connections allowed per host (clone limit). Default 3. |
| `NEWS ADD\|DEL\|LIST <text>` | g | Short lines shown to everyone after the MOTD. |

### 2.5 AdminServ: change the server live from IRC **DRAFT**

Every change is saved straight to the config file and applies at once, so there's no rehash. The Hive Panel makes the same changes through the same code.

| Command | Flag | What it does |
|---|---|---|
| `SHOW [setting]` | a | Current settings. |
| `SET <setting> <value>` | a | For example `SET maxusers 500`, `SET cloak off`, `SET nick-expire 120d`. |
| `MOTD SHOW\|SET\|ADD\|CLEAR` | a | Edits the MOTD from IRC. |
| `PORT ADD\|DEL\|LIST <port> [tls]` | a | |
| `OPER <nick> ADD FLAGS <letters> HOST <mask>` | a | Makes someone an oper (they need a registered nick). |
| `OPER <nick> SET FLAGS\|HOST\|TITLE <value>` / `OPER <nick> DEL` / `OPER LIST` | a | |
| `SERVER <name> ADD` | c | Adds a server, disabled. **Stage 2** |
| `SERVER <name> SET HOST\|PORT\|INPASS\|OUTPASS\|PING\|AUTOCONNECT\|TLS <value>` | c | **Stage 2** |
| `SERVER <name> ENABLE\|DISABLE\|LINK\|UNLINK\|DEL` / `SERVER LIST` | c | **Stage 2** |
| `RESTART [reason]` / `DIE [reason]` | a | Asks you to repeat with a confirm code. |

### 2.6 Oper flags

| Flag | Meaning |
|---|---|
| `a` | server admin: AdminServ, RESTART, DIE |
| `c` | link and unlink servers |
| `g` | GLOBAL and NEWS |
| `h` | see real hosts, hidden channels and full WHOIS |
| `k` | KILL, AKILL, session limits |
| `s` | OperServ nick and channel tools |

- An oper entry is an **account** (a registered nick), flags, a title and host masks. The installer creates the first one for your admin nick with all flags.
- **Opered on identify (decided 11 Nov):** you only become an oper after you identify to your registered nick, and only from a host on your oper entry. Services then give `+o` straight away, so no separate /oper password sits in client configs. Someone who takes your nick without the password gets nothing. `/oper <account> <password>` also works, and asks for the same NickServ password.

---

## 3. Running it

### 3.1 Install (as in the walkthrough)
`./install` asks 8 questions (server name, network, admin nick, admin password, ports, certificate, standalone/hub/leaf, link port). It shows a summary and writes nothing until you confirm. Then it:
- creates the system user `beeircd`
- copies the program to `/opt/beeircd/beeircd`, the config to `/etc/beeircd/beeircd.conf`, data to `/var/lib/beeircd`, logs to `/var/log/beeircd`
- gets a Let's Encrypt certificate (built into the program, renews by itself), or makes a self-signed one, or uses your files
- opens the ports in ufw or firewalld if one is active
- installs and enables the systemd service (restart on failure, starts at boot, hardened: no new privileges, read-only system)

### 3.2 The `beeircd` command
| Command | What it does |
|---|---|
| `beeircd start\|stop\|restart` | Through systemd. |
| `beeircd status` | The status screen from the walkthrough. |
| `beeircd check` | Checks config, certificate and database. Restart refuses to run if this fails. |
| `beeircd backup [file]` | One `.tar.gz` with config, database and certificates. A daily backup is kept, last 7. |
| `beeircd update` | Downloads the newest release, checks its signature, makes a backup, swaps the program and restarts. Rolls back if the new one doesn't start. |

### 3.3 Storage
- Config: one readable **TOML** file. AdminServ and the Hive Panel write to it, comments are kept.
- Services data: **SQLite** (pure Go, no C compiler needed). One file, `/var/lib/beeircd/services.db`.
- Behind NAT or a proxy: the PROXY protocol (v1/v2) is supported on listed ports, so the real IP is shown.

### 3.4 Safety
- Flood control per connection, connection throttling per IP, session (clone) limits, and a short wait before JOIN for new connections.
- Passwords stored as argon2id hashes. Wrong-password lockout: 5 tries, then 10 minutes, for IDENTIFY, OPER, SASL and the Hive Panel alike.
- TLS 1.2+ only. Plain port 6667 can be turned off.

---

## 4. Built ready for linking (Stage 2)

Things Stage 1 does now so linking doesn't mean a rewrite:
- Every server has a **server ID** (3 chars, like `0BE`). Every user has a **unique ID** (server ID + 6 chars) besides their nick, and everything inside the server uses the ID, never the nick.
- Every channel and nick carries a **timestamp**, so collisions after a split can be settled the classic way (older wins).
- The network state (users, channels, modes) sits in one module that only changes through **events** ("user joined", "mode set"). In Stage 2 the same events go over the link.
- Services run on **one server, the hub**, like ConferenceRoom. Leaves forward service commands over the link. Services data stays in one database, so there are no sync conflicts. If the hub is down, services are down too until it's back (decided 11 Nov). Replicating services to leaves can come later.
- The link layer sits behind one interface, so an UnrealIRCd or InspIRCd link module can be added later.

---

## 5. Stage 1 plan

Each step ends with tests passing and something you can try. I'll ask before the first commit and before any test build.

| # | Step | You can try |
|---|---|---|
| 1 | Install Go on the PC, set up the repo (module, folders, tests, `go vet`, lint) | – |
| 2 | Core server: connect, NICK/USER, PING, JOIN/PART, PRIVMSG/NOTICE, TOPIC, MODE, KICK, WHO/WHOIS, LIST, MOTD, TLS | Connect BChat to a test server on the homelab, chat in a channel |
| 3 | IRCv3 caps + SASL + host cloaks + flood control | BChat typing and reply tags, SASL login |
| 4 | Services framework + NickServ + MemoServ (SQLite) | Register, identify, ghost, memos |
| 5 | ChanServ (levels, AKICK, SET options) | Register #bchat, AOP/VOP and so on |
| 6 | OperServ + AdminServ + opers and flags | AKILL, GLOBAL, OPER ADD, SET from IRC |
| 7 | `./install`, `beeircd` command, systemd, Let's Encrypt, backup/update | Full install on a fresh test VM or container |

Testing at every step: Go unit tests, plus an automatic test that runs a real server and drives it with several fake clients (join, modes, every service command). I'll run all of that myself before asking you to try anything.

Test server (decided 11 Nov): a separate Docker container `beeircd-test` on the homelab, ports **26667** plain and **26697** TLS, open only on the home network. No router or VPS forward, nothing shared with the running UnrealIRCd, InspIRCd or ConferenceRoom.

## 6. Still open
1. The full command lists above (2.1 to 2.5): strike out, add or rename anything.

Licence: AGPL-3.0-or-later (decided 11 Nov).
