# Installing BeeIRCd

BeeIRCd is one program: the IRC server and its services (NickServ, ChanServ,
MemoServ, OperServ, HostServ, AdminServ, Global) together. The installer asks
nine questions, writes the files, starts the service and tells you what to do
next. This page covers the installer, the firewall, certificates and running
behind a proxy or tunnel. Day-to-day running is in [ADMIN.md](ADMIN.md).

## What you need

- Linux with systemd: **Debian 12 or 13, Ubuntu 22.04 or 24.04** are tested.
  Others with systemd usually work too.
- amd64 or arm64.
- Root (`sudo`).
- A small VPS is enough: 1 CPU and 512 MB hold about 5,000 people.
- For a Let's Encrypt certificate: a DNS name pointing at the machine, and
  port 80 reaching it.

## Install

```sh
curl -fsSL https://beeircd.org/install.sh | sudo sh
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
| 9 | This server is | 1) on its own, 2) a hub, 3) a leaf. Linking comes in Stage 2; a hub or leaf also gets a link port (7000) |

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
passwords. `/oper` only works once the nick is registered and identified.

## Firewall

The installer opens the client ports (and the link port for a hub or leaf)
when **ufw** or **firewalld** is active. If neither is running, it says so.
Then open the ports in your provider's firewall (the cloud panel) yourself:

- 6697/tcp (TLS) and 6667/tcp (plain), or the ports you chose;
- 80/tcp if you use Let's Encrypt (BeeIRCd listens there only for the few
  seconds a certificate is asked for);
- 7000/tcp on a hub or leaf, later, for server links.

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

## Uninstall

```sh
sudo beeircd uninstall          # removes the program and the service, keeps config and data
sudo beeircd uninstall --purge  # also deletes /etc/beeircd, /var/lib/beeircd (backups too) and the beeircd user
```

## Installing by hand

Without the installer (another init system, a container):

```sh
beeircd run -config /path/to/beeircd.conf
```

Relative paths in the config are read from the config file's folder. The
smallest working config:

```toml
[server]
name = "irc.example.org"
network = "ExampleNet"
sid = "0EX"

[[listen]]
address = ":6667"

[cloak]
secret = "at least 16 random characters"

[services]
database = "services.db"
admin = "YourNick"
```

`beeircd check -config ...` reads it and says what's wrong, in words. Every
setting is in [ADMIN.md](ADMIN.md#beeircdconf).
