# Installing BeeIRCd

BeeIRCd is one program: the IRC server and its services (NickServ, ChanServ,
MemoServ, OperServ, HostServ, BotServ, AdminServ, Global) together. The installer asks
nine questions, writes the files, starts the service and tells you what to do
next. This page covers the installer, the firewall, certificates and running
behind a proxy or tunnel. Day-to-day running is in [ADMIN.md](ADMIN.md).

## What you need

- Linux with systemd: **Debian 12 or 13, Ubuntu 22.04 or 24.04** are tested.
  Others with systemd usually work too. No systemd (Alpine, Devuan, Void, a
  container)? See [Without systemd](#without-systemd), or use [Docker](#docker).
- amd64 or arm64.
- Root (`sudo`).
- A small VPS is enough: 1 CPU and 512 MB hold about 5,000 people.
- For a Let's Encrypt certificate: a DNS name pointing at the machine, and
  port 80 reaching it.

## Install

```sh
curl -fsSL https://beeircd.org/install.sh | sudo sh
```

No curl on the machine? wget works the same:

```sh
wget -qO- https://beeircd.org/install.sh | sudo sh
```

The script downloads the program for your machine (amd64 or arm64) and runs
`beeircd install`. If you'd rather not pipe a script into a shell, do it by
hand:

```sh
curl -fsSLO https://github.com/fhazal/BeeIRCd/releases/latest/download/beeircd-linux-amd64
chmod +x beeircd-linux-amd64
sudo ./beeircd-linux-amd64 install
```

Use `beeircd-linux-arm64` on ARM machines.

### The nine questions

Enter keeps the `[default]`. Ctrl+C stops without changing anything, and
nothing is written before you say yes to the summary.

| # | Question | What to answer |
| --- | --- | --- |
| 1 | Server name | The DNS name people connect to, like `irc.example.org` |
| 2 | Network name | One word, like `ExampleNet` |
| 3 | Your admin nick | The nick you will use on IRC. It gets every oper right |
| 4 | Admin password | Your `/oper` password (at least 8 characters, typed twice, not shown). Only its hash is stored |
| 5 | Admin may /oper from | `user@host` masks or IPs. Over SSH it suggests your own IP. Empty = anywhere |
| 6 | Ports | Default `6667 plain, 6697 TLS`. Answer `6697` alone for TLS only |
| 7 | TLS certificate | 1) Let's Encrypt, 2) self-signed, 3) your own files (see [Certificates](#certificates)) |
| 8 | Channel prefixes | 1) `@+` op and voice, 2) `~&@%+` owner, admin, op, halfop, voice |
| 9 | This server is | 1) on its own, 2) a hub, 3) a leaf. A hub or leaf also gets a link port (7000). A leaf is asked for its hub: name, address (`host` or `host:port`) and fingerprint (`beeircd link-info` on the hub; Enter adds it later). At the end a hub or leaf prints the block the other server pastes, see [Linking servers](ADMIN.md#linking-servers) |

Then one yes/no: **Set up the Hive Panel too?** (default no). Yes adds the
web admin as the `beeircd-web` service on `127.0.0.1:8787`; see
[Hive Panel](ADMIN.md#hive-panel-web-admin).

### What the installer sets up

| What | Where |
| --- | --- |
| The program | `/opt/beeircd/beeircd`, linked as `/usr/local/bin/beeircd` |
| Config, with every setting explained | `/etc/beeircd/beeircd.conf` |
| Message of the day | `/etc/beeircd/motd.txt` |
| Certificates | `/etc/beeircd/tls/` |
| Accounts, channels, memos, history, backups | `/var/lib/beeircd/` |
| Control socket for the admin commands | `/run/beeircd/beeircd.sock` |
| The service | `beeircd.service`, running as the `beeircd` user |
| Daily backup at 04:00 server time, newest 7 kept | `beeircd-backup.timer` |
| Logs | journald: `journalctl -u beeircd` |
| The Hive Panel, if you said yes | `beeircd-web.service` (`beeircd web`), on `127.0.0.1:8787` only |

The service is locked down by systemd: no new privileges, a read-only
system except `/etc/beeircd` and `/var/lib/beeircd`, no access to home
folders. It may only bind low ports (`CAP_NET_BIND_SERVICE`), which
Let's Encrypt needs for port 80.

The cloak secret and the history secret are made at random. Keep a copy of
`beeircd.conf` somewhere safe (the daily backup has it too): a new cloak
secret changes everyone's cloak, and a new history secret can't read the old
history.

### First steps on IRC

The installer prints these at the end:

1. Connect to your server with your admin nick.
2. Wait 30 seconds, then register the nick:
   `/msg NickServ REGISTER <a NickServ password> <your email>`
3. Oper up: `/oper <admin nick> <admin password>`

The NickServ password and the admin (oper) password are two different
passwords. `/oper` only works once the nick is registered.

## Firewall

The installer opens the client ports (and the link port for a hub or leaf)
when **ufw** or **firewalld** is active. If neither is running, it says so.
Then open the ports in your provider's firewall (the cloud panel) yourself:

- 6697/tcp (TLS) and 6667/tcp (plain), or the ports you chose;
- 80/tcp if you use Let's Encrypt (BeeIRCd listens there only for the few
  seconds a certificate is asked for);
- 7000/tcp on a hub, for server links (a leaf connects out to its hub; open
  it on a leaf only if the hub should be able to connect to it).

`beeircd uninstall` leaves firewall rules alone.

## Certificates

**1) Let's Encrypt** (the default for a public name). BeeIRCd gets the
certificate itself on the first start and renews it when a third of its life
is left. Port 80 of the server name must reach the machine. Retries after a
failure come after 1 h, 3 h and 12 h; the server keeps running with a
temporary self-signed certificate meanwhile, and opers get a notice.

```toml
[tls]
acme = true
acme_email = "you@example.org"   # optional: Let's Encrypt mails you before expiry
# acme_domains = ["irc.example.org", "chat.example.org"]   # names on the certificate (the server name when empty)
```

**2) Self-signed.** Made at the first start. Clients warn about it. Fine for
a LAN or a test; switch later by setting `acme = true` and running
`beeircd restart`.

**3) Your own files.** Files from certbot (`/etc/letsencrypt/live/...`) are
copied into `/etc/beeircd/tls/`, and a certbot deploy hook copies each
renewal there. Other files are used where they are; the `beeircd` user must
be able to read them (the installer checks), and they can't be in a home
folder. BeeIRCd picks a changed certificate up by itself within a minute;
nobody is disconnected.

**STS.** With a real certificate (Let's Encrypt or your own) BeeIRCd tells
modern clients on the plain port to use TLS from then on (IRCv3 `sts`). If
people connect by an IP or a name the certificate doesn't cover, turn it off:

```toml
[tls]
sts = false
```

## Behind a proxy or a tunnel

When connections reach BeeIRCd through a load balancer or a tunnel (for
example a cheap VPS forwarding to a home server), BeeIRCd would see the
proxy's IP for everyone. With the **PROXY protocol** (v1 or v2, as HAProxy,
nginx `stream` and most load balancers send it) it sees the real IP:

```toml
[[listen]]
address = ":6697"
tls = true
proxy = true
proxy_from = ["203.0.113.10"]   # the proxy's IPs or CIDRs; nobody else may use this port
```

- A port with `proxy = true` only takes connections from `proxy_from`, and
  each must start with a PROXY header. Anything else is closed silently.
- TLS stays end to end: the proxy forwards raw TCP; BeeIRCd does the TLS.
- Bans, throttles, sessions and cloaks all use the real IP.

nginx on the front VPS, for example:

```nginx
stream {
    server {
        listen 6697;
        proxy_pass 10.0.0.2:6697;
        proxy_protocol on;
    }
}
```

**Let's Encrypt through a tunnel.** If the outside port 80 is forwarded to
another port on the BeeIRCd machine, say where with `acme_http`:

```toml
[tls]
acme = true
acme_http = ":8080"
```

**STS through a tunnel.** If the outside TLS port differs from the one
BeeIRCd listens on, set `sts_port` to the outside one.

## Check it works

```sh
sudo beeircd status
sudo beeircd check
```

`status` shows the version, ports, people online, services, memory, data,
the last backup and the certificate. `check` tests the config, certificate,
database and history file, and says if a restart would be safe. More in
[ADMIN.md](ADMIN.md).

## BeeRoom (web chat)

BeeRoom is the web chat that comes with BeeIRCd: people chat from the
browser, and the Hive Panel's Chat tab shows it. It has its own signed
installer and needs Linux with systemd and Node.js 22 (no systemd: use
the Docker `chat` profile, see [Docker](#docker)):

```sh
curl -fsSL https://beeircd.org/beeroom.sh | sudo sh
```

It checks the download's signature, then asks two questions on the BeeIRCd
machine: the web chat's address and the channels people join (on another
machine also the IRC server, its WEBIRC password and the network name). It
finds Apache, nginx or Caddy by itself and asks before writing their site
or getting a certificate. On the BeeIRCd machine it adds its WEBIRC
gateway by itself (`beeircd webirc add`), so BeeIRCd sees each person's
real IP. Run the same line again later to update. With Docker it is one
profile instead: see [Docker](#docker).

To show it in the Hive Panel's Chat tab, see
[Let members reach the Hive Panel](ADMIN.md#let-members-reach-the-hive-panel).

## Uninstall

```sh
sudo beeircd uninstall          # removes the program and the service, keeps config and data
sudo beeircd uninstall --purge  # also deletes /etc/beeircd, /var/lib/beeircd (backups too) and the beeircd user
```

## Docker

The BeeIRCd image runs on any machine with Docker: a NAS (Synology,
Unraid, TrueNAS), any Linux, or Windows and Mac with Docker Desktop. The
image is small (about 37 MB) and runs the server as an unprivileged user.

Save [compose.yaml](../deploy/docker/compose.yaml) in an empty folder, then:

```sh
docker compose run --rm beeircd install    # the same nine questions
docker compose up -d                       # start; it comes back after a reboot
```

| What | How |
| --- | --- |
| How it runs | `docker compose exec beeircd beeircd status` |
| Admin commands | `docker compose exec beeircd beeircd <command>` (`check`, `backup`, `rehash`, `link-info`, `webirc`) |
| Logs | `docker compose logs beeircd` |
| Restart | `docker compose restart beeircd` |
| Update | `docker compose pull && docker compose up -d` (your data stays) |
| Hive Panel | say yes to it in the questions, then `docker compose --profile panel up -d`; it answers on `127.0.0.1:8787` of the Docker machine only, reach it with the SSH tunnel from [Hive Panel](ADMIN.md#hive-panel-web-admin). Never publish it on other addresses: in Docker the panel counts the Docker machine itself as 127.0.0.1, so visitors from outside would too. For access from elsewhere use HTTPS through a web server on the Docker machine ([Let members reach the Hive Panel](ADMIN.md#let-members-reach-the-hive-panel)) |
| BeeRoom web chat | `docker compose --profile chat up -d`; it answers on `127.0.0.1:9000` for your web server in front (below) |

- **BeeRoom** needs nothing else: on its first start BeeIRCd adds the web
  chat's WEBIRC gateway for BeeRoom's fixed address in compose.yaml
  (`10.89.0.20`) and hands BeeRoom only that password and the network
  name. People's real IPs then reach BeeIRCd. Put a web server with HTTPS in
  front, for example Caddy:

  ```
  chat.example.org {
      reverse_proxy 127.0.0.1:9000
  }
  ```

  nginx and Apache work the same way: pass `/` (with WebSockets) to
  `http://127.0.0.1:9000/` and set `X-Forwarded-For`. If `10.89.0.0/24`
  clashes with a network you already have, change `10.89.0` in all three
  places in compose.yaml (`BEEROOM_WEBIRC`, `ipv4_address`, `subnet`); the
  next start moves BeeRoom's `[[webirc]]` entry to the new address.
- **Data.** Config, accounts, channels, history and backups are all in
  `/data`, kept in the `beeircd-data` volume. To use a folder instead,
  change `beeircd-data:/data` to `./data:/data`; the container makes it
  belong to its user by itself.
- **Backups** run daily at 04:00 in the `TZ` time zone set in compose.yaml
  (UTC by default), newest 7 kept.
- **Ports.** compose.yaml publishes 6667 and 6697. Uncomment `7000` for a
  hub and `80` for Let's Encrypt. Open the same ports in your firewall.
- **Real IPs.** On Linux, people's own IPs reach BeeIRCd, so bans and limits
  work per person. Docker Desktop on Windows and Mac can show everyone with
  one address: fine for trying BeeIRCd, but run a public network on Linux.
- `beeircd update`, `start`, `stop` and `restart` don't apply in Docker; each
  says which `docker compose` command to use instead.

## Without systemd

The guided installer needs systemd. On Alpine (OpenRC), Devuan, Void, in a
container or anywhere else, set BeeIRCd up by hand: about five minutes. The
steps use the same folders as the installer, so `beeircd status`, `check`,
`backup`, `rehash`, `webirc` and `link-info` work exactly as described in
[ADMIN.md](ADMIN.md). Only `beeircd start`, `stop` and `restart` are
systemd's: use your own service manager for those.

Run these as root.

**1. The program, a user and two folders.**

```sh
arch=amd64    # or arm64
mkdir -p /opt/beeircd
wget -O /opt/beeircd/beeircd https://github.com/fhazal/BeeIRCd/releases/latest/download/beeircd-linux-$arch
chmod 755 /opt/beeircd/beeircd
ln -sf /opt/beeircd/beeircd /usr/local/bin/beeircd

# Alpine:
addgroup -S beeircd
adduser -S -D -H -h /var/lib/beeircd -s /sbin/nologin -G beeircd beeircd
# Devuan, Void and most others:
#   useradd --system --user-group --no-create-home --home-dir /var/lib/beeircd --shell /usr/sbin/nologin beeircd

mkdir -p /etc/beeircd/tls /var/lib/beeircd/backups
chown -R beeircd:beeircd /etc/beeircd /var/lib/beeircd
chmod 750 /etc/beeircd /var/lib/beeircd
```

**2. The config.** Make your oper password's hash first (it asks for the
password; at least 8 characters):

```sh
beeircd mkpasswd
```

Then write `/etc/beeircd/beeircd.conf`. Change the names, paste the hash, and
put two different long random strings in the two `secret` lines
(`head -c 24 /dev/urandom | base64` makes one):

```toml
[server]
name = "irc.example.org"     # the DNS name people connect to
network = "ExampleNet"
sid = "0EX"                  # a digit and two capitals or digits, unique on your network

[[listen]]
address = ":6667"

[[listen]]
address = ":6697"
tls = true

[tls]
cert = "/etc/beeircd/tls/cert.pem"
key = "/etc/beeircd/tls/key.pem"
self_signed = true           # made at the first start; see Certificates for Let's Encrypt

[cloak]
secret = "a long random string"

[services]
database = "/var/lib/beeircd/services.db"
admin = "YourNick"
admin_password = "the hash from beeircd mkpasswd"
admin_hosts = ["203.0.113.5"]   # where you may /oper from (user@host masks or IPs); [] = anywhere

[history]
default = "50:1d"
secret = "another long random string"

[control]
socket = "/run/beeircd/beeircd.sock"

[backup]
dir = "/var/lib/beeircd/backups"
keep = 7
```

```sh
chown beeircd:beeircd /etc/beeircd/beeircd.conf
chmod 640 /etc/beeircd/beeircd.conf
beeircd check
```

`check` reads the config and says what's wrong, in words. Every setting is
in [ADMIN.md](ADMIN.md#beeircdconf).

**3. The service.** On OpenRC (Alpine, Gentoo, Devuan with OpenRC), save this
as `/etc/init.d/beeircd`:

```sh
#!/sbin/openrc-run
name="BeeIRCd"
description="BeeIRCd IRC server"
command="/opt/beeircd/beeircd"
command_args="run -config /etc/beeircd/beeircd.conf"
command_user="beeircd:beeircd"
supervisor="supervise-daemon"
respawn_delay=5
output_log="/var/log/beeircd.log"
error_log="/var/log/beeircd.log"
extra_started_commands="reload"

depend() {
	need net
	after firewall
}

start_pre() {
	checkpath -d -m 0750 -o beeircd:beeircd /run/beeircd
	checkpath -f -m 0640 -o beeircd:beeircd /var/log/beeircd.log
}

start_post() {
	# wait until it answers, like `beeircd start` does under systemd
	for i in $(seq 1 40); do
		$command status -config /etc/beeircd/beeircd.conf >/dev/null 2>&1 && return 0
		sleep 0.5
	done
	ewarn "BeeIRCd didn't answer within 20 seconds; see /var/log/beeircd.log"
}

reload() {
	ebegin "Reloading $name"
	supervise-daemon "$RC_SVCNAME" --signal HUP
	eend $?
}
```

```sh
chmod 755 /etc/init.d/beeircd
rc-update add beeircd default
rc-service beeircd start
beeircd status
```

`rc-service beeircd reload` reads the config again, like `beeircd rehash`.
The log is `/var/log/beeircd.log`. Another service manager (runit, s6, a
container): run the same command line as the `beeircd` user, after making
`/run/beeircd` owned by it:

```sh
/opt/beeircd/beeircd run -config /etc/beeircd/beeircd.conf
```

**4. The daily backup.** Add this line to the `beeircd` user's crontab
(`crontab -u beeircd -e`):

```
0 4 * * * /opt/beeircd/beeircd backup -config /etc/beeircd/beeircd.conf
```

Alpine runs cron through `crond`: `rc-update add crond default` and
`rc-service crond start` if it isn't on yet.

**Ports under 1024.** The `beeircd` user can't open them. Let's Encrypt needs
port 80 only for a few seconds per certificate: forward it to a high port
and set `acme_http` (see [Behind a proxy or a tunnel](#behind-a-proxy-or-a-tunnel)).

**Uninstall.** Stop and remove the service (`rc-service beeircd stop`,
`rc-update del beeircd`, delete `/etc/init.d/beeircd`), then delete
`/opt/beeircd`, `/usr/local/bin/beeircd` and, to drop all data too,
`/etc/beeircd`, `/var/lib/beeircd` and the `beeircd` user.

