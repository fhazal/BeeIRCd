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
[![Release](https://img.shields.io/github/v/release/fhazal/BeeIRCd?color=f5b301)](https://github.com/fhazal/BeeIRCd/releases/latest)

[![Website](https://img.shields.io/badge/Website-beeircd.org-f5b301?style=for-the-badge)](https://beeircd.org)

</div>

> [!NOTE]
> BeeIRCd is a free, open-source IRC server by **BajuBesi Labs**, written from scratch in Go. **1.1 is out**: the server with its services, server linking, the Hive Panel web admin and BeeRoom web chat, now with a spam filter and moving from Anope.

---

## 🐝 Why BeeIRCd

Running an IRC network usually means two programs that have to be wired together: a server (UnrealIRCd, InspIRCd) and a services package (Anope, Atheme). BeeIRCd is both. Start it once, and NickServ, ChanServ and friends are already there, sharing one database and one config.

- 🧩 **All in one**: server and services in a single binary, no separate services package to link and babysit.
- 🔗 **Linking**: grow one server into a network. Links are TLS with pinned keys, no passwords, to other BeeIRCd servers, UnrealIRCd 6 or InspIRCd 4.
- 🍯 **Hive Panel**: a web admin built in (`beeircd web`). Opers sign in with their oper account and see only what their flags allow.
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
- Oper classes, oper vhosts and swhois, `/oper <nick> <password>` or a TLS client certificate
- Spam filter: `/SPAMFILTER` and `STATS F` with UnrealIRCd's syntax, shared over links

</td><td width="50%" valign="top">

**🤖 Services (Anope 2.0 style)**
- **NickServ**: register, identify, SASL PLAIN + EXTERNAL, certificates, grouping, AJOIN
- **ChanServ**: access levels, xOP lists, AKICK, MLOCK, SET options
- **MemoServ**: memos, channel memos, notify by mail
- **HostServ**: vhosts with requests and approval
- **OperServ**: AKILL, sessions, news, SEEN, IGNORE
- **BotServ**: channel bots, kickers, bad words, fantasy commands
- **AdminServ**: live server settings with HISTORY / DIFF / UNDO
- **Moving from Anope**: `beeircd import-anope` brings anope.db over, passwords too

</td></tr>
<tr><td valign="top">

**🔗 Linking**
- Hub and leaf, TLS on its own port, locked by fingerprint
- BeeIRCd to BeeIRCd, UnrealIRCd 6 and InspIRCd 4, with services and SASL
- One network: users, channels, bans, services and oper entries shared
- `/MAP`, `beeircd link-info`, links shown in `beeircd status`

</td><td valign="top">

**🍯 Hive Panel**
- Overview: network status, Spotlight, health, server details
- People, Channels, Bans, Opers, Linked servers with a network map
- Services, Settings, Backups and Spam filter; changes kept in HISTORY with Undo
- Every click checked by the server against your oper flags

</td></tr>
<tr><td valign="top">

**📡 IRCv3**
- CAP 302, message-tags, server-time, message-ids
- SASL, account-notify, account-tag, extended-join
- batch, labeled-response, echo-message
- chghost, invite-notify, away-notify, MONITOR, extended-monitor
- WHOX, bot mode, no-implicit-names

</td><td valign="top">

**✅ Tested**
- Unit, integration and fuzz tests, race detector in CI
- [irctest](https://github.com/progval/irctest) conformance: 270 passing
- Linking test: 5,100 users on three servers, one cut off and rejoined 16 times, all agree after each
- 6 hour soak with 2,000 users, and fuzzing of every parser before each release
- Live checks against a real test network after every deploy

</td></tr>
</table>

The full list, with what is planned and what is only an idea, is in [docs/FEATURES.md](docs/FEATURES.md).

## 🗺️ Roadmap

| Version | What | Status |
| --- | --- | --- |
| 1.0 | Server and services, linking (BeeIRCd and UnrealIRCd 6), Hive Panel, BeeRoom web chat | ✅ Released |
| 1.1 | Spam filter, moving from Anope, opers by certificate, a calmer "What happened" page | ✅ Released |

## 📦 Installing

```sh
curl -fsSL https://beeircd.org/install.sh | sudo sh
```

Nine questions, then it runs. [docs/INSTALL.md](docs/INSTALL.md) has the details
(firewall, certificates, proxies); [docs/ADMIN.md](docs/ADMIN.md) covers running it
(`beeircd status`, AdminServ, the Hive Panel, linking servers, backups,
updates, every setting).

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

## 📂 Layout

| Folder | What lives there |
| --- | --- |
| `cmd/beeircd` | the `beeircd` program and its commands |
| `docs/` | installing, running and the feature list |
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
