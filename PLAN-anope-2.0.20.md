# Plan: re-check every service against Anope 2.0.20 (approved and built 11 Nov 2026)

Why: the 1.5.3 alignment (ANOPE-ALIGNMENT.md) followed Anope **2.1**
(d2eadd8). The rule is now Anope **2.0.20 stable**, exactly. The log
channel already matches 2.0.20; this pass covers commands, syntax,
options and defaults.

How it was checked: every command class in 2.0.20 `modules/commands`
against 2.1 `modules/*` (syntax lines), the default `command {}` blocks
in both example configs (names, aliases, hidden), the ChanServ privilege
table, the `defaults =` lines, then each difference against our code.

Result: most of what we built is the same in both versions. The
differences below are where 2.1 changed something and we followed 2.1.

## A. Changes, no decision needed (follow 2.0.20)

| # | Now in BeeIRCd (2.1 way) | Anope 2.0.20 | Change |
| --- | --- | --- | --- |
| A1 | `NS SET PROTECT {ON \| <10-600s> \| OFF}`, ENFORCE hidden | `SET KILL {ON \| QUICK \| IMMED \| OFF}`: 60 s, 20 s, at once | KILL is the command; PROTECT and ENFORCE stay hidden aliases. Stored delays move to the nearest of 60/20/0. SASET KILL too. |
| A2 | `NS SET HIDE {EMAIL \| MASK \| STATUS \| QUIT}` (HOST hidden) | `{EMAIL \| STATUS \| USERMASK \| QUIT}` | USERMASK; MASK and HOST hidden aliases. |
| A3 | `CS SET SECURE` = hidden alias of SECUREOPS | `SET SECURE`: account access counts only when identified, not only "recognised" (on by default) | Real 2.0.20 meaning; goes with B1. SECUREOPS keeps its own name. |
| A4 | `OS STATS [ALL]` | `STATS [AKILL \| HASH \| UPLINK \| UPTIME \| ALL \| RESET]` | Add AKILL, UPTIME, UPLINK (our links, empty until Stage 2), RESET (resets the max-users count). HASH is about Anope's C++ tables: we answer with our map sizes. |
| A5 | `OS MODE #chan <modes>` | also `MODE #chan CLEAR [ALL]` | Add CLEAR (all modes, bans, excepts, invex; ALL also takes status). User modes come with UMODE in 1.5.5b. |
| A6 | SNLINE mask is one word | `ADD [+expiry] mask:reason` | Accept Anope's `mask:reason` too; our one-word form keeps working. |
| A7 | NS STATUS planned for 1.5.6 | `STATUS [nick ...]` with 0-3 (0 not registered/offline, 1 not recognised, 2 recognised by access list, 3 identified) | Build now; it belongs with B1. |
| A8 | HELP syntax lines | 2.0.20 syntax lines | HELP shows the 2.0.20 syntax for every changed command. Our own help wording stays (Anope's text is GPL, we don't copy it). |

Already the same in both versions (checked, no change): REGISTER,
IDENTIFY/ID, LOGOUT, RECOVER, GROUP, UNGROUP, ALIST, INFO, LIST, SET
PASSWORD/EMAIL, SASET; CS REGISTER, INFO, ACCESS, LEVELS, xOP, AKICK,
STATUS, OP..DEOWNER, UP/DOWN, SYNC, KICK, BAN, INVITE, GETKEY, MODE,
TOPIC SET/APPEND/LOCK/UNLOCK, ENTRYMSG, ENFORCE, LOG, SUSPEND, SET
options; MS SEND, LIST, READ, DEL, SET, IGNORE; OS AKILL, SQLINE,
FORBID (2.0 LIST kind is optional, as ours), SESSION, EXCEPTION,
LOGONNEWS, KILL, KICK, GLOBAL. Privilege names and level numbers are the
same except UNBANME (B4). Default options match: NS kill protect, hide
email + mask; CS keeptopic, peace, securefounder, signkick.

Found while building, same rule (no decision needed):

| # | Now in BeeIRCd | Anope 2.0.20 | Change |
| --- | --- | --- | --- |
| A9 | Network bans without +expiry stay for good | `autokillexpiry`, `snlineexpiry`, `sqlineexpiry` = 30d (also in the owner's live config); `+0` = for good; a bare number = days | `ban_expiry` (30 days) for AKILL, SNLINE, SQLINE, ZLINE, SHUN; ELINE stays until removed. IRCd-style `/ZLINE mask 0 reason` = +0. |
| A10 | UNGROUP deletes the nick | UNGROUP moves the nick into a new account of its own, with the same password and email; ungrouping the display nick moves the display | Same as Anope. |

## B. Decisions (approved: all five recommendations)

**B1. NickServ ACCESS (host list) is back in 2.0.20.**
In 1.5.3 we removed it because 2.1 did (decision N2). In 2.0.20,
`ACCESS ADD|DEL|LIST [nick] mask` exists, with `SET SECURE` (on by
default) and STATUS level 2 "recognised".
With SECURE on, being on your access list does **not** log you in or
give channel rights. It only means NickServ doesn't start the KILL
timer on you, and STATUS says 2. With SECURE off, a matching host is
treated as the owner (the weak old way).
Recommend: **bring it back exactly as 2.0.20** (ACCESS, SET/SASET
SECURE default on, STATUS 0-3, CS SET SECURE). ALIST stops being an
alias of ACCESS. The default is safe, and the rule says follow 2.0.20.
CERT + SASL EXTERNAL stay in 1.5.6.

**B2. NS DROP.**
2.0.20: `DROP <nickname>`, no code. It drops **that nick only**. The
account goes when its last nick goes; dropping the account's main name
moves the name to the next nick. Typing the nick is the confirmation.
We have (2.1): `DROP <nick> [code]`, and the account name drops the
whole account.
Recommend: **2.0.20 exactly** (no code, one nick at a time). Opers with
flag s may drop anyone, except other services opers (secureadmins).

**B3. CS DROP.**
2.0.20: `DROP #chan #chan`: you type the channel name twice. We have
(2.1): `DROP #chan [code]`.
Recommend: **2.0.20** (name twice; the code goes).

**B4. UNBANME.**
2.0.20 has no UNBANME privilege: UNBAN on yourself needs UNBAN. Both
default to HOP (4), so nothing changes for anyone with default levels.
2.0.20 also has `UNBAN` with no channel, which unbans you everywhere
you have UNBAN.
Recommend: **2.0.20**: drop UNBANME and add UNBAN without a channel. A
channel that changed UNBANME with LEVELS loses that setting; I list
those channels in the server log when the database migrates.

**B5. 2.1 extras we already built, which 2.0.20 doesn't have.**
These don't change any 2.0.20 command; they only add to it:
TOPIC PREPEND; a description on xOP/ACCESS ADD; GHOST and RELEASE as
hidden aliases (2.0.20 ships them commented out in the config); WHY
(hidden alias of STATUS).
Recommend: **keep them all as hidden extras**: they work, but HELP
shows only 2.0.20. GHOST is muscle memory for many people.

## C. Not in this step (already planned)

- NS SET AUTOOP, DISPLAY, KEEPMODES, LANGUAGE, MESSAGE, PRIVATE, URL,
  GLIST, AJOIN, RESETPASS, CERT; MS CANCEL CHECK INFO RSEND SENDALL
  STAFF: 1.5.6 as planned. MESSAGE (services talk by PRIVMSG) gets
  added to the 1.5.6 list.
- NS GETPASS: not possible, because we only keep password hashes. Anope
  2.0.20 also refuses when passwords are encrypted.
- NS CONFIRM, LIST SUSPENDED/NOEXPIRE/UNCONFIRMED: come with email
  confirmation and NS SUSPEND (1.5.6).
- OS OLINE, QUIT, JUPE, DEFCON, module/config commands: IRCd- or
  Anope-specific; AdminServ covers ours.

## D. Build and test

- Store: accounts get `access` (hosts) + `secure` (default on);
  protect_delay maps to kill mode; UNBANME rows go from chan_levels;
  schema bump with migration test on a copy of the test server DB.
- Tests: each change in A and B gets a test; the old 2.1-shape tests are
  rewritten; log lines for ACCESS / SET KILL / DROP follow 2.0.20 (lines
  are in /opt/beeircd-test/anope-logs.txt).
- Gates: check.sh, -count=6, Linux race -count=3, load gate, quick
  perf alarm (bench2.sh bee: 5k ~100 MB, chat ~53%, lag 5-10 ms).
- Deploy to the test server, run live155.py plus new checks, then
  ANOPE-ALIGNMENT.md gets a "2.0.20" section, FEATURES.md updated.
  Commit message: "Services follow Anope 2.0.20".
