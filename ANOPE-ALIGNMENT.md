# Services vs Anope 2.1: alignment list (draft for review)

Rule (agreed 11 Nov 2026): services follow Anope for commands, syntax and
behaviour wherever Anope has the feature; ConferenceRoom extras sit on
top. This list compares what BeeIRCd had with Anope 2.1 (source d2eadd8) and
says what changes. Status 11 Nov 2026: every item not marked "1.5.5" or
"1.5.6" is built (Stage 1.5.3, and the "in 1.5.4" items plus M1's channel
memo boxes in Stage 1.5.4).

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

- O2: forbids have no +expiry yet (the store has no expiry for them).
- O4: exceptions have no +expiry yet; SESSION LIMIT now points to
  /as set session-limit.
- O6: MODE on users comes in 1.5.5 (ChanServ MODE #chan CLEAR is built).
- O7: STATS ALL adds forbids, exceptions, news, memory and goroutines.

## Decisions

N2 decided 11 Nov 2026: host ACCESS goes, as in Anope 2.1. Everything else
follows the agreed rule.

## Stage 1.5.4 (built 11 Nov 2026)

Decided by the user: Anope's level numbers (UOP 1, VOP 3, HOP 4, AOP 5,
SOP 10, QOP 10000, founder above; old databases move over at start),
Anope's channel memo boxes (M1), OVERFLOW moved to 1.5.9 (needs +L).

Built as Anope: ACCESS with levels, LEVELS with Anope's 32 privileges and
their defaults, STATUS/WHY, MODE LOCK/SET/CLEAR, TOPIC
SET/APPEND/PREPEND/LOCK/UNLOCK, ENTRYMSG, ENFORCE, GETKEY, LOG, SUSPEND,
UNSUSPEND, SYNC, SET RESTRICTED PEACE SECUREOPS SECUREFOUNDER SIGNKICK
KEEPMODES PERSIST AUTOOP BANTYPE NOEXPIRE, MemoServ [#chan] on LIST READ
DEL SET LIMIT IGNORE (M2).

Where we differ from Anope, on purpose:

- A privilege at level 0 belongs to everyone in the channel, also those
  not on the list (Anope only counts list entries). That is how AUTOVOP
  (voice everyone) works.
- Everyone on the access list still gets past +i and +k when joining
  (our CR habit); Anope has no such pass.
- LEVELS LIST needs ACCESS_LIST, not FOUNDER: anyone who can see the list
  can see what the levels mean.
- INFO shows mode lock and options only with the INFO privilege, as in
  Anope (it used to show them to everyone).
- Hidden aliases: SET MLOCK (= MODE LOCK SET), SET TOPICLOCK (= TOPIC
  LOCK), SET SECURE (= SECUREOPS), CLEAR (CR: OPS takes every status but
  voice), WHY (= STATUS). CR shortcuts MEMOLEVEL, AUTOVOP and LEAVEOPS
  map onto LEVELS and SECUREOPS.
- With Anope's defaults UOP (1) only has NOKICK and the join pass; INVITE,
  UNBANME and GETKEY start at HOP/AOP. A founder who wants the old CR UOP
  sets `/cs levels #chan set invite 1` and `set unbanme 1`. Kept as Anope
  by the user's decision (11 Nov 2026).
- ENFORCE has no SSLONLY yet (no +z until 1.5.9). LIMIT kicks people with
  no status and no access first (we don't keep join order).
- Not built: FLAGS, CLONE, SEEN, SET EMAIL; BotServ's ASSIGN, SAY,
  BADWORDS, FANTASY. (BotServ was built later, see below.)

## BotServ (built 13 Nov 2026, v1.0)

Follows Anope 2.0.20 bs_*.cpp, fantasy.cpp, greet and botserv.example.conf
defaults (defaults = greet fantasy, minusers 1, botmodes ao, badwordsmax
32, keepdata 10m, gentlebadwordreason yes, fantasycharacter !). Where we
differ, on purpose:
- New oper flag `b` stands for botserv/bot/* privileges; botserv/
  administration is flag `s`.
- Bans after times-to-ban are `~t:` timed bans (BANEXPIRE rounded up to
  whole minutes) instead of a timer.
- AMSG kicker remembers the last line per person. Anope 2.0 keeps it per
  channel membership, so its AMSG check never fires.
- Bots are network services: KICK refuses them, so no rejoin-on-kick.
- No MUTE fantasy command (needs a quiet mode on BAN); no BOTSERV COPY or
  per-bot fantasy prefixes (Anope 2.1).

## Stage 1.5.5a (built 11 Nov 2026)

Decided by the user: IRCd-style /GLINE /ZLINE /SHUN /QLINE /RLINE /ELINE
as short forms of OperServ; SQLINE and FORBID NICK one list; extbans
~a ~r ~m (~q) ~t; split into 1.5.5a and 1.5.5b.

Built as Anope: AKILL, SNLINE, SQLINE with `+expiry` and the same
ADD/DEL/LIST/VIEW/CLEAR; FORBID ADD KIND [+expiry]; EXCEPTION ADD
[+expiry]; CS BAN #chan +expiry.

Ours (Anope leaves these to the IRCd): ZLINE, SHUN, ELINE (as `EXCEPTION
BAN`), CIDR in every IP mask. An SNLINE mask is one word (spaces as ? or
`\s` in a /regex/), where Anope uses `mask:reason`. Timed channel bans are
UnrealIRCd's ~t:minutes:mask, so they work without services too.

## Log channel (built 11 Nov 2026, matches Anope 2.0.20)

Follows Anope **2.0.20 stable** exactly (src/logger.cpp and every
command's Log() call; full mapping in PLAN-anope-logs.md), with the
owner's config: admin, override, commands, users = connect disconnect
nick, other; no channel events. Each line comes from the service it
belongs to (USERS and module lines from Global):

- `COMMAND|ADMIN|OVERRIDE: nick!ident@realhost (account) used NAME [on
  #chan] text`, only where Anope's command logs, with Anope's text. ADMIN
  for OperServ and for NickServ changes to someone else's account;
  OVERRIDE where an oper uses flag s on a channel without access (DROP,
  SET FOUNDER).
- `USERS: ... connected to the network (server)`, `... disconnected from
  the network (server)`, `... changed nick to new`; on by default
  (`log-connects`).
- No prefix: `Access denied for unregistered user MASK with command NAME`,
  `Access denied for user MASK with command NAME` (missing oper flag),
  `Denied access to OperServ from MASK (non-oper)`, `Expiring nickname X
  (group: X) (e-mail: E)`, `Expiring channel #c (founder: X)`,
  `Transferring foundership of #c from deleted nick A to B`, `AKILL on
  mask has expired` (SQLINE/SNLINE/ZLINE/SHUN alike), `Expiring forbid for
  X type T`, `Session exception for X has expired.`; `CS_SUSPEND:
  Expiring suspend for #c`.
- SASL: Anope 2.0.20 logs only EXTERNAL (`M_SASL:`); BeeIRCd has the line
  ready for 1.5.6, PLAIN logs nothing.
- Not logged, as in Anope: INFO, LIST, READ, STATUS, HELP; server notices
  (kills, ban hits, failed /oper) stay server notices for +s opers.
- Ours, in Anope's shape: AdminServ (os_config / os_oper / os_shutdown
  style), ZLINE/SHUN/EXCEPTION BAN (os_sxline style), CS CLEAR.

## Re-check against Anope 2.0.20 (built 11 Nov 2026)

The rule is now Anope **2.0.20 stable**, exactly (the passes above were
against 2.1). Every command in 2.0.20 `modules/commands` and its default
config was compared with 2.1 and with ours; PLAN-anope-2.0.20.md has the
full list. Decided by the user: all five recommendations (B1-B5).

Changed to 2.0.20:

- NickServ ACCESS ADD/DEL/LIST [nick] mask is back (up to 32 user@host
  masks), with SET/SASET SECURE (on by default) and STATUS 0-3. A
  connection on the list is "recognised" only with SECURE off; with SECURE
  on it just isn't renamed. ALIST is no longer an alias of ACCESS.
- SET KILL {ON | QUICK | IMMED | OFF} (60 s, `kill_quick` 20 s, at once;
  IMMED only with `allow_kill_immed`, off by default as in Anope).
  PROTECT and ENFORCE are hidden names for it; old PROTECT delays moved
  to the nearest mode.
- SET HIDE USERMASK (MASK and HOST hidden names).
- DROP <nick>: no code, one nick at a time; dropping the account's name
  gives the account its oldest other nick's name; the last nick takes the
  account. Opers may not drop other opers' nicks (secureadmins).
- UNGROUP makes the nick an account of its own with the same password and
  email, as Anope does (it used to delete the nick); found during the
  build.
- ChanServ SET SECURE has its own meaning again (it was an alias of
  SECUREOPS): with it off, a recognised founder or access-list member gets
  their access without identifying. On by default.
- CS DROP #chan #chan (the name twice instead of a code).
- UNBANME is gone (UNBAN covers yourself); UNBAN with no channel unbans
  you everywhere you have UNBAN. Channels that had changed UNBANME's level
  are named in the server log when the database moves over.
- OperServ STATS AKILL | HASH | UPLINK | UPTIME | ALL | RESET; MODE #chan
  CLEAR [ALL] (ALL leaves opers' status); SNLINE accepts mask:reason.
- Network bans without +expiry last 30 days (`ban_expiry`, Anope's
  autokillexpiry/snlineexpiry/sqlineexpiry, also in the owner's live
  config); +0 keeps one for good, a bare number is days. ELINEs stay until
  removed. Found during the build.

Kept as hidden extras (B5): TOPIC PREPEND, a description on xOP/ACCESS
ADD, GHOST and RELEASE (2.0.20 ships them commented out), WHY.

Where we differ from 2.0.20, on purpose:

- In 2.0.20 the code never reads ChanServ's SECURE flag (AccessFor checks
  NS_SECURE on the channel), so recognised users get no channel access at
  all. We follow what its HELP says instead.
- STATS with no option shows our one-page overview (Anope: uptime only).
- Defaults that are config in both: nick_expire 90 days (Anope 21),
  chan_expire 60 days (Anope 90); the owner's live Anope uses 365 for both.

## Stage 1.5.5b (built 11 Nov 2026, Anope 2.0.20)

Built as Anope 2.0.20 (os_chankill, os_svs, os_mode, os_list, os_info,
os_ignore, os_news, cs_seen), with its log lines:

- CHANKILL [+expiry] #chan reason: an AKILL on *@real host of everyone in
  the channel but opers and services; 30 days without +expiry.
- SVSNICK nick newnick, SVSJOIN nick #chan, SVSPART nick #chan [reason],
  UMODE nick modes (the person is told who changed their modes).
- USERLIST [pattern|#chan [INVISIBLE]], CHANLIST [pattern|nick [SECRET]].
- INFO ADD|DEL|CLEAR nick|#chan text: up to 10 notes, shown to opers in
  NS and CS INFO; a nick's notes belong to its account.
- IGNORE ADD time nick|mask [reason] (bare number = seconds, 0 = for good)
  / DEL / LIST / CLEAR; never applies to opers; expiry logged.
- LOGONNEWS / OPERNEWS / RANDOMNEWS: the newest 3 logon lines at connect
  and oper lines at /oper, one random line per connection in turn, as
  "[Logon News - time] text" from Global (oper news from OperServ).
- ChanServ SEEN nick (connect, nick change both ways, join, part, kick,
  quit; secret channels stay secret), OperServ SEEN STATS | CLEAR time;
  kept 30 days (purgetime).

Where we differ, on purpose:

- Anope's USERLIST INVISIBLE and CHANLIST SECRET filters don't work in
  2.0.20 (a `continue` inside the inner loop); ours do what HELP says.
- SEEN is kept in memory and written to the database once a minute and at
  shutdown (it changes on every join and nick change).
- Oper flags: CHANKILL and IGNORE need k, SVS*, UMODE and OS SEEN need s,
  USERLIST, CHANLIST and INFO any oper, the news commands g.
- DNSBL: Anope's m_dnsbl always AKILLs (4h in the owner's commented-out
  block); ours does what each list says, reject by default (decided for
  1.5.5). Reasons take Anope's %i %n %u %h %g %N %r, plus %b for the list.

## Stage 1.5.6a (built 11 Nov 2026)

Decided by the user (PLAN-1.5.6.md, all six recommendations): mail and
BUDDY come in 1.5.6b; RSEND, SET MESSAGE and SET LANGUAGE are skipped;
Anope 2.0.20's defaults (maxlogins 10, regdelay 30s, SET PRIVATE on for
new accounts).

Built as Anope 2.0.20 (ns_cert, ns_ajoin, ns_group GLIST, ns_suspend,
ns_list, ns_getemail, ns_update, ns_set AUTOOP/DISPLAY/KEEPMODES/PRIVATE,
ns_set_misc URL, ns_saset NOEXPIRE, m_sasl EXTERNAL), with their replies
and log lines: CERT ADD/DEL/LIST [nick] (max 5, secureadmins), identify
by certificate at connect and on a nick change, AJOIN ADD/DEL/LIST (max
10, comma lists, channels that would refuse you are skipped unless your
access lets ChanServ invite you), GLIST, SUSPEND nick [+expiry] [reason]
(logged out and renamed, INFO shows suspended/by/reason/on/expires to
everyone as in the owner's config), UNSUSPEND, LIST pattern [SUSPENDED]
[NOEXPIRE] [UNCONFIRMED] with #from-to, GETEMAIL, UPDATE, SET AUTOOP,
DISPLAY, KEEPMODES, PRIVATE, URL, SASET NOEXPIRE, maxlogins, regdelay.

Where we differ, on purpose:

- MONITOR is the IRCd's (IRCv3), not services.
- NOEXPIRE is per account (Anope: per nick); our expiry is per account.
- KEEPMODES keeps +i, +w and +x only (what people set themselves).
- A suspended nick is renamed when suspended (Anope's Collide) and later
  only told "Nick X is suspended." when someone takes it, as in Anope.
- Fingerprints are SHA-256 of the client certificate, lower case hex;
  CERT ADD for someone else takes the fingerprint with or without colons.
- LIST UNCONFIRMED matches nothing: there are no unconfirmed accounts
  until mail confirmation exists.

## Stage 1.5.6b (built 11 Nov 2026)

Built as Anope 2.0.20 (ms_cancel, ms_check, ms_info, ms_sendall,
ms_staff, ms_set NOTIFY, ns_resetpass, mail.cpp, memoserv senddelay and
maxmemos, nickserv forceemail), with its replies and log lines.

Mail: `[mail] use_mail`, `send_from`, `sendmail_path` (Anope's), `delay`
5m, `dont_quote_addresses`, and the reset/memo templates with Anope's
tokens. The mail looks like Anope's (From, To "name" <addr>, Subject,
Content-Type text/plain UTF-8, 8bit). Delivery results are logged as
Anope's mail lines.

Where we differ, on purpose:

- `smtp_server` (+ user/password, STARTTLS when offered) as well as
  sendmail: BeeIRCd often runs where no sendmail exists (containers).
- CONFIRM only takes RESETPASS codes: there is no mail confirmation of
  registrations yet (Anope's registration = "none" in the owner's config).
- RESETPASS and CONFIRM are hidden from HELP while mail is off.
- STAFF says that it went; Anope 2.0.20 sends the memos and answers
  nothing.
- Old accounts on our old default memo limit (50) moved to 20 once;
  limits people chose themselves stay.
- BUDDY is ConferenceRoom's, not Anope's: up to 32 nicks, NickServ tells
  you when one identifies or their last connection goes, and who is
  online when you identify.

## Stage 1.5.7 HostServ (built 11 Nov 2026)

As Anope 2.0.20 (hs_on, hs_off, hs_group, hs_request, hs_set, hs_del,
hs_list, pseudoclients/hostserv.cpp) and the owner's live hostserv.conf:
every command, Anope's replies and log lines, vhost per nick, shown at
identify and NS UPDATE (OnUserLogin/OnNickUpdate), removed from whoever
shows it on DEL, DELALL and when its nick or account is dropped
(OnDeleteVhost/OnNickDrop). activate_on_set off, memouser/memooper off by
default (options in [hostserv]). Validation = networkinfo vhost_chars,
allow_undotted_vhosts false, disallow_start_or_end ".-", hostlen 64,
userlen 10.

Ours: hostserv/set, /del, /list are oper flag h. A vident changes the
shown ident; network bans and the log keep matching the real ident
(User.RealIdent), channel bans match both. The vhost stays when user mode
+x is changed. Oper vhosts at /oper come in 1.5.8.

## Stage 1.5.8 oper classes (built 11 Nov 2026)

One oper list for IRCd and services. Classes combine UnrealIRCd's operclass
(names, vhost, swhois) and Anope's opertype (what services commands an oper
gets): built-in helper (h = Anope Helper), globop (hk = Services
Operator), admin (ghks = Services Administrator), netadmin (acghks =
Services Root); [[operclass]] blocks add or replace them (AS CLASS).
Anope's OS OPER LIST/INFO/ADD/DEL work on the same list (ADD/DEL need flag
a and answer from AdminServ). Not built: Anope OS LOGIN/LOGOUT (our /oper
already asks for the oper password), Unreal snomasks.

## Stage 1.5.9b NickServ wording (12 Nov 2026)

- Nick protection now sends Anope 2.0.20's own lines (language.h, nickserv.cpp Validate): NICK_IS_SECURE (three lines) or NICK_IS_REGISTERED (two lines), then "If you do not change within <duration>, I will change your nick." (kill / kill quick), or FORCENICKCHANGE_NOW "This nickname has been registered; you may not use it." for KILL IMMED. Bold and underline as Anope sends them.
- IDENTIFY success: "Password accepted - you are now recognized." (ns_identify.cpp).
- **Deliberate difference (owner, 12 Nov 2026):** IDENTIFY when already identified keeps BeeIRCd's "You're already identified as <account>." instead of Anope's "You are already identified.".
