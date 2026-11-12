<div align="center">

<img src="docs/banner.webp" alt="BeeIRCd, the Internet Relay Chat daemon" width="85%">

# BeeIRCd

**The Hive of IRC.**<br>
Where communities gather, conversations thrive, and every connection strengthens the hive.

An IRC server with its services built in: one program, one config file, everything a network needs.<br>
Classic IRC feel · services in the box · IRCv3 · light on a 1-CPU VPS

[![CI](https://github.com/fhazal/BeeIRCd/actions/workflows/ci.yml/badge.svg)](https://github.com/fhazal/BeeIRCd/actions/workflows/ci.yml)
![Go 1.27](https://img.shields.io/badge/Go-1.27-00add8?logo=go&logoColor=white)
[![Licence: AGPL-3.0-or-later](https://img.shields.io/badge/licence-AGPL--3.0--or--later-f5b301)](LICENSE)
![Status: pre-release](https://img.shields.io/badge/status-pre--release-8b5cf6)

[![Website](https://img.shields.io/badge/Website-beeircd.org-f5b301?style=for-the-badge)](https://beeircd.org)

</div>

> [!NOTE]
> BeeIRCd is a free, open-source IRC server by **BajuBesi Labs**, written from scratch in Go. It is **not released yet**: Stage 1.5 (services and modes) is almost done and runs on our test network. Follow the progress below.

---

## 🐝 Why BeeIRCd

Running an IRC network usually means two programs that have to be wired together: a server (UnrealIRCd, InspIRCd) and a services package (Anope, Atheme). BeeIRCd is both. Start it once, and NickServ, ChanServ and friends are already there, sharing one database and one config.

- 🧩 **All in one**: server and services in a single binary, no separate services package to link and babysit. Servers still link to each other to grow a network (BeeIRCd to BeeIRCd, Stage 2).
- 🛡️ **Services you already know**: commands, syntax and replies follow **Anope 2.0** (the stable line), so admins and users feel at home.
- 👑 **AdminServ**: run the server from IRC itself, ConferenceRoom style: settings, MOTD, ports, opers and classes, with history and undo.
- ⚡ **Small and fast**: 5,000 users on a 1-CPU / 512 MB VPS with single-digit millisecond lag.
- 🌐 **Modern IRC**: IRCv3 capabilities, SASL, TLS, cloaked hosts, MONITOR.

## ✨ What's inside today

<table>
<tr><td width="50%" valign="top">

**🖧 Server**
- TLS 1.2+ listeners, HMAC host cloaking
- Prefixes `@+` or `~&@%+` (your choice)
- UnrealIRCd-style channel modes, flood guard `+f` / `+F` profiles, `+L` link channels
- Bans with extbans `~a ~r ~m ~t`, timed bans
- AKILL, Z-lines, SHUN, SQLINE / SNLINE, DNS blocklists (DroneBL, EFnetRBL)
- Oper classes, oper vhosts and swhois, `/oper <nick> <password>`

</td><td width="50%" valign="top">

**🤖 Services (Anope 2.0 style)**
- **NickServ**: register, identify, SASL PLAIN + EXTERNAL, certificates, grouping, AJOIN
- **ChanServ**: access levels, xOP lists, AKICK, MLOCK, SET options
- **MemoServ**: memos, channel memos, notify by mail
- **HostServ**: vhosts with requests and approval
- **OperServ**: AKILL, sessions, news, SEEN, IGNORE
- **AdminServ**: live server settings with HISTORY / DIFF / UNDO

</td></tr>
<tr><td valign="top">

**📡 IRCv3**
- CAP 302, message-tags, server-time, message-ids
- SASL, account-notify, account-tag, extended-join
- batch, labeled-response, echo-message
- chghost, invite-notify, away-notify, MONITOR

</td><td valign="top">

**✅ Tested**
- Unit, integration and fuzz tests, race detector in CI
- [irctest](https://github.com/progval/irctest) conformance: 270 passing
- Live checks against a real test network after every deploy

</td></tr>
</table>

The full list, with what is planned and what is only an idea, is in [FEATURES.md](FEATURES.md).

## 🗺️ Roadmap

| Stage | What | Status |
| --- | --- | --- |
| 1 | One server with all services working | ✅ Done |
| 1.5 | Anope-style services, HostServ, oper classes, channel and user modes | ✅ Done |
| 7 | Installer, `beeircd` command, systemd, Let's Encrypt, REHASH, signed updates | ✅ Done, v1.0 checks running |
| 2 | Linking BeeIRCd to BeeIRCd | ⏳ Planned |
| 3 | **Hive Panel**: web admin | 🚧 Started: every page built (sign-in, overview, people, channels, bans, opers, linked servers, services, settings); bug sweep and release gates next |
| 4 | **BeeRoom**: web chat | ⏳ Planned |

## 📦 Installing

```sh
curl -fsSL https://beeircd.org/install.sh | sudo sh
```

Nine questions, then it runs. [INSTALL.md](INSTALL.md) has the details
(firewall, certificates, proxies); [ADMIN.md](ADMIN.md) covers running it
(`beeircd status`, AdminServ, backups, updates, every setting).

## 🛠️ Building

Needs Go 1.27 or newer.

```sh
go build ./cmd/beeircd
./beeircd version
```

Checks (gofmt, `go vet`, [staticcheck](https://staticcheck.dev) and every test):

```sh
scripts/check.sh
```

How BeeIRCd is tested, and how to run irctest yourself: [TESTING.md](TESTING.md).

## 📂 Layout

| Folder | What lives there |
| --- | --- |
| `cmd/beeircd` | the `beeircd` program and its commands |
| `internal/` | the server, services and storage, one package each |
| `tools/` | live checks, load generator, irctest controller |
| `scripts/` | developer scripts |

<p align="center">
  <img src="docs/mascot.webp" alt="The BeeIRCd queen bee at her server rack" width="55%">
</p>

## 📜 Licence

GNU Affero General Public License v3.0 or later, see [LICENSE](LICENSE). If you run a changed BeeIRCd for other people, you share your changes.

---

<div align="center">

A **BajuBesi Labs** project · [beeircd.org](https://beeircd.org)

</div>
