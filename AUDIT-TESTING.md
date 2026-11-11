# BeeIRCd — repository, architecture and testing audit

Date: 11 Nov 2026. Repository state: `main` at `b9f2c97` (Step 1.5.9a), clean
except the untracked `mockups/mascot/` (left alone). Test server on the homelab
runs the same build (`BeeIRCd 0.1.0-dev (1.5.9a)`, deployed 18:50 MYT, log clean).
This report changes no production code; nothing is committed.

Status words in this report: **PASS**, **FAIL**, **NOT IMPLEMENTED**, **NOT
TESTED**, **BLOCKED**. "Verified" means I read the code or ran it, not the docs.

---

## 1. Summary

1. **The foundation is better than average for this stage.** There's one event loop
   owning all state, every network change goes through `state.Event`, UIDs and
   timestamps exist, and 175 tests drive the real protocol over TCP. Statement
   coverage is **80.9%** across `internal/...` (measured). The weak spot is not
   *how much* is tested but *who wrote the expectations*: the same hand wrote
   the code and the tests, and nothing independent checks protocol conformance.
2. **Two confirmed security defects** (found by probing during this audit):
   - **S1 (high): line injection.** A bare `\r` and a NUL byte inside a line are
     relayed to other clients unchanged. Probe result:
     `:pa!~pa@127.0.0.1 PRIVMSG #p :hello\r:evil!x@y PRIVMSG #p :forged\x00nul`.
     Clients that split on `\r` show a forged line from `evil`.
   - **S2 (medium): oper-only modes through ChanServ.** A channel founder runs
     `/cs mode #chan set +PO` and gets `+OPnrt`. These are oper-only modes,
     refused on direct MODE (481).
3. **One resource gap:** the unregistered-connection count has no limit and there is no
   connect-rate throttle (only 3 per IP, a 60 s register timeout, and `max_users` checked
   at *registration*). This is the classic many-IP connection flood.
4. **No CI, no fuzzing, no independent conformance suite, and the live
   test scripts and benchmark script are outside the repository.** These are the
   four highest-value testing gaps.
5. **Architecture vs the long-term vision.** It's ready for the next steps and for
   Stage 2 design. Five items need decisions before Stage 2 (§4.3), none before 1.5.9b.
   **Anope and InspIRCd interop turn out to be the same piece of work**: an
   InspIRCd-protocol link adapter (§4.4).

---

## 2. Phase A — what exists (verified)

### 2.1 Repository

| Area | Contents | Notes |
| --- | --- | --- |
| `cmd/beeircd` | CLI (`run`, `version`, …) | 228 lines |
| `internal/irc` | parser, builder, folding, masks, numerics | 540 lines, 11 tests |
| `internal/state` | network state + events (`Apply`, `Clone`, `CopyEvent`) | 1,125 lines, 9 tests |
| `internal/server` | connections, loop, commands, modes, opers, AdminServ, DNSBL | 6,794 lines, 119 tests |
| `internal/services` | NickServ, ChanServ, MemoServ, OperServ, HostServ, Global, log channel | 11,011 lines, 4 own tests (covered mainly through `server` tests) |
| `internal/store` | SQLite (modernc, no cgo), write-through cache | 3,396 lines, 18 tests, 1 benchmark |
| `internal/config` | TOML, defaults, checks, in-place editor (AS SET) | 1,018 lines, 7 tests |
| `internal/passwd` | argon2id (m=19 MiB, t=2, p=1), constant-time compare | |
| `internal/tlsutil`, `cloak`, `version` | TLS 1.2+, HMAC cloaks | |
| `tools/beeload` | load bots (connect rush, chat, watchers) | used by bench |
| `scripts/check.sh` | gofmt, vet, staticcheck, tests (race only where gcc exists) | |
| `deploy/homelab-test` | Dockerfile (scratch, uid 65534), sample conf, MOTD | |
| Docs | SPEC, FEATURES, ENGINEERING, ASSESSMENT, ANOPE-ALIGNMENT, PLAN-1.5.4…1.5.9, PLAN-anope-* | no DEVELOPMENT.md, no TESTING.md |
| CI | **none** (no `.github/`) | |

### 2.2 Tests today

| Layer | What exists | Evidence |
| --- | --- | --- |
| Unit | parser, masks, folding, ranks, extbans, store, config editor, passwd, cloak | `internal/*/…_test.go` |
| Protocol integration (in-process) | real TCP clients against an isolated server: temp dir, `127.0.0.1:0` ports, temp SQLite; helpers `login`, `registered`, `founded`, `boss`, `expect`, `quiet`, `sync` | `internal/server/*_test.go`, 119 tests |
| Load gate | 5,000 clients, loop-time percentiles (`BEEIRCD_LOAD=1`) | `load_test.go`; **not** run by `check.sh` |
| Race | Linux `go test -race -count=3` in `golang:1.27` on the homelab, by hand | HANDOFF; no automation |
| Small-VPS bench | `bench2.sh bee|insp`, 1 core + 512 MB, vs InspIRCd 4.12.1 | **on the homelab only** (`/opt/beeircd-test/bench/`), not in the repo |
| Live checks | Python scripts `live155…live159a.py` against the test server | **scratchpad + homelab only**, not in the repo |
| Fuzz | **none** (0 `Fuzz*` functions) | |
| Independent conformance | **none** (irctest proposed in ASSESSMENT.md, never set up) | |
| Goroutine-leak / soak | **none** | |

Coverage (measured, `-coverpkg=./internal/...`): **80.9%**. Lowest files:
`state/event.go` 31.5% (mostly the empty `event()` markers, but `CopyEvent` paths
too), `server/server.go` 64.1% (real DNS lookup, `validHostname`, `trimDot` at
0%), `adminserv158.go` 72.5%. Uncovered functions that matter: `lookupHost` /
`validHostname` (forward-confirmed reverse DNS, security-relevant),
`dbErr` in server and services (no test makes the database fail), `cmdPong`,
`cmdPass`, `cmdTime`.

### 2.3 Discrepancies between docs and code

| # | Where | Says | Actually | Fix |
| --- | --- | --- | --- | --- |
| D1 | FEATURES.md IRCv3 table | "sasl (PLAIN) Partial (EXTERNAL, SCRAM planned)" | EXTERNAL is implemented (`sasl=PLAIN,EXTERNAL`, `register.go:31`, `sasl.go:43`); FEATURES' own services table says so | change the row |
| D2 | ENGINEERING.md | bench at `/tmp/bench2.sh` | moved to `/opt/beeircd-test/bench/` | update the path |
| D3 | ENGINEERING.md | "the load test in `check.sh`" | `check.sh` doesn't run it (it needs `BEEIRCD_LOAD=1`) | add it, or change the text |
| D4 | ENGINEERING.md | 5k "99 MB", "chat 54%" | now 112–116 MB, 53–55% (1.5.7–1.5.9a) | refresh the table at stage end |
| D5 | `check.sh` comment | "run it where a C compiler exists (Linux CI…)" | there is no CI | |
| D6 | ASSESSMENT.md roadmap | step 7 as phase 2, before the foundations | decided 11-11: Stage 1.5 → linking → Hive Panel → BeeRoom; step 7 after 1.5.9 | mark it superseded |
| D7 | ASSESSMENT.md | "About 20 of the 34 IRCv3 specs" | MONITOR, SASL EXTERNAL and account-extban have landed since | refresh |
| D8 | SPEC.md | "draft 2", "Stage 1 plan" | Stage 1.5 is nearly done | mark historical or update |
| D9 | Your brief, Part 2 | 1.5.9b = "SETHOST and CHGHOST" | PLAN-1.5.9.md also lists SETIDENT, CHGIDENT, CHGNAME, SATOPIC, the `setname` cap, SILENCE=32 | **please confirm the plan's wider list is the approved scope** |
| D10 | Your brief | DEVELOPMENT.md | doesn't exist | propose TESTING.md (below) |
| D11 | 1.5.8 | oper side "done" | unit tests only; not tried live as Belon | still your manual test |
| D12 | `dist/beeircd-linux-sym` | — | leftover symbols build (git-ignored) | can be deleted |

---

## 3. Phase B — testing matrix

Priority: **C** critical, **H** high, **M** medium, **L** low. Env: **I** isolated
in-process / homelab instance with its own ports and data; **L** live test server
(non-destructive, own `zt*` nicks/channels only).

### A. Core protocol

| Subsystem | Functionality | Impl | Method | Existing coverage | Missing coverage | Pri | Env | Expected |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Registration | NICK/USER/CAP/PASS order, 001–005, timeouts | Existing | integ | TestRegistration, BeforeRegistration, RegistrationTimeout | PASS ignored (by design) not asserted; CAP END without USER | H | I | 001–005, 451 before reg, timeout closes |
| Parser | tags, source, params, trailing, spaces | Existing | unit+fuzz | TestParse/RoundTrip/String | **fuzz**; independent cases (irctest) | C | I | never panics; round-trips |
| Input hygiene | bare CR, LF, NUL in a line | Existing (defect) | integ | none | **S1: FAIL today** | C | I | CR/NUL rejected or stripped, never relayed |
| Line limits | 512 body, 8191 tags, 417 | Existing | integ | TestLongLines, ReadLineSizes | tag-limit edge, multi-byte cut | H | I | 417 / truncation, no split UTF-8 |
| PING/PONG, timeouts | ping, pong, ping timeout | Existing | integ | TestPingTimeout | `cmdPong` 0% | M | I | |
| Numerics | target, param counts per numeric | Existing | integ | substring checks | **parsed checks** (count, target, trailing) | H | I | RFC/Modern-docs shapes |
| Unknown / malformed | 421, empty, only tags, only source | Existing | integ+fuzz | partial | fuzz + table | H | I | 421 / silently ignored, no crash |
| Many clients | concurrent connect/join/talk | Existing | stress | load test 5,000 | soak | H | I | loop 99% < 5 ms |
| UTF-8 | invalid UTF-8 relayed | Existing (passes bytes) | integ | none | decide policy (UTF8ONLY later) | M | I | documented behaviour |

### B. Channels

| Subsystem | Functionality | Impl | Method | Existing coverage | Missing | Pri | Env | Expected |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Lifecycle | create, +nt, op on create, destroy on empty, +P keeps | Existing | integ | ChannelBasics, PermanentChannel | +P with ChanServ PERSIST together | H | I | |
| Ranks | ~&@%+, who sets what, protection | Existing | integ | ranks_test (4) | full rank×mode matrix (table test) | H | I | Unreal rules |
| KICK/INVITE/TOPIC/NAMES/LIST | | Existing | integ | several | LIST with +D, +s/+p for opers | M | I | |
| Lists | b e I, extbans ~a ~r ~m ~t, list full 478 | Existing | unit+integ | Bans…, NormalizeBan, extban_test | fuzz NormalizeBan | H | I | |
| 1.5.9a modes | c S C T N K V Q z Z D P L E B J f F | Existing | integ+live | 10 tests, live159a 25/25 | **S2: FAIL** (+O/+P via ChanServ); +D with WHO/WHOIS from opers; +f k (needs KNOCK) | C | I+L | refusals 404/447/470/489/495/518/972 |
| Mode parsing | MODES=4 split, param validation 696 | Existing | unit+integ | ParamModes | fuzz parseFlood / mode strings | H | I | |
| Concurrency | simultaneous MODE/KICK/JOIN | Existing | race | race -count=3 | scripted conflicting ops | M | I | consistent final state |

### C. User modes and opers

| Subsystem | Functionality | Impl | Method | Existing | Missing | Pri | Env | Expected |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| User modes | i w o s r x | Existing | integ | partial | table of who may set each | M | I | |
| 1.5.9b umodes | B d D R T p H W z Z q | **Not implemented** | — | — | write with 1.5.9b | H | I | |
| /OPER | password, host, CIDR, admin_hosts, lockout, DISABLE | Existing | integ | opers158, operserv | **live as Belon (D11)** | C | I+L | 381 / 491 / 464, lockout after 5 |
| Oper classes | flags a c g h k s, AS CLASS, HISTORY/DIFF/UNDO | Existing | integ | TestOperClasses etc. | negative matrix: every oper command × missing flag | C | I | 481 / "You need oper flag x" |
| KNOCK, SILENCE, SETNAME, SETHOST/CHG*, SA* | | **Not implemented** | — | — | with 1.5.9b; SA* without flag a | H | I | |
| Escalation | services acting for users (S2 class) | Existing | integ | none | audit every `ApplyChannelModes`/`SetUserModes`/`ForceJoin` caller | C | I | services never grant more than the asker may |

### D. IRCv3

| Spec | Impl | Existing | Missing | Pri |
| --- | --- | --- | --- | --- |
| CAP 302, cap-notify | Existing | TestCapNegotiation | irctest cap suite | H |
| SASL PLAIN, EXTERNAL | Existing | ns156, sasl tests | irctest sasl, abort/too-long cases | H |
| message-tags, server-time, echo-message, labeled-response, batch | Existing | caps_test | irctest; interactions (labeled + batch + echo) | H |
| account-notify/tag, extended-join, away-notify, chghost, invite-notify, multi-prefix, userhost-in-names | Existing | caps_test | irctest | M |
| MONITOR | Existing | ns156 | irctest monitor | M |
| setname, bot | **Not implemented** (1.5.9b) | — | — | H |
| chathistory | **Not implemented** (1.5.9c) | — | — | H |
| standard-replies, WHOX, extended-monitor, UTF8ONLY, no-implicit-names, STS, WebSocket | **Not implemented** | — | — | M/L |

### E. Security

| Area | Impl | Existing | Missing | Pri | Env |
| --- | --- | --- | --- | --- | --- |
| Line injection (CR/NUL) | defect S1 | none | fix + regression test | C | I |
| Privilege escalation via services | defect S2 | none | fix + caller audit test | C | I |
| Connection exhaustion | per-IP 3, register timeout 60 s | none | unregistered cap + connect throttle (not built); test many-IP flood | H | I |
| Flood | fake lag, +f/+F | FloodIsSlowedNotKilled, FloodGuard | sendq overflow disconnect test | H | I |
| Passwords | argon2id, hashSem=2, constant-time, lockouts | passwd, services tests | timing of wrong user vs wrong pw (no account enumeration) | H | I |
| TLS | 1.2+, client certs | TestTLS | testssl.sh scan of an isolated instance | M | I |
| rDNS spoofing | forward-confirmed lookup | **0% coverage** | fake-resolver test | H | I |
| Info disclosure | +s/+p, +D, hidden opers, real host | partial | matrix test per query command | H | I |
| Fuzz | none | — | parser, NormalizeBan, parseFlood, mlock, config, SASL base64 | C | I |
| Dependencies | — | — | `govulncheck` (official Go tool) | M | I |
| Restart / DIE | confirm codes | operserv tests | restart keeps data, re-exec same PID | M | I |

### F. Performance and reliability

| Area | Existing | Missing | Pri |
| --- | --- | --- | --- |
| Throughput / latency / connect rate | bench2 (5k, 1 CPU), load gate | bench script into repo; record baselines in ENGINEERING.md per stage | H |
| Large channel | partly (watchers 200) | one 5,000-member channel broadcast | M |
| Goroutine leaks | none | count goroutines after `stopServer` in tests | H |
| Long-running | none | 24 h soak at 1,000 bots on an isolated homelab instance, sample RSS/goroutines (`BEEIRCD_MEMSTATS`) | H |
| Shutdown / restart | ShutdownTellsEveryone, restart tests | restart under load | M |
| Disk stalls | none | SQLite writes run on the loop: measure loop time with a slow disk (fsync delay) | M |

### G–J. Later stages

| Area | Impl | When to test | Notes |
| --- | --- | --- | --- |
| Services ↔ IRCd boundary | in-process, `Host` interface (20 methods) | each services step | `services_test.go` already proves NS/CS for a user on another (fake) server |
| Service failover / sync across servers | **Not implemented** | Stage 2 | depends on decision A3 below |
| Server-to-server | **Not implemented** | Stage 2 | multi-instance harness (§5.6) |
| Cross-IRCd / Anope | **Not implemented** | after BeeIRCd 1.0 | §4.4 |
| Web panel / API | **Not implemented** | Stage 3 | RBAC matrix, CSRF, session, audit tests |

---

## 4. Phase C — architecture review

### 4.1 Strengths (keep)

- **One loop owns all state** (`server.go`, `events chan func()`); readers and
  writers are per-connection goroutines; blocking work (argon2, DNS, DNSBL, mail)
  goes off the loop and comes back with `post`. This is the same shape as
  InspIRCd's single-threaded core, and it makes ordering and consistency simple.
- **Event layer** (`state.Event`, 14 kinds incl. the new `Reveal`; `Apply` is the
  only writer; `Clone` and `CopyEvent` exist). This is the right base for links.
  I found no state writes outside `Apply` except building a user before `UserAdd`.
- **Services behind an interface** (`services.Host`), already tested against a fake
  host for a user on another server.
- **Isolated, real-TCP test harness** already exists and is fast (~55 s for 119 tests).
- **Performance discipline**: measured gates, small-VPS bench against InspIRCd.

### 4.2 Immediate concerns (worth fixing before or with 1.5.9b; your call)

| # | Problem | Evidence | Proposal |
| --- | --- | --- | --- |
| S1 | CR/NUL relayed (line injection) | probe above; `readLine` splits on `\n` only, `irc.Parse` keeps `\r` and `\0` | drop bare `\r` and `\0` from input lines (or reject the line), like InspIRCd/UnrealIRCd; add a regression test |
| S2 | founder sets +O/+P through ChanServ | probe: `+OPnrt`; `modes.go` treats `src.Service` as allowed | `ApplyChannelModes` on behalf of a user checks that user's oper status for O and P (services' own enforcement unchanged) |
| S3 | unregistered connections unlimited, no connect throttle | `max_users` checked in `register.go:241`, not at accept | cap unregistered connections and add a per-IP connect-rate throttle (UnrealIRCd `connthrottle`, InspIRCd `connectban`). **New scope: needs your approval** |
| T1 | live and bench scripts not versioned | scratchpad / homelab only | move them into the repo (§5) |

### 4.3 Before Stage 2 (decide during Stage 2 planning, no refactor now)

| # | Topic | Why it matters | Options |
| --- | --- | --- | --- |
| A1 | Oper state lives on `Client` (flags, class, title, swhois, vhost), not on `state.User` | remote servers must know who is an oper and with which privileges | move oper identity into state and an event (`OperUp`) |
| A2 | Network bans, forbids and news live in services data, not events | they must reach every server | ban events (like InspIRCd's ADDLINE/DELLINE) |
| A3 | One SQLite file with an exclusive lock | only one process can own services data | **services on one server** (ConferenceRoom / Anope model, simplest, a single point of failure that degrades gracefully) vs replicated services (complex). Recommend the first, with read-only fallback when the services server is split away |
| A4 | Link protocol | no spec yet | write a small native spec first (UID/SID, TS6-style collision rules, burst = `Clone` + events, capability negotiation, version field) |
| A5 | SQLite writes on the loop | a slow disk stalls everyone | measure first (test above); if needed, a write queue goroutine with ordering kept |

### 4.4 Long-term (honest feasibility)

- **External services (Anope) and cross-IRCd links are the same problem.** Anope
  links to an IRCd through that IRCd's server protocol. The realistic path is
  native BeeIRCd linking first, then **one adapter speaking InspIRCd's
  spanning-tree protocol**. That one adapter would let Anope (its `inspircd`
  protocol module) and real InspIRCd 4 servers link. An UnrealIRCd adapter would be a
  second, separate piece.
- **What won't translate cleanly** (because of D1 = Unreal letters):
  - Unreal `+f [..]:15` and `+F` profiles have no exact InspIRCd form (`+f`/`+j`/`+F` split, no profiles), so that translation is lossy.
  - +D/+d, +L rules and extbans differ in detail.
  - `Reveal` has no InspIRCd equivalent.

  The adapter must map these per mode and document what drops. This is technically
  achievable, but expect months, not weeks. A full mixed BeeIRCd/InspIRCd/Unreal network is the hardest
  part of the whole vision; native linking must not bend to it.
- **Web panel / API.** Use a separate management listener (localhost or unix socket by default),
  token auth mapped to oper classes (RBAC you already have), every write through
  the same code as AdminServ so AS HISTORY/UNDO stays the audit log.
  UnrealIRCd's JSON-RPC is the established reference. The panel is just a client of
  the API and never touches internals.
- **Extensibility.** Go plugins are fragile, so I don't recommend them. Recommend compiled-in modules
  behind small registration interfaces (commands, modes, caps, hooks) plus
  out-of-process extensions through the API, an event stream and webhooks.
- **Observability.** Add a metrics endpoint (Prometheus text format) on the
  management listener; loop stats already exist.
- **Scale.** The single loop is fine far past 5,000; the 50,000 target needs
  measuring before any sharding talk.

---

## 5. Phase D — testing infrastructure proposal

Principle: **one framework per layer, reuse what exists, no new runtime
dependencies in the server.**

1. **Unit tests: keep `go test`.** Add **Go native fuzzing** (no dependency) for
   `irc.Parse`, tags, `NormalizeBan`, `parseFlood`, `parseMlock`, config load,
   SASL decoding. Run each for a short time in `check.sh` and longer before releases.
2. **Protocol harness: extend the existing `tc` helpers, don't replace them.**
   - Add a **protocol-violation checker** on every line a test client reads: length,
     no bare CR/NUL, a valid prefix, numerics carrying the client's nick as first
     param. It is written independently of `internal/irc`, so it doesn't check
     the server against itself.
   - Add **parsed expectations** (`expectNumeric("473", "#chan")`) next to
     substring `expect`.
   - Add a **goroutine-leak check** when each test server stops.
   - Move the client side into a small shared package so the
     **live checks** (today's Python scripts) become a Go tool
     (`tools/livecheck`) using the same client. That gives one framework, versioned,
     and always non-destructive (own `zt*` names only).
3. **Independent conformance: irctest** (the suite Ergo, Solanum, InspIRCd and
   UnrealIRCd use). It's an external Python/pytest tool run in Docker on the
   homelab against an *isolated* BeeIRCd instance; it is not a Go dependency.
   It needs a small BeeIRCd controller file. The first run gives a
   PASS/FAIL/NOT IMPLEMENTED baseline; tests for features we don't have are
   marked NOT IMPLEMENTED, not failures. This directly answers "don't test the
   implementation's own assumptions".
4. **Compatibility clients.** Run scripted sessions with weechat/irssi in
   Docker, plus BChat as one client among them (never the framework).
5. **Stress and soak:**
   - Put `bench2.sh` in `scripts/` and keep the baselines table in ENGINEERING.md.
   - Run a 24 h soak on an isolated homelab instance (own ports/data, cleaned up after).
   - Add `govulncheck` and a `testssl.sh` scan to the pre-release list.
6. **Multi-server harness (Stage 2).**
   - N servers in one test process, linked through an in-test proxy that can delay,
     drop or cut links (split injection).
   - After every scenario, compare each server's `state.Clone()`: they must match.
     The event layer makes this unusually cheap.
7. **CI:**
   - GitHub Actions on push: `check.sh` + `-race` on Linux (~10 min).
   - The homelab keeps the heavy jobs (bench, soak, irctest).
   - Alternative: a self-hosted runner on the homelab (no minute limits on a private repo, more upkeep).
8. **TESTING.md.** One page with the layers, the commands and the result vocabulary
   (PASS/FAIL/NOT IMPLEMENTED/NOT TESTED/BLOCKED).

---

## 6. Phase E — execution roadmap

| Step | When | Work | Gate |
| --- | --- | --- | --- |
| T0 | now, before 1.5.9b (about one session) | fix S1 + S2 with regression tests (**your approval**); protocol-violation checker; fuzz targets; goroutine-leak check; bench + live scripts into the repo; TESTING.md; doc fixes D1–D8 | all current tests still pass; fuzz 5 min clean |
| T1 | with 1.5.9b and 1.5.9c | each feature gets unit + protocol + livecheck tests; irctest set up on the homelab, baseline recorded | no new irctest FAIL on implemented features |
| T2 | step 7 | CI live; S3 throttle (if approved); clean-VM install test; REHASH tests; TLS scan; govulncheck; first 24 h soak | CI green on every push; soak flat memory |
| T3 | Stage 2 | multi-server harness, split/rejoin, state-equality checker, link auth/TLS tests | 3-server homelab network, split tests |
| T4 | after 1.0 | Anope + InspIRCd via adapter, irctest across links | documented compatibility table |
| T5 | Stage 3 | API/panel: RBAC matrix, sessions, CSRF, fuzzing the API, audit trail | security review |

T0 doesn't delay 1.5.9b meaningfully, and it removes the two defects before more
code builds on them.

---

## 7. Phase F — decisions for you

| # | Question | My recommendation |
| --- | --- | --- |
| Q1 | Fix S1 (CR/NUL) and S2 (+O/+P via ChanServ) now, before 1.5.9b? | **Yes** (small, with regression tests) |
| Q2 | Connect throttle + unregistered-connection cap (S3): when? | **Step 7** with the install hardening (or a small step now if you prefer) |
| Q3 | Do T0 test infrastructure before 1.5.9b? | **Yes** |
| Q4 | irctest as the independent conformance suite (homelab, isolated)? | **Yes** |
| Q5 | Port the Python live scripts to a Go `tools/livecheck` (one framework)? | **Yes** |
| Q6 | CI: GitHub Actions or a homelab self-hosted runner? | **GitHub Actions** for check + race; homelab for heavy jobs |
| Q7 | 1.5.9b scope: is the plan's full list (incl. SETIDENT, CHGIDENT, CHGNAME, SATOPIC, setname cap) approved? | **Yes, as written in PLAN-1.5.9.md** |
| Q8 | Services topology for Stage 2 (A3) | decide at Stage 2 planning; leaning **services on one server** |

Nothing above is implemented. 1.5.9b is not started.

---

# Part 2 — results of the approved work (S1, S2, T0), 11 Nov 2026

Decisions taken by the owner: S1, S2 and T0 approved; irctest, Go livecheck
and GitHub Actions approved; S3 deferred to step 7; services topology deferred
to Stage 2; 1.5.9b scope = PLAN-1.5.9.md as written. Everything below is
**local and uncommitted**. The test server still runs 1.5.9a (`b9f2c97`) and
has not been redeployed.

## 8. Security fixes

### S1 — line framing (CR, LF, NUL)

- **Input** (`irc.SplitFraming`, used in `readLoop`):
  - CR, LF and CRLF all end a line, so a bare CR can't hide a second line;
  - empty lines are ignored;
  - a line holding a NUL is dropped;
  - fake-lag counts each line.
- **Output** (`irc.Message.String`, `Client.send`):
  - CR, LF and NUL in the source, command or parameters become spaces;
  - NUL is dropped from tag values;
  - a middle parameter can never be empty, hold a space or start with `:`, so it can't shift the parameters after it.
- **Regression tests:** `irc/framing_test.go` (3) and `server/framing_test.go` (5).
  **3 of them FAIL on the old code and PASS on the new** (checked by running
  them against the stashed old files). CRLF, bare LF, split TCP writes and
  empty lines keep working.

### S2 — services acting with more rights than the asker

| Path | Before | After | Test |
| --- | --- | --- | --- |
| `/cs mode #chan set +O/+P` (founder, not an oper) | `+OP` set | 481, nothing set; opers (direct, OperServ, or a founder who is an oper) still can | `TestOperModesThroughServices` |
| ChanServ mode lock with O or P | refused already | unchanged (tested) | same |
| NickServ AJOIN | its own copy of the JOIN rules, so **+z and +J were skipped** (added in 1.5.9a) | uses the server's own JOIN check (`host.JoinBlocked`); an invite only helps where it would for a JOIN (b, i, k, l, R) | `TestAjoinFollowsJoinRules` |
| `/cs mode #chan set` status modes | any MODE holder could set any status, deop the founder or deop services | follows Anope 2.0.20 `cs_mode`: OP/OPME, VOICE/VOICEME, HALFOP, PROTECT, OWNER privileges, PEACE, protected services, Anope's replies; opers with flag s override (like `chanserv/administration`) | `TestModeSetNeedsStatusPrivileges` |

The two S2 tests **FAIL on the old code and PASS on the new**. Other paths I
checked, with no problem found:
- OperServ MODE, UMODE and SVSJOIN are oper-only.
- KEEPMODES restore uses the user's own mode rules.
- CS INVITE doesn't get past +z or +O.
- The mode lock can't hold O or P.

### Additional finding O1 — command order during password checks (NOT fixed, needs a decision)

- **What happens:** password checks (OPER, NS IDENTIFY/REGISTER, SASL, CS commands
  with passwords) run off the main loop (argon2), and **the same client's later
  commands are handled before the result**.
- **Evidence:** irctest's `OPER` tests see the PONG before 381. A bot that sends
  `NS IDENTIFY` and then `JOIN` to a +R channel can be refused.
- **Proposed fix:** while a client has a password check in flight, keep its
  following lines in a small per-client queue and run them when the check
  returns. That keeps everyone else unblocked and this client's order intact.
- It changes how commands are dispatched, so per your rules it waits for your
  decision. irctest's `sync_sleep_time` knob would hide it; I didn't use it.

## 9. T0 testing foundation

| Item | State | Evidence |
| --- | --- | --- |
| Line check on every received line (`lineProblem`) | done | 0 violations across the whole suite |
| `expectNumeric` helper | done | used in framing tests |
| Goroutine-leak check per test server | done | 0 leaks; a deliberate leak is caught |
| Fuzz targets | 8 (`irc` 4, `server` 3, `services` 1) | seeds run in every `go test` |
| irctest | controller `tools/irctest/beeircd.py`, runner `scripts/irctest.sh`, pinned `7d5d761` | 4 full runs on the homelab, `--network none` |
| Go livecheck | `tools/livecheck` (core, opers, modes) | **182/182 PASS** against the test server (146 + 11 + 25; the wrong-password `/oper` on Belon is now opt-in `-admin`) |
| GitHub Actions | `.github/workflows/ci.yml` | actionlint clean. **NOT TESTED on GitHub** (needs a push) |
| TESTING.md | done | |

### Fuzzing findings (all fixed, each with a regression test in `TestFuzzFindings` / `TestBanMaskWithSpaceRefused`)

| # | Found by | Problem | Fix |
| --- | --- | --- | --- |
| F1 | FuzzParse | empty/invalid tag keys accepted (`@=x CMD` printed back as `@ CMD`) | keys must follow IRCv3 (`[+][vendor/]name`) |
| F2 | FuzzNormalizeBan | a ban mask with a space accepted: `MODE #a +b :foo bar` stored it and broke the 367 list line | masks with space/CR/LF/NUL refused (696) |
| F3 | FuzzNormalizeBan | long masks cut to 100 bytes lost their `!`/`@` (the ban never matched) | nick/user/host cut separately (30/10/58), like UnrealIRCd |
| F4 | FuzzParse | NUL in a tag value or command reached the wire | dropped / cleaned |
| F5 | FuzzNormalizeBan | `~a:` with only invalid UTF-8 became an empty `~a:` ban | refused |
| F6 | follow-up of F2 | the 696 reply echoed `foo bar` as a middle parameter | `middleParam` (§8 S1) |

Final runs: FuzzParse 3 min (130 M execs), FuzzNormalizeBan, FuzzSplitFraming,
FuzzTags 1–2 min each, and FuzzParseFlood, FuzzStripColour, FuzzReadLine,
FuzzParseMlock 1 min each: **no further findings**.

### irctest baseline (run 4, the BeeIRCd build with S1/S2/T0)

**235 PASS · 31 FAIL · 171 SKIPPED · 3 XPASS** (98 deselected:
`implementation-specific` or `deprecated`, as for other servers).

- **SKIPPED = NOT IMPLEMENTED.** These are irctest's own reasons, not failures:
  - metadata (27), WHOX (17), chathistory (20), extended-monitor (18), read-marker (9);
  - redaction (8), account-registration (8), bot mode (6), ACCOUNTEXTBAN token (5), multiline (4), UTF8ONLY (3);
  - SCRAM (2), HELP (3), LINKS (2), WebSocket (8), PASS (4).
- **XPASS:** 3 strict tests on NUL inside SASL passwords that irctest expects most servers to fail.

The 31 FAILs, classified:

| Class | Count | Tests | What it is |
| --- | --- | --- | --- |
| **O1 ordering** (real, needs your decision) | 13 | oper.py ×2, info, kill, wallops, who NickOper ×3, NickAwayAndOper ×3, whois `[oper]` ×2 | the OPER reply arrives after the next command (§8 O1) |
| **Real protocol gaps** (small fixes, not made) | 12 | see list below | BeeIRCd behaviour differs from the spec |
| **Expected differences** (BeeIRCd choice) | 4 | help `[HELPOP]` ×3, `WHO *` | HELPOP answers with UnrealIRCd's 292; modern IRC uses 704/705/706. `WHO *` lists the built-in services as users (10 lines instead of 3) |
| **Harness limit** (BLOCKED) | 2 | sasl `ReAuthenticate`, `PostRegistrationAndReAuthenticate` | irctest registers account `foo` while a client named foo is online. BeeIRCd's NickServ (Anope-style) registers only your *current* nick, so the account can't be created that way |

The 12 real gaps:
1. `USER` with an empty realname is accepted (should be 461).
2. `+e ~m:mask` doesn't lift a `~m` mute (UnrealIRCd and InspIRCd allow it).
3. `KICK #a,#a bar,baz` (a channel list) isn't supported, although TARGMAX says KICK:4.
4. A body over 512 bytes isn't answered with 417. It is cut instead.
5. Client tags over 4094 bytes aren't answered with 417.
6. `WHO exactnick` hides a +i user (it should show them on an exact nick).
7–9. PRIVMSG / NOTICE / TAGMSG to yourself with echo-message + labeled-response comes back in the wrong shape.
10. SASL after registration sends `MODE +r` between 900 and 903.
11. `WHOWAS nick 1` doesn't end with 369.
12. A 400 kB SASL string: irctest wants a different complaint than our 905 (needs reading).

None of these was changed, since they're outside the approved scope. They're good candidates for a small
"protocol conformance" step (my suggestion: before or with 1.5.9b).

## 10. Verification

| Check | Result |
| --- | --- |
| `scripts/check.sh` (gofmt, vet, staticcheck, all tests) | **PASS** |
| Windows tests `-count=6` | **PASS** |
| Linux race `-count=3` (homelab) | **PASS**, 0 data races |
| Coverage | **80.9% → 81.3%** (same method; new code included) |
| govulncheck v1.9.0 | **0 vulnerabilities reachable.** 2 in required modules that we never call: GO-2026-6859 (Go 1.27.3 net/http, fixed in 1.27.4) and GO-2026-5932 (x/crypto/openpgp, unused) |
| livecheck against the test server | **182/182 PASS** (on 1.5.9a; the S1/S2 build isn't deployed) |

## 11. Remaining risks and documentation changes

- **Risks:**
  - **O1** ordering (above).
  - **S3** connection floods (step 7; temporary firewall mitigation in TESTING.md §6).
  - The 12 protocol gaps.
  - Go 1.27.4 toolchain update recommended (not done).
- **Docs to change** (not changed yet, your OK):
  - FEATURES.md SASL row (EXTERNAL is implemented).
  - FEATURES.md account-extban: "Implemented" → **Partial** (no `ACCOUNTEXTBAN` token).
  - ENGINEERING.md bench path and the "load test in check.sh" claim.
  - ASSESSMENT.md / SPEC.md marked historical.
  - HELPOP numerics (292 vs 704–706) need a decision before 1.5.9b docs.

## 12. Closed in the protocol conformance step (12 Nov 2026)

PLAN-conformance.md: all 12 real gaps from §9 fixed with a regression test each (`server/conformance_test.go`, every test fails on the old code). INFO added (371/374). HELP and HELPOP answer with 704/705/706 (524 for an unknown topic), so the 3 HELPOP "expected differences" are gone. The `ACCOUNTEXTBAN=a` token is advertised. Repeat `/oper` while opered now says why (UnrealIRCd wording, checked before the password). Docs D1-D8 fixed. Still different on purpose: `WHO *` lists the built-in services, and the 2 SASL re-auth tests that register an account for a nick someone else is using.

irctest run 5 (this build, Go 1.27.4): **270 PASS · 4 FAIL · 163 SKIPPED · 3 XPASS** (run 4: 235 / 31 / 171 / 3). The 4 fails:
- `WHO *`: expected difference (above).
- The 2 SASL re-auth tests: harness limit (above).
- `sasl testPlainLarge800`: harness limit, new. The controller registers the account with a 592-character password in one `PRIVMSG NickServ :REGISTER ...` line (619 bytes). BeeIRCd now refuses client lines over 510 bytes with 417 (A4), so no client could send that line. SASL with an 800-byte payload itself works (`testPlainLarge` passes).
