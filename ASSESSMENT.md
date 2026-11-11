# BeeIRCd architecture assessment (11 Nov 2026)

The full assessment, with the feature comparison, the IRCv3 table, the
ConferenceRoom heritage plan, sixteen innovation ideas and the roadmap
drawing, is a shared document:
https://claude.ai/artifact/362ftbQubR1pWT9ZCuZQWW

This file keeps the conclusions next to the code. The feature status lives
in [FEATURES.md](FEATURES.md).

## Findings

1. The architecture is sound for one server: one loop owns all state, UIDs
   and timestamps exist from day one, and tests drive the real protocol.
2. Three things must change before Stage 2 linking:
   - services data is read from SQLite on the main loop (every JOIN and
     NICK); cache it in memory and write through (done in 1.5.2);
   - state changes are direct edits, not events; add an event layer that
     local delivery and server links both use (done in 1.5.2);
   - channel status is two booleans; switch to ranks, which also gives the
     `@+` / `~&@%+` prefix option.
3. About 20 of the 34 released IRCv3 specs were implemented on 11 Nov 2026; MONITOR, SASL EXTERNAL and account-extban (with the ACCOUNTEXTBAN token) have landed since, and irctest runs in TESTING.md. Next and cheap:
   standard-replies, MONITOR, extended-monitor, WHOX, SETNAME, bot mode,
   UTF8ONLY, no-implicit-names, SASL EXTERNAL and SCRAM. Run irctest in CI.
4. Gaps against UnrealIRCd, InspIRCd, Solanum and Ergo: linking, ban types
   (CIDR, Z-line, Q-line, SHUN, extbans), anti-abuse (DNSBL, filters), an
   admin API, a module system.
5. ConferenceRoom 2.1 (help files on the homelab, /home/cr/CR2.1-Linux)
   stood out for auditorium rooms (+A), moderated question queues (+M/+N),
   channel overflow (+O), HelpServ, buddy lists, and opers managed in
   AdminServ with their own /oper password.

## Roadmap

> **Superseded (11 Nov 2026):** the order decided with the owner is Stage 1.5 (services and modes, 1.5.3-1.5.9) -> step 7 (installer, systemd, Let's Encrypt, REHASH) -> Stage 2 linking -> Stage 3 Hive Panel -> Stage 4 BeeRoom. The table below is the original proposal, kept for the record.

| Phase | Contents | Gate |
| --- | --- | --- |
| 1 | Step 6 fixes from the user's test (oper via /oper, GLOBAL wording, more) | tests, live test, user OK |
| 2 | Step 7: installer, beeircd command, systemd, Let's Encrypt, STS, REHASH | clean-VM install, irctest in CI |
| 3 | Quick IRCv3 wins (list above) | irctest per cap before advertising |
| 4 | Foundations: ranks + prefix option, services cache, event layer, services behind an interface, ban engine, oper classes | all tests, 5,000-client load test |
| 5 | Stage 2 linking | 3-server homelab network, split tests |
| 6 | HelpServ, event rooms, raid shield, why-check, audit log | per-feature tests, scripted raid |
| 7 | Stage 3 Hive Panel + admin API | API security review |
| 8 | Stage 4 BeeRoom (WebSocket, chathistory, account-registration, multiline) | browser tests, irctest over WebSocket |
| 9 | After BeeIRCd is done: built-in services off switch + Anope link, then Unreal/InspIRCd links, modules | Anope on a 3-server network |

## Decided by the user (11 Nov)

- Channel prefixes are a server option: `@+` only or `~&@%+`. New installs
  default to `@+`; the installer asks which one.
- Opers must /oper up (`/oper <nick> <oper password>`) with a separate oper
  password per oper, ConferenceRoom style; identifying to NickServ does not
  oper anyone.
- ChanServ keeps SOP/AOP/VOP/UOP; levels are stored as permission sets from
  Stage 1.5 so Anope-style FLAGS/LEVELS can come later as an advanced option.
- Services follow Anope (most used, most modern): same commands, syntax and
  behaviour where Anope has the feature; ConferenceRoom extras on top.
- HostServ (vhosts) is built in Stage 1.5, before linking; BotServ is Anope
  style, a separate bot per channel. HelpServ helpers use user mode +h.
- Built-in services get an off switch; Anope support comes after BeeIRCd is
  finished.

## Open

- Which innovation ideas to plan in.
- Full command list per service.
