# Step 1.5.5 plan: ban engine, extbans, OperServ extras, DNSBL (draft for review, 11 Nov 2026)

Rule: follow Anope 2.1 for OperServ; UnrealIRCd/InspIRCd for what lives in
the IRCd itself (line types, extbans, DNSBL). Today we have AKILL
(user@host wildcards, expiry), FORBID (no expiry), SESSION/EXCEPTION (no
expiry).

## A. One network ban engine

One list, several kinds, all with `+expiry` (+30m, +12h, +7d; none = until
removed), who set it, when, and a reason. Matches the real host and IP,
never the cloak. Adding a line applies at once to everyone connected.

| Kind | Matches | Effect | OperServ (Anope name) | IRCd command |
| --- | --- | --- | --- | --- |
| AKILL | user@host, host may be CIDR (`*@10.0.0.0/8`) | disconnect, refuse | `AKILL` | `/GLINE` |
| ZLINE | IP or CIDR only | refused before anything else (no DNS, no ident) | `ZLINE` (ours) | `/ZLINE` |
| SQLINE | nick mask | nobody may use the nick (opers can) | `SQLINE` = today's FORBID NICK | `/QLINE` |
| SNLINE | realname mask (`/regex/` too) | disconnect, refuse | `SNLINE` | `/RLINE` |
| SHUN | user@host or CIDR | stays connected but can't talk, join, change nick | `SHUN` (ours) | `/SHUN` |
| ELINE | user@host or CIDR | exempt from all of the above and DNSBL | `EXCEPTION BAN` | `/ELINE` |

- Same sub-commands everywhere: `ADD [+expiry] mask reason`, `DEL
  {mask|num|list}`, `LIST`, `VIEW`, `CLEAR`.
- FORBID and session EXCEPTION get `+expiry` too (the O2/O4 gaps).
- Snotices for add, remove, expiry and every hit; STATS counts per kind.
- Stored in one `netbans` table; the server keeps the live copy (as AKILL
  does now). Ready to be sent over links in Stage 2.
- DECISION 1: also give opers the IRCd-style commands (/GLINE /ZLINE
  /SHUN /QLINE /RLINE /ELINE) as short forms of the OperServ ones?
  Recommended: yes, UnrealIRCd opers type them by habit; same list behind
  both.
- DECISION 2: SQLINE and FORBID NICK become one list (FORBID NICK stays
  as a name for it). Recommended: yes, two lists for the same thing would
  disagree sooner or later.

## B. Channel extbans and timed bans

- `~a:account` (`~a:*` = anyone identified, `~a:0` = not identified),
  `~r:realname` and `~m:mask` (mute: can stay, can't talk). `~q:` works too
  for UnrealIRCd users.
- They work in +b, +e and +I, and in ChanServ AKICK. 005 says
  `EXTBAN=~,amqrt`.
- Timed bans UnrealIRCd's way: `+b ~t:30:*!*@host` lifts itself after 30
  minutes; `/cs ban #chan +1h nick [reason]` sets one (the `+expiry` Anope
  has).
- DECISION 3: extban letters ~a ~r ~m (+ ~q alias) and ~t. Recommended
  as written.

## C. OperServ extras (Anope 2.1 names)

- `CHANKILL [+expiry] #chan reason`: AKILL everyone in a channel (not
  opers).
- `SVSNICK nick newnick`, `SVSJOIN nick #chan`, `SVSPART nick #chan
  [reason]`.
- `UMODE nick modes`; `MODE` keeps doing channels.
- `USERLIST [pattern | #chan]`, `CHANLIST [pattern]`.
- `INFO ADD|DEL|CLEAR target text`: oper notes on a nick or channel, shown
  to opers in NS/CS INFO.
- `IGNORE ADD +expiry {nick|mask} [reason] / DEL / LIST / CLEAR`: services
  ignore someone (spam to the bots).
- `OPERNEWS` (shown at /oper) and `RANDOMNEWS` (one random line at
  connect) next to LOGONNEWS.
- `SEEN nick` (ChanServ, Anope's cs_seen): when someone was last seen and
  doing what; `OS SEEN STATS|CLEAR`.
- WHY was built in 1.5.4 (CS WHY). JUPE, NOOP and DEFCON wait for linking
  (Stage 2).

## D. DNSBL (you asked, 11 Nov 2026)

- beeircd.conf, one block per list (like UnrealIRCd blacklist{}):

  ```
  [[dnsbl]]
  name     = "dnsbl.dronebl.org"
  replies  = [3, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16]
  action   = "reject"        # reject | zline | akill | shun
  ban_time = "24h"           # for zline/akill/shun
  reason   = "Your IP $ip is listed in DroneBL: https://dronebl.org/lookup?ip=$ip"

  [[dnsbl]]
  name     = "rbl.efnetrbl.org"
  replies  = [1, 5]          # 4 = TOR, add it to block TOR
  ```

- Lookups run off the loop when someone connects (IPv4, and IPv6 for
  lists that have it); registration waits at most 2 seconds, then lets
  them in. Results cached (hit 1 hour, clean 10 minutes). ELINE and
  session-exception IPs are skipped, LAN IPs never looked up.
- AdminServ: `DNSBL LIST`, `DNSBL CHECK <ip>` (test a lookup), `DNSBL
  ENABLE|DISABLE <name>`; STATS shows hits per list.
- DECISION 4: default action. Recommended: `reject` (refuse that one
  connection, nothing stored). zline 24h hurts people on dynamic IPs who
  get a listed address by chance; it stays an option per list.
- The test server gets DroneBL + EFnetRBL with reject.

## E. Tests and gates (as every sub-step)

- Unit tests per kind and command, CIDR v4/v6 matching table, expiry,
  extban table test, timed bans, DNSBL with a fake resolver (hit, miss,
  timeout, cache, exemptions).
- check.sh, -count=6 Windows, Linux race -count=3, load gate 99% < 5 ms
  (also with 5,000 connects through a DNSBL that answers slowly).
- Live test on the test server; FEATURES.md + ANOPE-ALIGNMENT.md.

## Size

Bigger than 1.5.4. DECISION 5: split it like 1.5.3 into two commits:
**1.5.5a** = A + B (ban engine, CIDR, expiry everywhere, extbans, timed
bans), **1.5.5b** = C + D (OperServ extras, DNSBL). Each gets its own
tests, live test and your OK. Recommended: yes.
