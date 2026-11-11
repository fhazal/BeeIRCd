# PLAN – protocol conformance step (before 1.5.9b)

Goal: close the protocol gaps irctest found (AUDIT-TESTING.md §9) and the doc drift (§2.3, §11), and bring the toolchain up to date. Small and contained: no new features apart from INFO and the HELP replies.

## A. Protocol gaps (12 + INFO)

| # | Gap | Fix |
| --- | --- | --- |
| A1 | `USER` with an empty realname is accepted | 461 ERR_NEEDMOREPARAMS |
| A2 | `+e ~m:mask` doesn't lift a `~m` mute | an exception that matches also lifts the mute (UnrealIRCd/InspIRCd behaviour) |
| A3 | `KICK #a,#b nick1,nick2` lists not supported (TARGMAX says KICK:4) | RFC 2812 pairs: 1 channel + N nicks, or N channels + N nicks |
| A4 | A body over 512 bytes is cut, not refused | 417 ERR_INPUTTOOLONG, line dropped |
| A5 | Client tags over 4094 bytes not refused | 417, line dropped |
| A6 | `WHO exactnick` hides a +i user | an exact nick always shows the user (like WHOIS) |
| A7–9 | PRIVMSG / NOTICE / TAGMSG to yourself with echo-message + labeled-response comes back in the wrong shape | one echo carrying the label, no extra BATCH (as the spec says) |
| A10 | SASL after registration sends `MODE +r` between 900 and 903 | send `+r` after 903 |
| A11 | `WHOWAS nick 1` doesn't end with 369 | always end with 369 |
| A12 | 400 kB of AUTHENTICATE lines gets 905 | 905 stays for a single line over 400 bytes. A total over the limit gets 904 (what irctest, Ergo and InspIRCd do) |
| A13 | `INFO` returns 421 | 371/374: name, version, commit, motto, licence, build date, start time. Opers also see the config file path |

| A14 | `/oper` while already opered says nothing (user report 11-12). `operUp` returns quietly when +o is set and the flags didn't change | UnrealIRCd style (`oper.c`): check **before** the password, so no hash work and no failed attempt counted. Reply `*** You are already an IRC operator (as <account>, class <class>). To oper again, de-oper first: /mode <nick> -o`. No server notice (Unreal sends none). InspIRCd instead silently logs out and in again with a new 381; we don't copy that, because it hides typos and re-sends oper news |

## B. Expected differences: decide

| # | Item | Recommendation |
| --- | --- | --- |
| B1 | HELPOP answers with UnrealIRCd's 292 | **Switch to 704/705/706** (modern standard, what clients render). Add `HELP` as an alias with the same text. 292 goes |
| B2 | `WHO *` lists the built-in services as users | **Keep.** It's true: they are on the server, and Anope's clients show up the same way. Note it in FEATURES.md |
| B3 | The `ACCOUNTEXTBAN` 005 token is missing, but `~a:` works | **Add it** (`ACCOUNTEXTBAN=a,account`). This unlocks 5 skipped irctest tests. FEATURES row goes Partial → Implemented |
| B4 | SASL re-auth tests (harness registers an account while a client with that nick is online) | **Leave as is.** Anope-style REGISTER works that way on purpose. Listed as a known difference in TESTING.md |

## C. Docs (D1–D8, D12)

- **FEATURES.md:** SASL row (EXTERNAL done), account-extban, HELP numerics, INFO.
- **ENGINEERING.md:**
  - bench path `/opt/beeircd-test/bench/`
  - "load test" sentence fixed: it runs with `BEEIRCD_LOAD=1`, not in check.sh
  - 5k numbers refreshed (112–116 MB, chat 53–55%)
- **check.sh:** the comment now says CI exists.
- **ASSESSMENT.md:**
  - roadmap marked superseded (Stage 1.5 → linking → Hive Panel → BeeRoom)
  - IRCv3 count refreshed
- **SPEC.md:** marked historical (draft 2, Stage 1).
- **AUDIT-TESTING.md:** short "closed in conformance step" note.
- **Delete:** the local leftovers `dist/beeircd-linux-sym`, `beeircd-o1-linux` and `beeircd-t0-linux`, which are git-ignored.

## D. Toolchain

- **Go:** 1.27.3 → 1.27.4 (fixes GO-2026-6859 in net/http). Covers go.mod, CI, and the homelab image `golang:1.27` once it pulls 1.27.4. Install Go 1.27.4 on the PC.
- **CI actions:** `actions/checkout@v4` → v5 and `actions/setup-go@v5` → v6, which run on Node 24. Check with actionlint.

## Tests

- Each gap gets a regression test in `server/conformance_test.go` that fails on the old code.
- Then check.sh, plus one irctest run on homelab. This step is protocol-wide, so irctest is the real proof. No bench, race or load runs: the read loop isn't touched apart from the 417 checks, which are a length compare.
- Target: the 12 real gaps and 3 HELPOP failures pass. The remaining fails are only `WHO *` and the 2 harness tests.
- Deploy to the test server (ask first) and run livecheck once.

## Commits

1. `Protocol conformance: irctest gaps, INFO, HELP numerics`
2. `Docs: bring FEATURES, ENGINEERING, ASSESSMENT and SPEC up to date`
3. `Go 1.27.4 and Node 24 CI actions`
