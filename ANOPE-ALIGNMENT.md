# Services vs Anope 2.1: alignment list (draft for review)

Rule (agreed 11 Nov 2026): services follow Anope for commands, syntax and
behaviour wherever Anope has the feature; ConferenceRoom extras sit on
top. This list compares what BeeIRCd had with Anope 2.1 (source d2eadd8) and
says what changes. Status 11 Nov 2026: every item not marked "in 1.5.4",
"1.5.5" or "1.5.6" is built (Stage 1.5.3), except M1's channel memo boxes
(see below).

Marks: **Change** = we switch to Anope's way. **Keep** = ours stays (a
CR extra or a deliberate choice). **Ask** = needs your decision.
"Hidden alias" = the old word still works but HELP doesn't show it, as
Anope does with GHOST.

## NickServ

| # | Now in BeeIRCd | Anope 2.1 | Proposal |
| --- | --- | --- | --- |
| N1 | `GHOST <nick> [password]` and `RECOVER` | `RECOVER <nick> [password]`; GHOST and RELEASE are hidden aliases | **Change**: RECOVER is the command; GHOST stays a hidden alias. RECOVER keeps doing both (disconnect, then take the nick). |
| N2 | `ACCESS ADD/DEL/LIST <mask>`: hosts that identify you automatically | Removed. `ACCESS` is now a hidden alias for `ALIST` (channels you have access in). Certificates (`CERT`) replace host lists | **Change** (decided 11 Nov 2026). Recommended: Anope's way: drop host auto-identify (it is weak, and on a cloaked network a host says little), make ACCESS an alias of ALIST, and bring CERT + SASL EXTERNAL forward to 1.5.6 as the safe auto-identify. |
| N3 | `DROP`, then `DROP <code>` | `DROP <nickname> [code]` | **Change**: you name what you drop. Your account name drops the whole account; a grouped nick drops only that nick (same as UNGROUP). The code step stays. |
| N4 | `SET ENFORCE ON\|OFF` | `SET PROTECT {ON \| <delay> \| OFF}` | **Change**: PROTECT, with an optional delay (default 60 s); ENFORCE is a hidden alias. |
| N5 | `SET HIDE EMAIL\|HOST ON\|OFF` | `SET HIDE {EMAIL \| STATUS \| MASK \| QUIT} {ON \| OFF}` | **Change**: HOST becomes MASK; add STATUS (hide that you are an IRC operator, Anope's meaning) and QUIT (hide the last quit message). |
| N6 | `GROUP <nick> <password>` | `GROUP [target] [password]` | Keep (same thing). GLIST comes in 1.5.6 as planned. |
| N7 | Opers: `/os nick setpass`, `/os nick drop` | `/ns saset password <nick> <pw>`, `/ns drop <nick>` with the oper privilege | **Change**: the Anope forms. OperServ NICK SETPASS/DROP go away (one way to do each thing). |
| N8 | `IDENTIFY [nick] <password>`, `LOGOUT`, `INFO [nick]`, `LIST <pattern>`, `REGISTER <password> [email]` | same | Keep. LIST options (`NOEXPIRE` and the like) come with SUSPEND in 1.5.6. |

## ChanServ

| # | Now in BeeIRCd | Anope 2.1 | Proposal |
| --- | --- | --- | --- |
| C1 | `SET #chan <option> <value>` | `SET <option> #chan <value>` | **Change, softly**: HELP shows Anope's order, and both orders work (a word starting with # is the channel). Nobody's habit breaks. |
| C2 | `SET #chan DESC` | `SET DESCRIPTION` (DESC hidden alias) | **Change**: DESCRIPTION, DESC alias. |
| C3 | `SET #chan MLOCK <modes>` | `MODE #chan LOCK ADD\|DEL\|SET\|LIST` | **Change in 1.5.4** (already planned there): MODE LOCK; `SET MLOCK` stays a hidden alias. |
| C4 | `SET #chan TOPICLOCK ON\|OFF`, `TOPIC #chan <text>` | `TOPIC #chan [SET\|APPEND\|PREPEND\|LOCK\|UNLOCK]` | **Change in 1.5.4**: TOPIC grows APPEND/PREPEND/LOCK/UNLOCK; SET TOPICLOCK hidden alias. |
| C5 | `SET #chan ENTRYMSG <text>` (one line) | `ENTRYMSG #chan ADD\|DEL\|LIST\|CLEAR` (several lines) | **Change in 1.5.4**; old SET form adds/replaces the first line. |
| C6 | `CLEAR #chan OPS\|BANS\|...` | `MODE #chan CLEAR [what]` | **Change in 1.5.4**: MODE CLEAR; CLEAR stays a hidden alias (CR users know it). |
| C7 | `DROP #chan`, then the code | `DROP #chan [code]` | Keep (already the same shape). |
| C8 | `SOP/AOP/VOP/UOP/QOP/HOP #chan ADD\|DEL\|LIST\|CLEAR` | `xOP #chan ADD <mask> [description] / DEL {mask\|num\|list} / LIST [mask\|list] / CLEAR` | **Change**: accept entry numbers and ranges (`DEL 2-4`) and an optional description, like Anope. UOP stays (CR extra). |
| C9 | `ACCESS #chan LIST` | `ACCESS #chan ADD <mask> <level> / DEL / LIST / VIEW / CLEAR` and `LEVELS` | **Change in 1.5.4**, where levels become permission sets: ACCESS with level names (SOP, AOP...) or numbers, VIEW, and LEVELS to redefine them. |
| C10 | `AKICK #chan ADD\|DEL\|LIST` | adds `VIEW`, `ENFORCE`, `CLEAR`, entry numbers | **Change** in this step (small). |
| C11 | `BAN`, `UNBAN #chan [nick]`, `INVITE #chan`, `KICK`, `OP/DEOP/VOICE/...`, `UP/DOWN` | `BAN #chan [+expiry] nick\|mask [reason]`, `INVITE #chan [nick]` | **Change**: BAN takes `+expiry` once timed bans exist (1.5.5); INVITE may name someone else (AOP+). |
| C12 | `INFO #chan`, `LIST <pattern>`, `REGISTER #chan <description>` | same (description optional in Anope) | **Change**: description optional. |

## MemoServ

| # | Now in BeeIRCd | Anope 2.1 | Proposal |
| --- | --- | --- | --- |
| M1 | `LIST`, `READ <num>\|NEW\|LAST`, `DEL <num>\|ALL\|READ` | `[#chan]` first for channel memos; numbers, ranges (`1-3,5`), `ALL`, `NEW`, `LAST` | **Change**: ranges and lists; channel memo boxes readable by those with access; `DEL READ` stays (CR extra). |
| M2 | `IGNORE ADD\|DEL\|LIST [nick]` | `IGNORE [#chan] ADD\|DEL\|LIST` | **Change**: the optional channel. |
| M3 | `SET NOTIFY\|LIMIT` | same options | Keep. CANCEL, CHECK, INFO, STAFF, SENDALL come in 1.5.6. |

## OperServ

| # | Now in BeeIRCd | Anope 2.1 | Proposal |
| --- | --- | --- | --- |
| O1 | `AKILL ADD\|DEL\|LIST ...` | `AKILL ADD [+expiry] mask reason / DEL {mask\|num\|list} / LIST / VIEW / CLEAR` | **Change**: numbers, ranges, VIEW, CLEAR. |
| O2 | `NICK FORBID`, `CHAN FORBID`, `FORBIDS` | `FORBID ADD\|DEL\|LIST {NICK\|CHAN\|EMAIL\|REGISTER} [+expiry] entry reason` | **Change**: one FORBID command, also for email domains and names nobody may register. |
| O3 | `CHAN DROP`, `CHAN SETFOUNDER` | `/cs drop #chan` and `/cs set founder #chan nick` with the oper privilege | **Change** (like N7). |
| O4 | `SESSION [LIMIT n \| EXCEPT ...]` | `SESSION LIST <threshold> / VIEW <host>`, `EXCEPTION ADD [+expiry] mask limit reason / DEL / LIST / VIEW` | **Change**: Anope's two commands; the default limit stays in AdminServ SET. |
| O5 | `NEWS ADD\|DEL\|LIST` | `LOGONNEWS`, `OPERNEWS`, `RANDOMNEWS` | **Change**: NEWS becomes LOGONNEWS (NEWS hidden alias); OPERNEWS in 1.5.5 as planned. |
| O6 | `KILL <nick> <reason>`, `MODE #chan <modes>`, `KICK #chan <nick> <reason>` | `KILL <user> [reason]`, `MODE {#chan\|user} <modes>`, `MODE #chan CLEAR [ALL]` | **Change**: reason optional; MODE on users (1.5.5 UMODE folds in here). |
| O7 | `STATS`, `GLOBAL <text>` | `STATS [AKILL\|UPTIME\|ALL\|RESET]`, `GLOBAL` lives in its own Global service | **Keep** our STATS (one calm page) and add `STATS ALL` for the long view. GLOBAL stays in OperServ (CR habit) and works as `/msg Global` too. |

## AdminServ

AdminServ is ours (CR heritage); Anope has no equivalent. No changes from
this pass.

## HELP layout (also 1.5.3)

Anope style: HELP shows the everyday commands with one line each, then the
rest grouped by topic, and `HELP ALL` lists everything. Commands you can't
use (oper or founder only) are hidden from people who can't use them.
Hidden aliases never show up.

## Built differently than planned

- M1: channel memo boxes are not built. A memo to #chan still goes to the
  own box of everyone on its access list; Anope keeps one box per channel.
  Open question for the user.
- O2: forbids have no +expiry yet (the store has no expiry for them).
- O4: exceptions have no +expiry yet; SESSION LIMIT now points to
  /as set session-limit.
- O6: MODE on users and MODE #chan CLEAR come in 1.5.4/1.5.5.
- O7: STATS ALL adds forbids, exceptions, news, memory and goroutines.

## Decisions

N2 decided 11 Nov 2026: host ACCESS goes, as in Anope 2.1. Everything else
follows the agreed rule. Items marked "in 1.5.4" wait for that step.
