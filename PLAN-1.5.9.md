# PLAN 1.5.9 - channel modes, user modes and the SA*/SET* commands

Status: 1.5.9a built (b9f2c97), 1.5.9b built 12 Nov 2026; 1.5.9c (+H history, CHATHISTORY) built 12 Nov 2026 (history.db is one encrypted file, not SQLite).

Sources studied: InspIRCd 4.12.1 modules (mode letters), UnrealIRCd 6.2.7
chanmodes (letters, +f/+F flood profiles, +L rules), the user's live Unreal
config (modes-on-join +nt, anti-flood block, +H history kept on disk and
encrypted), Anope 2.0.20 (MODE LOCK must know every new mode).

Today BeeIRCd has `CHANMODES=beI,k,l,MORimnprst` and user modes `i w o s x r`.

## The one big question first (D1): whose letters?

The old roadmap line said "InspIRCd-style modes". Looking at both servers,
they agree on almost every letter (c C N T K S z Q P D O R M L H G). They
disagree on a few, and the flood modes are the big one:

| What | InspIRCd 4 | UnrealIRCd 6 |
|---|---|---|
| Message flood | +f `5:10` | +f `[5m,3j,2n,4k,..]:60` (one mode for every flood kind, each with its own action) |
| Join flood | +j | inside +f (`j`) |
| Nick flood | +F | inside +f (`n`) |
| Flood profiles | none | +F `normal`/`strict`/... and a server default for ALL channels |
| No invites | +A is the opposite (allow invite) | +V |
| Hide my channels (user) | +I | +p |
| TLS-only PMs (user) | +z | +Z |

**Recommendation: Unreal letters where the two disagree.** BeeNet's users,
bots and scripts come from Unreal, so they already know `+f [..]:60` and
`+F normal`. One flood mode with profiles is also calmer (zen): most owners
never touch it, the server default protects every channel. InspIRCd-only modes
with no Unreal letter (+E repeat, +B caps, +J rejoin delay) go on top, since
their letters are free in Unreal.

## 1.5.9a - channel modes

| Mode | Does | Notes |
|---|---|---|
| +f `[5j,10m,3n,5k,4t]:60` | flood guard: joins, messages, nick changes, knocks, CTCP... in N seconds; action per kind (+i, +m, +N, +K, kick, timed ban) | Unreal syntax and actions; actions undo themselves after the time |
| +F `<profile>` | flood profile (very-strict, strict, normal, relaxed, very-relaxed, off) | **D2** server default profile |
| +c / +S | block / strip colours | |
| +C | no CTCP (ACTION allowed) | |
| +T | no channel NOTICEs | |
| +N | no nick changes while in the channel | |
| +K | no KNOCK | |
| +V | no INVITE (ops still can) | |
| +Q | no KICK (services + opers excepted) | |
| +z / +Z | TLS users only / shown when everyone is on TLS | |
| +D | delay join: people show up when they first speak | |
| +P | permanent: channel stays when empty (opers) | ChanServ PERSIST uses it |
| +L `#other` | send people on when they can't join | Unreal rules: full (+l), +i, +k wrong, banned, +R, +z. Makes **CS SET OVERFLOW** work (moved here from 1.5.4) |
| +E `lines:secs` | block repeated lines | InspIRCd repeat, simple form |
| +B `percent:minlen` | block shouting (caps) | InspIRCd anticaps, simple form |
| +J `secs` | no rejoin for N seconds after a kick | InspIRCd kicknorejoin |
| +G | censor words from a server list | **D4** maybe later |

Also in 1.5.9a: `~f:#chan` forward extban? (Unreal; small, goes with +L), new
modes in 004/005 CHANMODES, ChanServ MODE LOCK / MLOCK / CLEAR know them,
HELP CMODES (Anope has no such help; short table like Unreal's `/helpop chmodes`).

**D3 - who is exempt from the content modes (+c +S +C +T +E +B and the
message part of +f)?** Recommend: halfop and up (voice is not exempt), plus
opers with flag g. An `+e ~m:` style per-mode exemption list waits.

## 1.5.9b - user modes and commands

User modes (Unreal letters):

| Mode | Does |
|---|---|
| +B | bot (WHOIS 335, `bot` message tag, IRCv3 BOT= in 005) |
| +d | deaf: no channel messages |
| +D | private deaf: no PMs except from opers/services |
| +R | PMs only from registered (identified) users |
| +T | no CTCPs to me |
| +p | hide my channels in WHOIS |
| +H | hide that I'm an oper (oper only) |
| +W | tell me when someone WHOISes me (oper only) |
| +z | connected with TLS (set by the server, can't be changed) |
| +Z | PMs only to/from TLS users |
| +q | can't be kicked (oper flag a only) |

Commands:
- **KNOCK #chan [reason]**: ask a +i/+k/+l channel to let you in (rate limited,
  blocked by +K, counted by +f `k`).
- **SILENCE +mask / -mask / list** (server-side ignore, 005 SILENCE=32).
- **SETNAME** + IRCv3 `setname` cap; **CHGNAME** for opers.
- Opers: **SETHOST / SETIDENT / CHGHOST / CHGIDENT** (flag h; show up as chghost).
- Opers: **SAJOIN / SAPART / SANICK / SAMODE / SATOPIC** (**D5** flag).
- SA* and CHG* are logged to the log channel + server notices, like Unreal
  (not AS HISTORY: that list is for config changes only).

## 1.5.9c - channel history (+H) and CHATHISTORY

The user's Unreal keeps +H history across restarts, encrypted on disk, read
by IRCv3 chathistory. BeeIRCd should match:

- +H `lines:time` (Unreal: `+H 50:1d`), playback on join for clients
  without chathistory, IRCv3 `draft/chathistory` (BEFORE/AFTER/LATEST/AROUND/
  BETWEEN/TARGETS) for clients with it.
- **D6 - storage:** recommend memory with a per-channel cap, written to a
  separate `history.db` (SQLite, encrypted with a key from beeircd.conf, like
  Unreal's db-secret) so a restart doesn't lose it. Off = memory only.
- Small-VPS cap: server-wide limit (default 32 MB of history) so 1 CPU /
  512 MB stays safe; oldest lines dropped first.

## Small VPS

- Flood counters only exist while someone in the channel is actually
  talking/joining; idle channels cost nothing even with the default +F.
- Bench alarm per sub-step (bench2.sh, compare 5k ~115 MB, chat ~55%,
  lag ~5 ms) plus a new load-test phase with +F normal on every channel.

## Tests and gates (each sub-step)

check.sh, -count=6, Linux race -count=3, load gate, bench2 alarm, deploy
(ask first), live159a/b/c scripts, then ask to commit.

## Decisions for the user

| # | Question | Recommendation |
|---|---|---|
| D1 | Unreal or InspIRCd letters where they disagree | **Unreal** (+f/+F profiles, +V, user +p/+Z); InspIRCd-only +E +B +J on top |
| D2 | Server default flood profile on every channel | **Yes, "normal"** (like Unreal 6.2), AdminServ SET to change/off |
| D3 | Who skips +c +S +C +T +E +B and the message flood | **Halfop and up + opers (g)** |
| D4 | +G censor with a server word list | **Later** (needs a word list + AdminServ editor) |
| D5 | Oper flag for SAJOIN/SAPART/SANICK/SAMODE/SATOPIC | **a (admin)**, like Unreal's admin/netadmin classes |
| D6 | +H history kept on disk, encrypted | **Yes**, separate history.db, 32 MB cap |
| D7 | Split | **1.5.9a channel modes -> 1.5.9b user modes + commands -> 1.5.9c history**, each committed on its own |
