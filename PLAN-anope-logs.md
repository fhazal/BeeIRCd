# Plan: log channel exactly like Anope 2.0.20 (draft for review, 11 Nov 2026)

Source of truth: Anope **2.0.20 stable** (`/home/ircd/anope-2.0.20` on the
homelab) and the owner's live config (`/home/ircd/services/conf/services.conf`):

```
target   = "services.log #services"
admin    = "*"
override = "chanserv/* nickserv/* memoserv/set ~botserv/set botserv/*"
commands = "~operserv/* *"
servers  = "*"
users    = "connect disconnect nick"
other    = "*"
(channels not set)
```

## 1. How Anope writes a line (src/logger.cpp)

| Kind | Prefix and shape | Sent by |
| --- | --- | --- |
| COMMAND | `COMMAND: nick!ident@realhost (account) used NAME [on #chan] <text>` | the service of the command (NickServ for nickserv/*) |
| ADMIN | `ADMIN: ...` same shape; oper commands, and NS/CS commands done on someone else's account | same |
| OVERRIDE | `OVERRIDE: ...` same shape; an oper using a channel command without channel access | same |
| USERS | `USERS: nick!ident@realhost (vhost) (realname) [ip] connected to the network (server)`; `... (realname) disconnected from the network (server)`; `... (realname) changed nick to new` | Global (the log block's bot) |
| module | `M_SASL: ...` (module name in capitals) | NickServ |
| other (no prefix) | `Access denied for user mask with command NAME`, `Expiring nickname ...`, `AKILL on **mask** has expired` | the service it belongs to |

- `(account)` only when identified; `(vhost)` only when the shown host differs; `[ip]` only when the IP differs from the host.
- `NAME` is the command as typed (IDENTIFY, ID, GHOST...).
- Anope logs **only where the command's code calls Log()**, mostly at the point it succeeded. Commands that only show something (INFO, LIST, READ, STATUS, HELP, VIEW) log nothing. Failures are logged only where Anope has a line for them (IDENTIFY, RECOVER, GROUP).
- Not in #services with this config: CHANNEL lines (join/part/kick/mode), account/host/mode/kill USERS lines, oper up/down (those go to globops in the second block).

## 2. What changes in BeeIRCd

| Now (1.5.5a-log) | Anope 2.0.20 | Change |
| --- | --- | --- |
| Every command logged with `→ first reply` | Only Anope's lines, Anope's text | Replace the generic logger with a log call at each Anope point (table below) |
| `M_SASL:` for PLAIN success and failure | 2.0.20 logs SASL only for EXTERNAL (certificates) | Remove PLAIN lines; EXTERNAL line comes with 1.5.6 (CERT) |
| `OPER:` copy of every server notice (kills, ban hits, failed /oper) | Not in Anope's log | **Decision 1** |
| `USERS:` connect/quit/nick only with `log-connects on` | On in the default config and yours | Default ON; quit line without the reason, as Anope |
| `EXPIRE:` / `FOUNDER:` with our words | `Expiring nickname X (group: X) (e-mail: none)`, `Expiring channel #c (founder: X)`, `Transferring foundership of #c from deleted nick A to B`, `Deleting channel #c owned by deleted nick A`, `Expiring suspend for #c`, `AKILL on mask has expired` (same for SQLINE/SNLINE, forbid, exception) | Anope's words, no prefix |
| Nothing | `Access denied for user mask with command NAME` (oper command without the flag), `Access denied for unregistered user mask with command NAME`, `Denied access to OperServ from mask (non-oper)` | Add |

## 3. Command by command

NickServ (COMMAND, or ADMIN when done to another account):

| BeeIRCd | Anope 2.0.20 line text |
| --- | --- |
| REGISTER | `to register NICK (email: E\|none)` |
| IDENTIFY | `and identified for account X` / `and failed to identify to [nonexistent ]account X` |
| LOGOUT | `to logout NICK` (ns_logout) |
| RECOVER (GHOST/RELEASE) | `for NICK` / `with an invalid password for X` / `and was automatically identified to X` |
| GROUP / UNGROUP | `to make NICK join group of T (X) (email: E)` / `and failed to group to T` / `to make NICK leave group of X (email: E)` |
| DROP | `to drop nickname NICK (group: X) (email: E)` (ADMIN when not yours) |
| SET PASSWORD / SASET PASSWORD | `to change their password` / ADMIN `to change the password of X` |
| SET EMAIL | `to change the email of X to E` / `to unset the email of X` |
| SET HIDE | `to change hide OPTION to ON\|OFF for X` |
| SET PROTECT (KILL) | `to set kill on\|quick\|immed for X` / `to disable kill for X` |
| SASET <others> | same texts as SET, ADMIN |
| ALIST, INFO, LIST | nothing |

ChanServ (COMMAND, OVERRIDE when an oper acts without access; `on #chan` added by the format):

| BeeIRCd | Anope 2.0.20 line text |
| --- | --- |
| REGISTER | (no text) |
| DROP | `(founder was: X)` |
| SOP/AOP/HOP/VOP/QOP ADD/DEL/CLEAR | `to add MASK` / `to delete MASKS` / `to clear the access list` |
| ACCESS ADD/DEL/CLEAR | `to add MASK with level N` / `to delete MASK` / `to clear the access list` |
| LEVELS SET/DISABLE/RESET | `to set PRIV to level N` / `to disable PRIV` / `to reset all levels` |
| AKICK ADD/DEL/CLEAR/ENFORCE | `to add MASK: REASON` / `to delete MASK` / `to clear the akick list` / `ENFORCE, affects N users` |
| OP/DEOP/VOICE/... (cs_modes) | `on NICK` |
| UP / DOWN | `to update the status modes of NICK` / `to remove the status modes from NICK` (no channel: `on all channels to ...`) |
| KICK | `for NICK` |
| BAN | `for MASK` |
| UNBAN | `to unban NICK` (no channel: `on all channels`) |
| INVITE | `for NICK` or no text |
| TOPIC SET | `to change the topic to: T` / `to unset the topic` |
| TOPIC LOCK/UNLOCK, SET KEEPTOPIC | `to enable keeptopic` / `to disable keeptopic` (and topiclock alike) |
| MODE LOCK ADD/DEL, MODE SET | `to lock +m` / `to unlock +m` / `to set MODES` |
| ENTRYMSG ADD/DEL/CLEAR | `to add a message` / `to remove a message` / `to remove all messages` |
| ENFORCE | `to enforce secureops\|restricted\|registered only\|bans\|limit` |
| LOG | `to log CMD with method M` / `to change logging for CMD to method M` / `to remove logging for CMD with method M` |
| SET <option> | Anope's text per option (`to enable peace`, `to change the founder from A to B`, `to change the successor from A to B`, `to change the description to D`, `to change the ban type to N`, ...) |
| SET NOEXPIRE, SUSPEND, UNSUSPEND | ADMIN `to enable noexpire`, `(REASON), expires on DATE`, `which was suspended by X for: R` |
| GETKEY, SYNC | (no text) |
| INFO, LIST, STATUS | nothing |
| CLEAR (our ConferenceRoom extra, not in Anope 2.0) | logged like MODE SET: `to set MODES` |

MemoServ: SEND `to send a memo to NICK`; DEL `on memo N` / `on LAST memo` / `on ALL memos`; READ, LIST, IGNORE, SET nothing (Anope has no lines there).

OperServ (ADMIN): AKILL/SQLINE/SNLINE `on MASK (REASON), expires in DURATION [affects N user(s) (P%)]`, `to remove MASK from the list`, `to CLEAR the list`; our ZLINE, SHUN, EXCEPTION BAN use the same shape. KILL `on NICK for REASON`; KICK `on NICK in #c (REASON)`; MODE `MODES on #c`; FORBID `to add a forbid on X of type T` / `to remove forbid on X of type T`; SESSION/EXCEPTION `to set the session limit for MASK to N` / `to remove the session limit exception for MASK`; LOGONNEWS `to add a news item` / `to delete a news item` / `to delete all news items`; GLOBAL (no text); STATS (the sub-command).

AdminServ (ours, Anope has none): log ADMIN in Anope's os_config shape, `to change the configuration value of KEY to VALUE`, and `ADD`/`DEL` for opers like os_oper; RESTART/DIE like os_shutdown (no text).

/GLINE /ZLINE /SHUN /QLINE /RLINE /ELINE run the OperServ command, so they log as that command.

## 4. Decisions

1. **OPER: lines** (copies of server notices: kills, ban hits, failed /oper,
   bans running out). Anope doesn't put these in #services; opers still
   get them as server notices with +s. Recommended: **remove** them from
   #services, following Anope exactly. (Ban expiry still shows, with
   Anope's own `AKILL on mask has expired` line.)
2. **`→ reply`**: removed (not Anope). Recommended yes, as you asked for
   Anope exactly.
3. **log-connects default ON** (Anope's default and your config).
   Recommended yes; busy networks can turn it off with `/as set
   log-connects off`.
4. **Re-check of all services against 2.0.20** (the earlier alignment was
   against 2.1): a separate step right after this one, so this one stays
   reviewable. Recommended yes.

## 5. Tests and gates

- A test per service checking the exact Anope text of the main lines
  (COMMAND, ADMIN, OVERRIDE, USERS, Access denied, expiry), and that
  INFO/LIST/READ log nothing.
- Passwords never appear (IDENTIFY, REGISTER, SET PASSWORD, GROUP,
  RECOVER): Anope never puts them in the text, same here.
- check.sh, -count=6, Linux race, load gate, small-VPS gate, live test
  watched in #services on the test server.
