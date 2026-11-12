# PLAN 3 - Hive Panel (Stage 3)

Status: APPROVED in outline 12 Nov 2026 (owner: "proceed"), with one
change: our OWN panel, written from scratch in this repo. The panel lab
(beeircd-panel-lab, ChatGPT's experiment) stays as it is: no code, files
or look taken from it. Order (owner): v1.0 rc1 and the 2g link tests come
first when the soak ends; Hive Panel work runs on branch `stage3` while
we wait. New mockup (v2) first, then each sub-step asks "build 3x?".
Mockup v2 OK'd by the owner 12 Nov (mockups/hive-panel-v2).
3a built 12 Nov 2026 (branch stage3): beeircd-web + internal/hivepanel
(sessions, address lock, cross-site guard, host check, CSP, live stream,
bundled fonts), server side internal/server/panel3a.go (panel-login with
the /oper lockout, every request re-checked, Settings via AdminServ's SET
so HISTORY/UNDO cover it, panel_log table for What happened), [web]
config block. Pages: Sign in, Overview, Settings + server actions.
Changes while building: lockout is /oper's (5 wrong: 10 minutes, not
15) plus a per-address 5/10 minutes in the panel; host masks are checked
against the web address only (no hostname); installer unit moves to 3g;
Update in the panel only says "run beeircd update" for now.
3b built 12 Nov 2026 (branch stage3): People page (internal/server/
panel3b.go, services PanelAkill): everyone on the network, search
(addresses only with h), filters, 50 a page, refreshed every 15 s unless
a person is open; details; notice (any oper), disconnect and ban (flag k,
the same flag as /KILL and OperServ AKILL; the mockup said s). Ban = AKILL
on *@address, 1 h / 1 d / 7 d / 30 d / until removed, asks first; "What
happened" never shows the address. Client version isn't known (no CTCP
VERSION), so the details don't show it.
3c built 12 Nov 2026 (branch stage3): Bans page (internal/server/
panel3c.go, services PanelBan/PanelBanDel/PanelBanMatch): AKILL, ZLINE,
SHUN, exceptions (the panel calls ELINE "Exception"); flag k for the
whole page (OperServ's rule; s does not give bans, decided with the
owner 12 Nov). SQLINE/SNLINE stay in OperServ for now.
3d built 12 Nov 2026 (branch stage3): Opers page (internal/server/
panel3d.go): AdminServ OPER's rules (give/take only own flags, remove
only with all their flags, owner locked) plus no self-removal and no
dropping your own a; Undo through HISTORY (oper kind added to the panel's
undo; a removed entry's password is kept in memory for that). Flag
legend fixed: k = disconnect + network bans, s = other OperServ tools.
Not in the panel yet: DISABLE/ENABLE, title, vhost, swhois (AdminServ).
UI: one put() helper for every content swap (two "null" text slips were
found in 3b/3d).

## What it is
Hive Panel = the web admin for BeeIRCd. A separate program
`beeircd-web` (ships in the same download), loopback only, reached over an
SSH tunnel or a reverse proxy the admin sets up. It talks to the server
only through the local control socket, so a panel bug cannot crash IRC.

## Decided earlier (owner)
- P1 Sign-in = oper nick + OPER password (same record as /oper), not the
  NickServ password. Normal users cannot sign in.
- P2 Flags are checked on the SERVER for every action (hiding buttons is
  only cosmetic): h hosts, k kill, g global, s OperServ/bans, c links,
  a admin/settings/rehash/AdminServ, owner = everything + backups. An oper
  can only give flags they have; the owner row is locked. Audit log of who
  changed what.
- P3 Look = our own design (Hive Panel mockup v2, mockups/hive-panel-v2),
  black + yellow like beeircd.org, one SVG line-icon set, yellow left bar
  with straight corners on active items, balanced blocks, zen: hide what
  the signed-in oper cannot use. One row opens at a time, no row
  pre-selected.
- P4 Saves are XenForo-style: no page reload, busy button, a toast that
  says WHAT changed, the changed row lights up briefly, Undo where
  AS HISTORY/UNDO allows.
  AJAX everywhere (owner 12 Nov): the page loads once, then every action
  is a fetch() to the JSON API, no reloads and no outside libraries:
  sign in/out, settings save + Undo, check/rehash/backup/update, people
  search/filter/paging + row details, notice/disconnect/ban, bans
  add/remove (+ live "matches N online" while typing the mask), opers
  edit/remove, link blocks + connect/split. Live parts (overview
  numbers, What happened, link states, people count) update by
  themselves over one Server-Sent Events stream (falls back to a 15 s
  poll), paused when the tab is hidden; a form being typed in is never
  overwritten. Errors show next to the thing that failed, in plain words.

## Recommendations (need OK)
- R1 Branch `stage3` from `stage2` (the Servers page needs linking).
  main stays the v1.0 rc line. Ships as v1.2 after v1.1 (linking).
- R2 (owner 12 Nov) Written from scratch: cmd/beeircd-web +
  internal/hivepanel, UI files embedded with go:embed, no build step, no
  external fonts or scripts (strict CSP). Nothing from the lab.
- R3 Login safety: 5 wrong tries -> 15 min lock per oper and per IP,
  oper host rule checked against the web client IP (or the proxy's
  X-Forwarded-For only when `[web] trusted_proxy` is set), session cookie
  HttpOnly + SameSite=Strict + Secure when behind TLS, 30 min idle
  timeout. The one-time startup key goes away (oper login replaces it).
  2FA (TOTP) later, not in v1.2.
- R4 Config: new `[web]` block (listen, trusted_proxy, session_idle),
  `beeircd install` asks "set up Hive Panel? (y/N)" and adds a systemd
  unit `beeircd-web` that runs as the beeircd user.

## Sub-steps
- 3a The panel program + oper sign-in. Control commands `oper-login`
  (checks AdminServ record, host rule, lockout; returns flags) and
  `whoami`. Panel sessions carry flags; every control call carries the
  session's oper so the server re-checks flags (P2). Audit log
  (journald + services.db table). Pages: Sign in, Overview, Settings,
  Server actions (backup = owner only).
- 3b Users page: control `users` (list, search, filter all/opers/guests,
  paging for 5k users), `user` (details: real host, client, channels),
  `notice`, `kill` (k), `ban-nick` (s). One row open at a time.
- 3c Bans page: `bans` list AKILL/ZLINE/SHUN/exceptions, `ban-add`
  (live "matches N online" preview), `ban-del`, expiry. Network-wide via
  the linking code (TKL).
- 3d Opers page: `opers`, `oper-set` (class, flags, allowed-from, new
  password), `oper-del`; owner locked, can only give own flags; Undo via
  AS HISTORY.
- 3e Linked servers page: list/map, link blocks (add/edit/remove with
  fingerprint), CONNECT/SQUIT (c), per-link lag and burst state.
- 3f Services settings: the [services] values the panel may change,
  BotServ web view later (PLAN-botserv).
- 3g Bug sweep first (owner 12 Nov: before any release, sweep for bugs and fix them all), then the gates: RBAC matrix test (every flag x every endpoint), CSRF and
  session tests, API fuzzing, login lockout test, phone 375 px, light +
  dark, every button clicked on a real build (full button check),
  security review of the API, installer test on Debian 13 + Ubuntu 24.04.

## Not in Stage 3
BeeRoom (web chat) = Stage 4. Public internet exposure without a reverse
proxy is not supported.
