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
| `beeircd update` | Installs the newest release (see [Updates](#updates)) |
| `beeircd mkpasswd` | Reads a password and prints its hash, for `admin_password` |
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
`log-connects`, `flood-profile`.

### Opers

The admin from `[services] admin` always has every flag and can't be changed
from IRC. Add others with AdminServ:

```
/as oper ember add class globop host *@203.0.113.* password Hive-2026
```

- The nick must be registered with NickServ. Opers `/oper <nick> <oper
  password>` from a matching host; the oper password is their own, not the
  NickServ one.
- Flags: `a` admin (AdminServ, REHASH, SA* commands), `c` links, `g` global,
  `h` hosts (CHGHOST and friends, HostServ), `k` kill, `s` services (OperServ).
- Built-in classes: `helper` (h), `globop` (hk), `admin` (ghks), `netadmin`
  (acghks). You can only give flags you have yourself.
- Opers skip the flood slowdown.

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
and the certificate files. Backups are readable only by the `beeircd` user and root. Copy backups off
the machine now and then; they contain password hashes and secrets.

`beeircd status` shows when the last backup was made.

### Restore

```sh
sudo beeircd stop
mkdir /tmp/restore && tar -xzf /var/lib/beeircd/backups/beeircd-2026-11-12-0400.tar.gz -C /tmp/restore
sudo cp /tmp/restore/beeircd.conf /etc/beeircd/beeircd.conf
sudo cp /tmp/restore/services.db /tmp/restore/history.db /var/lib/beeircd/
sudo cp /tmp/restore/tls/* /etc/beeircd/tls/            # only if the certificate is lost
sudo chown -R beeircd:beeircd /etc/beeircd /var/lib/beeircd
sudo rm -f /var/lib/beeircd/services.db-wal /var/lib/beeircd/services.db-shm
sudo beeircd check && sudo beeircd start
rm -rf /tmp/restore
```

To move to a new machine: install there with the same server name, then
restore the backup the same way. Keep the backup's `beeircd.conf` (its cloak
and history secrets go with the data).

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
  Only opers can sign in. The oper entry's hosts are checked against the
  address you come from (`*@*` and `*@127.0.0.1` work through a tunnel;
  hostname masks don't, the panel only knows the address).
- Wrong passwords count toward the same lock as `/oper` (5 wrong: 10
  minutes), and the panel also locks an address after 5 wrong tries.
- **Every click is checked by the server** against your oper entry and
  flags. Settings, Check and Rehash need flag `a`; Backup is for the
  owner (the `[services] admin`). Take an oper entry away and that oper is
  signed out at their next click.
- **People** lists everyone online on every server: search, filters
  (logged in, guests, opers), 50 at a time. Click a person for their
  details and to send them a notice. Real hosts and addresses show only
  for opers with flag `h` (and only they can search by them). Disconnect
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
  can be written by hand. Nothing is looked up from an address and no
  person is ever on the map.
  Lite turns off the glow and the moving signals (it switches on by itself
  when drawing is slow, and the signals stop with reduced motion). The
  outlines are Natural Earth (public domain), shipped with the panel.
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
- Behind a reverse proxy on the same machine, list the proxy in
  `[web] trusted_proxy` so the panel believes its `X-Forwarded-For` and
  `X-Forwarded-Proto` (https makes the session cookie Secure).


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
and two capitals; the installer makes one from the name, check the
others don't have it). The hub's port 7000 must be open to the leaves.

Linking a server already installed as standalone: set `[link] role`,
restart once (the link certificate is made then), and follow steps 3–4
with `beeircd link-info` on each side. For an UnrealIRCd 6 server on
the other end, `link-info` also prints the `link { }` block for
unrealircd.conf; see [link] below for services and SASL there.

## Security notes

- Connections are throttled per IP: 5 connects per 60 s (`connect_rate`),
  3 still logging in per IP, 500 logging in in total. An IP refused 3 times in
  10 minutes gets a 10-minute ZLINE. LAN IPs and OperServ exceptions skip it.
- `session_limit` (3) caps connections per IP; OperServ `EXCEPTION` raises it
  for one IP.
- `[[dnsbl]]` lists check new connections against DNS blocklists.
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
| `prefixes` | `"@+"` | `"@+"` or `"~&@%+"` (owner, admin, op, halfop, voice) (restart) |
| `casemapping` | `"ascii"` | How nicks and channels ignore case: `"ascii"` (only A-Z: `Belon` = `belon`, but `[` and `{` differ, like UnrealIRCd 6 and InspIRCd) or `"rfc1459"` (also `[]\~` = `{}|^`). Every linked server needs the same; UnrealIRCd links need ascii. Changing it moves the stored nicks and channels to the new rule at the next start; going to rfc1459 is refused when two stored names would become one (restart) |

### [[listen]]

One block per port.

| Setting | Default | Meaning |
| --- | --- | --- |
| `address` | – | `":6697"`, `"203.0.113.5:6697"`, `"[::]:6697"` |
| `tls` | `false` | Encrypted port |
| `proxy` | `false` | Connections start with a PROXY v1/v2 header ([INSTALL.md](INSTALL.md#behind-a-proxy-or-a-tunnel)) |
| `proxy_from` | – | IPs or CIDRs allowed on a proxy port (needed with `proxy`) |

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
| `admin_password` | – | argon2id hash from `beeircd mkpasswd`. Empty: the admin sets it with the first `/oper` while identified |
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

### [channels]

| Setting | Default | Meaning |
| --- | --- | --- |
| `flood_profile` | `"normal"` | The +F profile for channels without their own: `very-strict`, `strict`, `normal`, `relaxed`, `very-relaxed`, `off` |

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

### [mail]

For NickServ `RESETPASS` and MemoServ mail notices. Off by default.

| Setting | Default | Meaning |
| --- | --- | --- |
| `use_mail` | `false` | Switch mail on |
| `send_from` | – | From address |
| `smtp_server` | – | `"smtp.example.org:587"` (STARTTLS when offered); empty: sendmail |
| `smtp_user`, `smtp_password` | – | |
| `sendmail_path` | `"/usr/sbin/sendmail -t"` | |
| `delay` | `5m` | Wait between two mails per person |
| `dont_quote_addresses` | `false` | |
| `reset_subject`, `reset_message`, `memo_subject`, `memo_message` | Anope's texts | `%n` nick, `%N` network, `%c` code, `%s` sender, `%d` memo number, `%t` memo text |

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
| `flags` | Letters from `acghks` |
| `title` | WHOIS line, like "is a Network Administrator" |
| `vhost` | Host shown after `/oper` |
| `swhois` | Up to 5 extra WHOIS lines |

### [control]

| Setting | Default | Meaning |
| --- | --- | --- |
| `socket` | `beeircd.sock` next to the database | The socket the admin commands use (restart) |

### [backup]

| Setting | Default | Meaning |
| --- | --- | --- |
| `dir` | `backups` next to the database | |
| `keep` | `7` | Dated backups kept |

### [update]

| Setting | Default | Meaning |
| --- | --- | --- |
| `check` | `true` | Look once a day for a newer release |
| `url` | GitHub releases | Folder holding `release.json` |
| `rc_url` | GitHub releases | Where `update --rc` looks |

### [web]

The Hive Panel (`beeircd web`, the `beeircd-web` service); the server itself doesn't use it.

| Setting | Default | Meaning |
| --- | --- | --- |
| `listen` | `127.0.0.1:8787` | Where the panel answers; a loopback address only |
| `trusted_proxy` | none | Reverse proxies (IP addresses) whose forwarded address and https the panel believes |
| `session_idle` | `30m` | Signed out after this long without a click (1m to 24h) |

The panel's fonts (Inter, Space Grotesk, JetBrains Mono) are bundled
under the SIL Open Font License; the licences are in
`internal/hivepanel/ui/fonts`.

### [link]

Server linking (v1.1; walk-through in [Linking servers](#linking-servers)).
Linked servers share users, channels, every change, network bans and one
set of services. BeeIRCd speaks the UnrealIRCd 6 server protocol, so it
links with BeeIRCd and UnrealIRCd 6.

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
