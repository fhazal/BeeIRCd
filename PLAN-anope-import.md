# PLAN – import an Anope database (for 1.1)

Status: planned 14 Nov 2026 for **1.1**, the first release after 1.0. Not
in 1.0: 1.0 is in final testing (rc2), and this is a new feature.
Decisions D1-D6 are recommendations waiting for approval.

Why: networks moving from Anope to BeeIRCd services today lose every
registered nick, channel, access list, memo and vhost. Our services
already follow Anope 2.0.20 (PLAN-anope-2.0.20.md, ANOPE-ALIGNMENT.md):
the same access level numbers (1-10000), the 32 LEVELS privileges, xOP
names, NickServ/ChanServ/MemoServ/HostServ/BotServ/OperServ data. So the
import is mostly a straight copy, with a short list of things that need
a decision.

Sources: Anope 2.0.20 `modules/database/db_flatfile.cpp` (file format),
the `Serialize` methods of NickCore, NickAlias, ChannelInfo, ChanAccess,
AutoKick, Memo, BotInfo, XLine, ForbidData, NewsItem, Exception,
`modules/encryption/enc_*.cpp` (password hashes), our
`internal/store` schema 3.

## What gets imported

| Anope (db_flatfile record) | BeeIRCd | Notes |
| --- | --- | --- |
| NickCore | account | name, password hash (D2), email, registered, last seen, flags (KILL*, SECURE, PRIVATE, HIDE_*, AUTOOP, KEEPMODES, MEMO_*), URL, greet, memo limit, suspension + expiry, NOEXPIRE, access hosts, AJOIN |
| NickAlias | nick in the account | grouped nicks, display nick, last host/quit/realname, NOEXPIRE, vhost (HostServ, per nick as ours) |
| ChannelInfo | registered channel | founder, successor, description, registered, last used, topic + setter, mode lock, SET options, LEVELS, entry message, suspension, NOEXPIRE, memo box + limit |
| ChanAccess | channel access | access provider numbers as-is; xOP entries to their numbers (UOP 1 ... QOP 10000); FLAGS entries: D4 |
| AutoKick | AKICK | mask or account, reason, creator, time, last used |
| Memo (nick + channel) | memo | sender, time, text, unread, receipt flags |
| BotInfo + channel bot settings | bots, chan_bot | BotServ bots, assigned bot, GREET/FANTASY/NOBOT/DONTKICK*, kickers + times-to-ban, BADWORDS |
| HostRequest | HostServ request | waiting vhosts |
| XLine (AKILL, SNLINE, SQLINE) | network bans | mask, reason, setter, set time, expiry (expired ones skipped) |
| ForbidData | OperServ FORBID | kind, mask, reason, expiry |
| Exception | session exception | mask, limit, reason, expiry |
| NewsItem | LOGONNEWS / OPERNEWS / RANDOMNEWS | |
| Oper (os_oper) | OS OPER entry | Anope opertype -> our built-in class by name, unknown types listed (D5) |
| Seen (cs_seen), Ignore (os_ignore), Info (os_info) | the 1.5.5b tables | |

Not imported: Anope's runtime state (sessions, stats), module data for
modules we don't have (listed by name in the report), and the
`anope.conf` itself (settings are set by hand in `beeircd.conf`; the
report names the ones that matter: nick/channel expiry, `maxmemos`,
`defaults` lines).

## Decisions

**D1. Which Anope databases.**
Anope 2.0.x `db_flatfile` (`data/anope.db`) is the default and the most
common. `db_sql` / `db_sql_live` (MySQL/SQLite) and `db_redis` store the
same records in tables or keys. 2.1 changed some field names.
Recommend: **2.0.x flatfile first** (all of 1.1). 2.1 flatfile in the
same step if a sample shows only field renames. SQL/Redis: tell the user
to switch Anope to db_flatfile for one save (`OS UPDATE`) and import
that, which Anope supports, instead of writing three readers.

**D2. Passwords.**
We only keep argon2id. Anope stores what its `enc_*` module made:
`bcrypt:`, `sha256:`, `md5:`, `sha1:`, `oldmd5:`, `plain:` (prefixed by
module name). They can't be converted.
Recommend: **import the hash as-is with its prefix and check it with
that method at IDENTIFY / SASL PLAIN / Hive Panel sign-in; on the first
right password, store argon2id instead** (silently). Report how many
accounts are still on a legacy hash; `beeircd check` lists the count
too. `plain:` passwords are hashed with argon2id at import time, never
stored as text. SASL SCRAM (planned) is not offered to an account until
it has an argon2id hash.

**D3. How it runs.**
Recommend: **`beeircd import-anope <anope.db>`**, run with the server
stopped, into the configured services database:
- `--dry-run` (default first step): parse everything, print counts per
  record type and every record that can't be mapped, write nothing.
- Refuses a database that already has accounts or channels, unless
  `--merge` (D6). Takes a `beeircd backup` first anyway.
- One SQLite transaction: all or nothing.
- Writes a report file next to the database (what was skipped and why).

**D4. ChanServ FLAGS entries.**
Anope access entries can be by level, by xOP, or by FLAGS (`+AFORtv`
etc.). We have levels and xOP; FLAGS is planned (FEATURES.md).
Recommend: **map FLAGS entries to the lowest level that gives every
privilege in the flags** with the channel's LEVELS, and list each one in
the report (a few may gain a privilege, never lose one). When ChanServ
FLAGS ships, a later import keeps them as flags.

**D5. Opers.**
Anope opertypes are named in anope.conf, which we don't read.
Recommend: **match by name to our classes** (helper, globop, admin,
netadmin and any `[[operclass]]` of the same name); anything else is
imported as **disabled** with its type in the report, so nobody gets
more rights than intended. Opers still need their `[[oper]]` / OS OPER
password set as usual.

**D6. Merging into a live BeeIRCd database.**
Recommend: **not in 1.1.** Only a fresh database (or `--merge` refused
with a clear message). Merging needs rules for name clashes on both
sides; we add it only if someone asks.

## Sub-steps (each one: build, check.sh + its tests, ask to commit)

### 1.1-ai-a – reader
- `internal/anope`: db_flatfile parser (`OBJECT` / `DATA` / `END`
  blocks, escaped values), typed records for the table above.
- Fuzz test on the parser (it reads files from elsewhere); a fixture
  `anope.db` made with a real Anope 2.0.20 on the homelab sandbox
  (TESTING.md), covering every record type.

### 1.1-ai-b – legacy passwords
- `internal/passwd`: check `bcrypt`, `sha256` (Anope's own salted
  form), `md5`, `sha1`, `oldmd5`; `NeedsRehash`.
- Services: rehash on a right password at IDENTIFY, SASL PLAIN, panel
  sign-in. Wrong-password lockout counts as now.
- Tests against hashes made by the real Anope modules.

### 1.1-ai-c – import command
- `beeircd import-anope [--dry-run] <file>`: mapping, report, one
  transaction, backup first, refuse a non-empty database.
- Test: import the fixture, then check with the existing services
  tests' helpers (IDENTIFY with the old password works and rehashes,
  channel access, AKICK, memos, vhosts, bots, bans all present).

### 1.1-ai-d – docs and live test
- ADMIN.md "Moving from Anope" (stop Anope, copy anope.db, dry run,
  import, set the conf values the report names, start).
- Import a copy of a real network's database on the test server, run
  livecheck services suites; FEATURES.md row to Implemented.

## Gates
check.sh, -count=6, Linux race -count=3, 1 h fuzz on the new parser,
import of the fixture in CI.
