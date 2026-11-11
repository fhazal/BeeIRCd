# Step 1.5.7 plan: HostServ (draft for review, 11 Nov 2026)

Source of truth: Anope **2.0.20** (`modules/commands/hs_*.cpp`,
`modules/pseudoclients/hostserv.cpp`) and the owner's live
`hostserv.conf` (every hs_* module loaded, `activate_on_set = no`,
hs_request `memouser`/`memooper` commented out = off) and
`networkinfo` (`vhost_chars` letters, digits, `.` `-`;
`allow_undotted_vhosts = false`; `disallow_start_or_end = ".-"`;
`hostlen = 64`; `userlen = 10`).

Today BeeIRCd has cloaks (+x) and the chghost cap, but no vhosts.

## A. Data

- A vhost belongs to a **nick** (Anope's NickAlias), not the account:
  each grouped nick may have its own. Stored: optional vident, host,
  who set it, when. One pending request per nick.
- Dropping a nick drops its vhost and request; dropping the account
  drops them all.
- New tables `vhosts`, `vhost_requests`, kept in the services cache
  like the rest.

## B. Commands (Anope 2.0.20 names, syntax, replies, log lines)

| Command | Who | What |
| --- | --- | --- |
| `ON` | identified user | turn on the vhost of the nick in use |
| `OFF` | identified user | turn it off; the cloak (or real host) comes back |
| `GROUP` | identified user | copy the vhost of the nick in use to every nick in the group |
| `REQUEST [ident@]host` | identified user | ask for a vhost (one at a time, same send delay as memos) |
| `SET <nick> [ident@]host` | oper flag h | give a nick a vhost |
| `SETALL <nick> [ident@]host` | oper flag h | same for the whole group |
| `DEL <nick>` / `DELALL <nick>` | oper flag h | remove the vhost of a nick / of its group |
| `LIST [pattern \| from-to]` | oper flag h | list vhosts |
| `WAITING` | oper flag h | list pending requests |
| `ACTIVATE <nick>` / `REJECT <nick> [reason]` | oper flag h | approve or turn down a request |
| `HELP` | everyone | Anope HELP layout, oper commands hidden from non-opers |

Anope's `hostserv/set`, `/del` and `/list` permissions map to our oper
flag **h** (hosts), which exists already. Aliases `/hs` like `/ns`.

## C. When a vhost shows

- On identify (password, SASL, certificate) and on `NS UPDATE`, if the
  nick in use has a vhost: it is applied and HostServ says `Your vhost of
  host is now activated.` (Anope's OnUserLogin).
- On `SET`/`SETALL`/`ACTIVATE`: only applied at once when
  `activate_on_set` is on; default **off**, like your config (they see it
  at their next identify or `/hs on`).
- On a nick change to another nick with its own vhost, after identify:
  that vhost (Anope's OnNickUpdate).
- Shown host changes go through the event layer (`HostChange`), so
  chghost clients see a CHGHOST and others a quit/rejoin-free update,
  exactly as cloaks do today. WHOIS shows the real host to opers
  (378) as now.
- Bans keep matching the real host, the cloak and the vhost.

## D. Checks (Anope's)

- Host: only `vhost_chars`, at least one dot (`allow_undotted_vhosts =
  false`), not starting or ending with `.` or `-`, at most 64 chars.
  vident: at most 10 chars, ident characters only.
- Requests and `SET` refuse an invalid host with Anope's wording.
- New config in `[hostserv]`: `vhost_chars`, `allow_undotted`,
  `disallow_start_or_end`, `activate_on_set` (off), `memo_user` (off),
  `memo_opers` (off). Defaults = your Anope config.

## E. Logs (#services, Anope's exact lines)

`COMMAND: ... used REQUEST to request new vhost [ident@]host`, `COMMAND:
... used ON to enable their vhost of host`, `COMMAND: ... used OFF to
disable their vhost`, `COMMAND: ... used ACTIVATE for NICK for vhost
host`, `ADMIN: ... used SET to set the vhost of NICK to host`, `ADMIN:
... used DEL for user NICK`, `ADMIN: ... used DELALL for all nicks in
group X`.

## F. Decisions

1. **vident (ident@host vhosts).** Anope and UnrealIRCd support them;
   it needs the shown ident to change too (CHGHOST already carries it).
   Recommended: **yes**.
2. **Request memos** to the user (approved/rejected) and to opers (new
   request): built, but **off** by default like your config; the
   `[hostserv]` options turn them on. Recommended: yes.
3. **Oper vhosts at /oper** (staff.beeircd.org style) stay in **1.5.8**
   with oper classes, as planned. Recommended: yes.

## G. Tests and gates

- Server tests per command (own vhost, group, request flow, oper
  commands refused without flag h, validation, activation on identify,
  OFF restores the cloak, CHGHOST to chghost clients, drop removes
  vhosts, exact log lines).
- check.sh, -count=6, Linux race, load test, small-VPS alarm
  (`bench2.sh bee`), live test on the test server.
