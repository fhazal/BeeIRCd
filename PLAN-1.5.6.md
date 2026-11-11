# Step 1.5.6 plan: MONITOR, the rest of NickServ and MemoServ (draft for review, 11 Nov 2026)

Rule: Anope **2.0.20** exactly (source on the homelab), with the owner's
live config (`/home/ircd/services/conf`) where it picks a value. IRCv3
for MONITOR. ConferenceRoom extras on top.

## A. MONITOR (IRCv3)

- `MONITOR + nick,nick` / `- nick` / `C` / `L` / `S`; numerics 730
  (online), 731 (offline), 732/733 (list), 734 (list full).
- `005 MONITOR=100`; online/offline sent on connect, quit and nick change.
- Lists live on the connection only (IRCv3), so they work for remote users
  in Stage 2 the same way.

## B. NickServ (Anope 2.0.20 names and syntax)

| Command | What it does | Live config |
| --- | --- | --- |
| `CERT ADD [nick] [fingerprint]`, `DEL [nick] fingerprint`, `LIST [nick]` | TLS client certificate fingerprints (SHA-256). Connecting with one identifies you to that nick's account; `SASL EXTERNAL` logs in with it before registration. WHOIS 276 shows your fingerprint to you and opers. | `max = 5` |
| `AJOIN ADD [nick] #chan [key]`, `DEL [nick] #chan`, `LIST [nick]` | Channels you are joined to when you identify. | `ajoinmax = 10` |
| `GLIST [nick]` | Your nicks, with when each expires (opers: anyone's). | on |
| `SUSPEND nick [+expiry] [reason]`, `UNSUSPEND nick` (opers, s) | The account can't be identified to; whoever uses the nick is renamed; INFO shows by, reason, on, expires. `LIST pattern [SUSPENDED] [NOEXPIRE] [UNCONFIRMED]`. | `show = "suspended, by, reason, on, expires"`, no default expiry |
| `SET AUTOOP {ON\|OFF}` | ChanServ gives you your status on join (on by default). | on |
| `SET DISPLAY nick` | Which of your nicks is the account's name. | on |
| `SET KEEPMODES {ON\|OFF}` | Your user modes come back when you identify. | on |
| `SET PRIVATE {ON\|OFF}` | Hide you from NickServ LIST (on by default, `ns_private`). | on |
| `SET URL [url]` | A URL in INFO (`ns_set_misc`). | on |
| `SASET` for each of these, `SASET NOEXPIRE nick {ON\|OFF}` | opers with s | on |
| `GETEMAIL email` (opers) | Accounts using an email (wildcards). | on |
| `UPDATE` | Re-checks you: memos, channel status, oper status. | on |

Also from 2.0.20's defaults (and the owner's config) that we still do
differently:

- `maxlogins = 10`: at most 10 connections identified to one account.
- `regdelay = 30s`: REGISTER only after being connected 30 seconds.
- The `ns_private`, `memo_signon`, `memo_receive`, `autoop` account
  defaults (we have hide email/mask and kill already).

## C. MemoServ (Anope 2.0.20)

| Command | What it does | Live config |
| --- | --- | --- |
| `CANCEL {nick\|#chan}` | Takes back your last memo there while it is unread. | on |
| `CHECK nick` | Was your last memo to nick read, and when. | on |
| `INFO [nick\|#chan]` | How many memos, the limit, notify settings (opers: anyone's). | on |
| `SENDALL text` (opers, g) | A memo to every account. | on |
| `STAFF text` (opers) | A memo to every oper. | on |
| `SET NOTIFY {ON\|LOGON\|NEW\|OFF}` | When you hear about memos (ours is ON/OFF now). | on |

And the defaults: `maxmemos = 20` per account (ours is 50), `senddelay =
3s` between two SENDs from one person.

## D. Decisions (recommendation first)

1. **Email.** Anope uses mail for `RESETPASS` (lost password),
   `SET NOTIFY MAIL` and REGISTER confirmation; the owner's config has
   `usemail = yes`, `forceemail = yes` (REGISTER needs an email),
   `registration = "none"` (no confirm mail). BeeIRCd sends no mail today.
   Recommended: **add mail now**: a `[mail]` block (SMTP server, or a
   sendmail path like Anope's; off by default), with RESETPASS and NOTIFY
   MAIL shown only when mail is on, and `force_email` (on by default, as
   in Anope) for REGISTER. Without mail: RESETPASS can't exist, and opers
   keep using SASET PASSWORD.
2. **BUDDY** (ConferenceRoom): your list of friends, kept with your
   account; NickServ tells you when one identifies or leaves. Anope has
   none; MONITOR does the same in clients that support it (mIRC
   doesn't). Recommended: **build it as a CR extra**, on the same code as
   MONITOR, max 32 buddies.
3. **RSEND (read receipts)**: off in the owner's config and in Anope's
   default (`#command ... RSEND`). Recommended: **skip**; CHECK covers
   "was it read".
4. **SET MESSAGE** (services talk by PRIVMSG): only shown when
   `useprivmsg` is on, which it isn't in Anope's default or the owner's
   config. **SET LANGUAGE**: we have English only. Recommended: **skip
   both** (hide what can't be used).
5. **The defaults in B and C** (maxlogins 10, regdelay 30s, maxmemos 20,
   senddelay 3s, SET PRIVATE on): Recommended: **follow 2.0.20**, each
   as a config value. Existing accounts keep a memo limit above 20 if
   they set it themselves; the default ones move to 20.
6. **Size**: split like 1.5.5 into **1.5.6a** = A + B (MONITOR, CERT and
   SASL EXTERNAL, the NickServ commands) and **1.5.6b** = C + mail +
   BUDDY. Recommended: yes.

## E. Tests and gates

- Unit tests per command; TLS client-cert tests (certificate made in the
  test) for CERT, auto-identify and SASL EXTERNAL; MONITOR numerics;
  mail with a fake SMTP server.
- Log lines as Anope 2.0.20 (ns_cert, ns_ajoin, ns_suspend, ms_* lines
  and `M_SASL: ... identified to account X using SASL EXTERNAL`).
- check.sh, -count=6 Windows, Linux race -count=3, load gate, quick perf
  alarm (5k ~106 MB, chat ~52%, lag 5-8 ms); live test on the test
  server; FEATURES.md + ANOPE-ALIGNMENT.md.
