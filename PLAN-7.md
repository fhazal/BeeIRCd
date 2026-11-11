# PLAN 7 - install, the `beeircd` command, TLS, and hardening for v1.0

Status: approved 12 Nov 2026 with all ten recommendations (D1-D10). 7a built 12 Nov 2026 (uncommitted).

Step 7 turns BeeIRCd from "a binary we deploy by hand" into something a
stranger can install on a small VPS with one command, keep running, back up
and update. After step 7 the single-server v1.0 is feature complete; what
remains is the release itself.

Sources: SPEC.md §3 (install, `beeircd` command, storage, safety),
AUDIT-TESTING.md S3 + T2, FEATURES.md "Planned (step 7)" rows, UnrealIRCd 6
connthrottle / anti-flood, InspIRCd 4 connectban, the user's live Unreal
and wixchat setups (VPS tunnel in front of the homelab).

Note: the install walkthrough artifact linked in SPEC.md can no longer be
opened, so the `beeircd status` screen gets a fresh mockup before it is
built (D10).

## Sub-steps (each one: build, check.sh + its tests, ask to commit)

### 7a - connection safety (server only, small)
- **S3 connect throttle**, like UnrealIRCd anti-flood connect-flood and
  InspIRCd connectban: per IP at most **5 connections per 60 s**; more are
  refused with `ERROR :Closing link: Throttled (reconnecting too fast)`
  before any DNS/DNSBL work. An IP refused 3 times in 10 minutes gets a
  10-minute ZLINE (snotice to opers, OperServ log line).
- **Unregistered cap**: at most **3 unregistered connections per IP** and
  **500 in total**; registration already times out.
- Exempt: LAN/loopback, ELINE, session exceptions (same rules as DNSBL).
- `[limits]` settings `connect_rate = "5:60"`, `max_unregistered = 500`,
  `max_unregistered_per_ip = 3`; AdminServ SET for all three.
- **Opers skip fakelag** (handoff note 11-12; Unreal/Insp have this).
- **PROXY protocol v1/v2** on ports marked `proxy = true` in `[[listen]]`,
  accepted only from `proxy_from` CIDRs (SPEC §3.3; needed when a VPS
  tunnels to a homelab, like wixchat). The real IP then drives cloaks,
  bans, DNSBL and the throttle.
- Tests: throttle + zline, exemptions, unregistered caps, PROXY v1 + v2 +
  refusing PROXY from other IPs. Load gate (accept path changes).

### 7b - `beeircd check`, `status`, `backup`, REHASH
- **Control socket** `/run/beeircd/control` (unix, mode 0660, owner
  beeircd, group beeircd-admin): the admin commands talk to the running
  server through it. No network port.
- `beeircd check [-config]`: config, certificate (dates, host name, key
  match), database (opens read-only, integrity check), history file +
  secret, ports free. `restart` refuses when it fails.
- `beeircd status`: one screen (mockup first, D10): version, uptime,
  users/opers/channels, peak, memory, loop lag, certificate expiry, last
  backup, services DB size, history size.
- `beeircd backup [file]`: one `.tar.gz` with beeircd.conf, a consistent
  copy of services.db (SQLite online backup through the running server -
  the DB is locked EXCLUSIVE, see handoff), history.db and certificates;
  mode 0600. Without a running server it copies the files directly.
  Daily systemd timer keeps the last 7 (`/var/lib/beeircd/backups`).
- **REHASH** (oper flag a, `/rehash`, `beeircd rehash`, and SIGHUP):
  re-reads beeircd.conf and applies what is safe live: MOTD, limits, oper
  classes, DNSBL lists, flood profiles, history caps, TLS certificate.
  Changes that need a restart (ports, SID, server name, prefixes, cloak
  on/off and secret) are listed in
  the reply and the snotice, not applied. A config that fails the check is
  refused and the old one stays.
- **Certificate reload without restart** (GetCertificate), also on file
  change, so certbot or our own ACME can renew in place.

### 7c - Let's Encrypt and STS
- Built-in ACME (golang.org/x/crypto/acme/autocert, already a module we
  use parts of): `[tls] acme = true`, `acme_email`, host = server name.
  Challenge method: D2 (recommended HTTP-01 on port 80, open only while a
  certificate is requested or renewed; systemd grants
  CAP_NET_BIND_SERVICE). Renews 30 days before expiry; falls back to the
  current certificate and warns opers if renewal fails.
- Three choices stay: Let's Encrypt, your own files, self-signed.
- **STS** (IRCv3 `sts` cap): advertised only with a real (non-self-signed)
  certificate: `sts=port=6697,duration=2592000` on plain, `duration` on
  TLS. `[tls] sts = true` by default when ACME or own files are used.
- Tests: ACME against a local Pebble test CA in the homelab container;
  STS cap values; cert reload during live TLS connections.

### 7d - the installer
- **`beeircd install`** inside the program (D1), started by a two-line
  `install.sh` that downloads the right binary. One binary = tested Go
  code, no shell logic to rot.
- Questions (SPEC §3.1 + decisions since): server name, network name,
  admin nick, admin password (hashed at once), ports (6667/6697), plain
  port on or off, certificate (Let's Encrypt / own files / self-signed),
  **prefix style `@+` or `~&@%+`** (11-11), **admin_hosts** (11-11, empty =
  warning), standalone/hub/leaf + link port (stored for Stage 2). Shows a
  summary, writes nothing until confirmed.
- Then: system user `beeircd`; `/opt/beeircd/beeircd`,
  `/etc/beeircd/beeircd.conf` (0640, comments explaining every block),
  `/var/lib/beeircd` (services.db, history.db with a fresh random
  secret), `/var/log/beeircd`; systemd unit (Restart=on-failure, boot
  start, NoNewPrivileges, ProtectSystem=strict, ProtectHome, PrivateTmp,
  ReadWritePaths only for data/log/conf, CAP_NET_BIND_SERVICE only if ports
  < 1024 or ACME); backup timer; ufw/firewalld rules if one is active;
  `beeircd check`, start, show `status`.
- `beeircd uninstall` (keeps data unless `--purge`).
- Supported: D7. Others get a clear "install by hand" page.
- **Clean-machine test**: install on fresh Debian 13 and Ubuntu 24.04
  (incus/LXD system containers on the homelab, real systemd), connect,
  register, oper, reboot, still up. Scripted so it can be re-run.

### 7e - `beeircd update` and releases
- Releases built by GitHub Actions on a tag: linux amd64 + arm64,
  checksums, **ed25519 signature** with a key kept offline like the
  Daeva meter's (D4); the public key is built into the binary.
- `beeircd update`: checks the newest release (D3 decides where), verifies
  the signature, makes a backup, swaps the binary, restarts through
  systemd, waits for `ready`; **rolls back** to the old binary if the new
  one does not come up within 30 s. `update --check` only looks.
- `start | stop | restart` call systemctl (restart runs check first).

### 7f - docs and the v1.0 gates
- Docs: INSTALL.md (install, firewall, certificates, behind a proxy),
  ADMIN.md (every beeircd.conf block, AdminServ, REHASH, backup/restore,
  update), README roadmap row 7 done. No AI credit (BajuBesi Labs only).
- Gates (stage end + release): check.sh, Linux race, load gate, bench2
  (small VPS: 5k ~115 MB, chat ~55%, lag ~5 ms), irctest, govulncheck,
  testssl.sh on 6697 (TLS 1.2+ only, no weak ciphers), 24 h soak on the
  homelab with the load bot (memory flat), fuzzing 1 h per target,
  security review of the new parts (control socket, installer, update,
  PROXY), clean-machine install again.
- Then ask: tag v1.0.0-rc1.

## Decisions for the user

| # | Question | Recommendation |
|---|---|---|
| D1 | Installer form | **`beeircd install` in Go** + tiny install.sh (vs a large shell script) |
| D2 | Let's Encrypt challenge | **HTTP-01 on port 80, opened only while renewing** (TLS-ALPN needs 443 for good; "own files" covers certbot users) |
| D3 | Where `update` downloads from (repo is private today) | **Make the GitHub repo public at v1.0** and use its releases; until then update from the URL in `[update] url` |
| D4 | Release signing | **ed25519, key offline, public key in the binary** (same idea as the Daeva meter) |
| D5 | Throttle numbers | **5 connects / 60 s per IP, 3 unregistered per IP, 500 total, zline 10 min after 3 refusals in 10 min** |
| D6 | REHASH | **Apply what is safe live, list what needs a restart**, refuse a bad config |
| D7 | Supported systems for the installer | **Debian 12/13, Ubuntu 22.04/24.04** (systemd, amd64 + arm64); others by hand |
| D8 | PROXY protocol in step 7 | **Yes** (small, and needed behind a VPS tunnel) |
| D9 | Opers skip fakelag | **Yes, every oper** (Unreal/Insp do the same) |
| D10 | `beeircd status` screen | **Mockup first** (the old walkthrough is gone) |

## Order and size

7a (~1 day) -> 7b (~1-2 days) -> 7c (~1 day) -> 7d (~1-2 days) -> 7e (~1 day)
-> 7f (docs + 24 h soak). Each sub-step is committed on its own after its
tests; deploys to the test server only when asked.

Progress when done: v1.0 ~100% feature complete (release left), full
vision ~50%.
