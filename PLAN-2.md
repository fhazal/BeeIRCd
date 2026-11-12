# PLAN 2 - server linking (Stage 2)

Status: APPROVED 12 Nov 2026 with all recommendations; L1 changed on the
owner's choice (after advice) from an own protocol to the UnrealIRCd 6
server protocol, with the InspIRCd-style rename only between BeeIRCd
servers (L5). Each sub-step still asks "build 2x?" first.
2a built 12 Nov 2026 on branch stage2 (not merged: main stays the v1.0
rc line). Changes found while building: channel names must be valid UTF-8
(UnrealIRCd refuses links whose allowed-channelchars differ; utf8 is its
default); link password is plain text in the config, optional, because it
has to be sent (UnrealIRCd's spkifp links send `PASS :*`) - the pinned
fingerprint is the real lock (owner agreed 12 Nov: fingerprint-only is the
default `link-info` prints; `beeircd check` warns about a world-readable
config holding a link password and a link key others can read); `[link]
address` added.
2b built 12 Nov 2026 (branch stage2): burst both ways, collisions, TS
merge, netjoin batch, split quits; tested against a real UnrealIRCd 6.2.7.
Moved: network bans as TKL go to 2d (they are OperServ data on the hub).
Until 2c/2d services act on remote users but those changes stay on the
server that made them, and every BeeIRCd still runs its own services.
2c built 12 Nov 2026 (branch stage2), with the owner's 2c decisions:
casemapping `ascii` by default network-wide (`rfc1459` a setting, stored
names move at start, links refused when it differs; UnrealIRCd = ascii);
UnrealIRCd-compatible cloaks (`[cloak] method = "unreal"` + its three
keys, NETINFO checksum the same as UnrealIRCd's); network bans moved
back INTO 2c (AKILL/ZLINE/SHUN/ELINE = TKL G/Z/s/E, burst + live, each
server enforces); one services server per network (`[services] enabled`,
refused when both run, or when an UnrealIRCd network brings its own
NickServ). Live: NICK QUIT PART KICK MODE UMODE2/SVS2MODE SVSLOGIN
SETHOST/CHGHOST CHGIDENT SETNAME/CHGNAME INVITE SVSNICK, PRIVMSG/NOTICE/
TAGMSG routed to the target's server, numerics routed, KILL both ways,
three-server trees; services on the hub serve leaf users (replies,
numerics, +r, account, kills). Tested against a real UnrealIRCd 6.2.7.
Left for 2d: SVSJOIN/SVSPART (services joining people elsewhere, AJOIN),
SASL over the link, remote WHOIS (idle time), SQLINE/SNLINE network-wide.

Stage 2 links BeeIRCd servers into one network: a hub with leaves (and
later hubs under hubs), one set of services on the hub, splits and rejoins
that settle themselves. It ships as **v1.1** after v1.0 is tagged, so the
single-server release is not held up.

Sources: SPEC.md §4 (built ready for linking), ASSESSMENT.md (prerequisites
done in 1.5.2: event layer, services cache, ranks), the TS6 protocol notes
(Charybdis/Solanum), InspIRCd 4 spanningtree, UnrealIRCd 6 server protocol,
ConferenceRoom 2.1 link behaviour (hub-only services), IRCv3 batch
`netsplit` / `netjoin`.

What we already have: every server has a SID, every user a UID (SID + 6),
nicks and channels carry timestamps, all changes go through `emit()` and
`state.Event`, sinks are where a link plugs in, `[link] role/port` is in
the config, the installer opens the link port.

## Decisions (recommendation first)

| # | Question | Recommendation | Why |
|---|---|---|---|
| L1 | Wire format | **UnrealIRCd 6 server protocol** (PROTOCTL, SERVER/SID, UID, SJOIN, MODE, MD, EOS, SQUIT ...) as the base; BeeIRCd-only extras switched on with our own PROTOCTL token (`BEE=1`) only when both sides send it | Our modes and prefixes already use Unreal letters, so it fits with the fewest translations; Anope's existing UnrealIRCd module can link to BeeIRCd (owner wants Anope as an option later); real UnrealIRCd servers can link later; documented and proven. InspIRCd's protocol rejected: its strict module-match rule (CAPAB) and different mode letters don't fit BeeIRCd. Cost: ~20-30% more work in 2a/2b; we follow Unreal 6 protocol changes (rare, flagged by PROTOCTL tokens). |
| L2 | Link security | **TLS only + link password (argon2id hash stored) + certificate fingerprint pinned per server** | A leaked password alone can't link a rogue server; fingerprint pinning works with self-signed certs too. Plain links: never. |
| L3 | Topology | **Tree: hub-hub and hub-leaf allowed, a leaf has one uplink**; loops refused at handshake (SID/name already known = drop the newer link) | Classic and simple; mesh routing adds a lot for small networks. |
| L4 | Where link blocks live | **`[[link.server]]` blocks in beeircd.conf**; OperServ `SERVER ADD/SET/DEL` (SPEC §2.5) writes them through the same config editor AdminServ uses; REHASH picks them up | One source of truth, the Hive Panel edits the same file later. |
| L5 | Nick collision | **Between BeeIRCd servers (both sent `BEE=1`): the loser is renamed to its UID (InspIRCd 3+ style), nobody is killed**; older TS wins; same TS = both renamed; an identified user wins over a non-identified one with a newer TS. **On a link with a real UnrealIRCd server: UnrealIRCd's own collision rule** (to be confirmed from its docs/source in 2a), so both sides always agree who is online | No kills on netjoin is the modern behaviour; mixing rules with a foreign server would desync the network. |
| L6 | Channel collision | **Classic TS merge**: older TS keeps its modes, lists and ranks; the younger side loses ops/voices and modes (deop noticed to the channel); equal TS = merge both | Stops split-riding ops; what every TS ircd does. Registered channels: ChanServ re-applies its modes after the merge anyway. |
| L7 | Services | **Hub only (decided 11 Nov)**. Leaves see services as users on a `services.<network>` pseudo-server behind the hub; commands are plain PRIVMSG routed to the hub. Hub down = services down, NickServ prompts paused, no enforcement on leaves | Keeps one database, no sync conflicts. Services on another server can come later. |
| L8 | Splits users see | **Real server names in quit for everyone (`hub.net leaf.net`)**, with an option `hide_split = true` that shows `*.net *.split` to non-opers. IRCv3 `netsplit` / `netjoin` batches for clients that ask | Default honest, CR/Unreal-like; hiding is one switch. |
| L9 | Bans across the network | AKILL, SQLINE/SGLINE, DNSBL results and ZLINE from OperServ = **network-wide** (sent with the burst and live); new **K-line = this server only**. JUPE, NOOP and DEFCON (deferred in 1.5.5) built here | Matches Anope/Unreal behaviour; local K-line is what FEATURES.md promised. |
| L10 | Channel history | **Each server keeps history for channels that have local members**; messages reach only servers with members (TS6 rule). After a split a server may miss what was said on the other side; no back-fill in v1.1 | Back-fill means history sync = lots of code for a rare gap. Can be added later with message IDs (we already have msgid). |
| L11 | Version mixing | Linked servers must share `BEE=` protocol major; minor differences negotiated with capability flags; a server with an older major gets `ERROR :protocol too old` and a snotice | Lets `beeircd update` roll through a network one server at a time. |
| L12 | Oper commands | `/CONNECT`, `/SQUIT`, `/LINKS`, `/MAP`, `STATS l/c`, remote `WHOIS nick nick` (idle from the user's server), `/VERSION server`, `/ADMIN server`, remote `/MOTD`; oper flag `c` (and operclass `link`) | What opers on Unreal/Insp expect; flag `c` is already in SPEC §2.6. |

## Sub-steps (each: build, check.sh + its tests, ask to commit)

### 2a - link transport and handshake
- `internal/link`: listener on `[link] port` (TLS only), outgoing connect
  with autoconnect + backoff (30 s .. 5 min), PING/PONG with lag, send
  queue limits (a slow link is dropped with a snotice, never blocks the loop).
- Handshake: `PASS` (hash check) + `PROTOCTL BEE=1 caps` + `SERVER name SID
  desc`, fingerprint check, loop/duplicate check. Every refusal says why in
  ERROR + snotice + OperServ log line.
- Config `[[link.server]]`: name, host, port, password hash, fingerprint,
  autoconnect, class (sendq). `beeircd check` validates them, `beeircd
  status` gets a Links row.
- Unreal 6 handshake exactly as UnrealIRCd sends it (PASS, PROTOCTL tokens
  incl. SID/EAUTH/NOQUIT/NICKv2/SJOIN/SJ3/VL/UMODE2/TKLEXT2/MLOCK/NEXTBANS,
  SERVER, then the burst); `BEE=1` token for our extras. Unknown commands
  and MD keys from a peer are ignored safely and logged once.
- Tests: handshake ok, wrong password, wrong fingerprint, duplicate SID,
  loop, timeout, slow link dropped, recorded UnrealIRCd 6 handshake replayed.

### 2b - burst and state sync
- On link: servers (SID tree), users (`UID` with TS, modes, account, cloak,
  real host for opers), channels (`SJOIN` with TS, modes, lists, ranks),
  topics, away, network bans, then `EOB`. IRCv3 `netjoin` batch to local
  clients until EOB.
- Burst in Unreal 6 form: UID, SJOIN (SJ3, lists inside), TOPIC, network
  bans as TKL, MD for account/cloak/certfp, then EOS.
- L5 nick collisions (rename between BeeIRCd, Unreal rule with Unreal),
  L6 channel merge. Unit tests on `state` for every
  collision case; property test: two random networks merged in either order
  end up equal.

### 2c - live propagation
- Sink sends each `state.Event` to the right links: channel events only to
  servers with members, user events everywhere, PRIVMSG/NOTICE routed to
  the target's server. Incoming events go through the same `emit()` path,
  so local delivery code doesn't change.
- KILL, KICK, MODE, TOPIC, INVITE, AWAY, SETNAME, CHGHOST/cloak changes,
  oper up, account login/logout, MONITOR across servers, WHO/WHOIS/LIST/
  NAMES/LUSERS counting remote users.
- Flood/fakelag stays per local client; remote users are trusted to their
  own server.

### 2d - services over the link
- `services.<network>` pseudo-server on the hub; NickServ/ChanServ/... as
  its users. Leaves route service PRIVMSG + SASL (AUTHENTICATE relayed to
  the hub, answer relayed back) to the hub.
- Hub sets +r / account / vhost / channel modes on remote users through
  normal events. Enforcement (guest rename after 30 s) works for remote
  users. Hub down: leaves tell users "Services are unavailable" on service
  commands and SASL fails with 904 + a clear note.
- OperServ: STATS UPLINK, JUPE, NOOP, DEFCON, SERVER ADD/SET/LINK/UNLINK/
  DEL/LIST (writes config, L4). Log channel shows link events.

### 2e - splits and rejoins
- SQUIT / lost link: users behind it quit with L8 reason, `netsplit`
  batch; empty channels vanish; timed bans and history keep working.
- Rejoin: burst again, L5/L6 settle. Network bans burst both ways (newest
  wins per mask).
- `/CONNECT`, `/SQUIT`, `/LINKS`, `/MAP`, STATS l/c, remote WHOIS/
  VERSION/ADMIN/MOTD (L12). K-line (local) + `beeircd status` link lines.

### 2f - installer, docs, test network
- Installer: for hub/leaf ask the uplink (leaf) and print the block the
  other side needs (name, fingerprint, password) so linking two servers is
  copy and paste. `beeircd link-info` prints this server's block again.
- ADMIN.md "Linking servers", INSTALL.md, FEATURES.md rows, TESTING.md.
- **3-server test network on the homelab** (hub + 2 leaves in containers,
  one on its own network namespace to cut links with `tc`/iptables), with
  livecheck extended to multi-server (users on different servers in one
  channel, services from a leaf, split + rejoin with collisions).

### 2g - gates for v1.1
- race x3, fuzz the link parser (new fuzz targets: burst, SJOIN, UID),
  bench: 3 servers 5k users with a split every 2 minutes for 1 h (state
  equal on all servers after each rejoin = hash check), 24 h soak of the
  3-server net, irctest on each server, security review of the link code
  (rogue server tests: oversized burst, unknown SID, spoofed source).
- **UnrealIRCd 6 interop test**: a real UnrealIRCd 6 (same version as the
  owner's network) linked into the homelab test net: users on both sides
  in one channel, modes/bans/topics both ways, split + rejoin, collisions
  under Unreal's rule. "Compatible with UnrealIRCd" is only claimed after
  this passes. Anope 2.0 linking to BeeIRCd via its UnrealIRCd module is a
  smoke test here (full external-services option comes later).

## Not in Stage 2 (later, on purpose)
- Official support for UnrealIRCd links and the "use Anope instead of
  built-in services" option (protocol is ready after 2g; making it a
  supported feature waits until BeeIRCd is finished, Anope first, per the
  owner's note 11 Nov). InspIRCd links: planned later as a second
  protocol module (owner's vision 12 Nov: BeeIRCd can switch its services
  off for Anope and link with UnrealIRCd and InspIRCd). So the wire format
  stays behind one interface in internal/link: the network logic never
  depends on Unreal's command names.
- Services on more than one server, history back-fill after splits, mesh
  routing, encrypted DM history (BeeRoom).

## Progress estimate
v1.0 ~99% (rc1 after the soak). Stage 2 = about 8% of the full vision;
after 2g vision ~58%.
