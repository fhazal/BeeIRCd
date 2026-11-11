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
| `secret` | – | At least 16 random characters; needed when enabled. Changing it changes every cloak; linked servers need the same one |

### [services]

| Setting | Default | Meaning |
| --- | --- | --- |
| `database` | `/var/lib/beeircd/services.db` | Accounts, nicks, channels, memos, bans (restart) |
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

### [link]

Server linking is being built (Stage 2, v1.1). Since 2a servers link and
keep the server tree; users and channels follow in 2b/2c. BeeIRCd speaks
the UnrealIRCd 6 server protocol, so it links with BeeIRCd and UnrealIRCd 6.

| Setting | Default | Meaning |
| --- | --- | --- |
| `role` | `"standalone"` | `standalone`, `hub` or `leaf` (restart). A leaf keeps one link |
| `address` | all addresses | IP the link port listens on, like `127.0.0.1` (restart) |
| `port` | `7000` for hub/leaf | TLS port for server links (restart) |
| `cert`, `key` | next to services.db | This server's link certificate, made at the first start as a hub or leaf and never renewed, so its fingerprint stays the same. It is in every backup |

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

`beeircd link-info` prints this server's fingerprint and the blocks the
other side needs. Links use TLS 1.2+ only; plain links are never accepted.
Opers with `+s` see every link event (established, refused and why, sync
time, split). `BEEIRCD_LINK_DEBUG=1` in the environment logs every line
sent and received on links.
