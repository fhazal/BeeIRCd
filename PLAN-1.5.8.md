# Step 1.5.8 plan: oper classes and oper extras (draft for review, 11 Nov 2026)

What was agreed for 1.5.8 earlier: oper classes, OPER ENABLE/DISABLE,
AdminServ HISTORY/DIFF/UNDO, `admin_hosts` and oper vhosts (owner: yes,
11 Nov). References: Anope 2.0.20 (`opertype`, `oper`, os_oper) and the
owner's live configs:

- Anope `services.conf`: opertypes Helper (hostserv/\*), Services
  Operator, Services Administrator, Services Root; `oper` blocks bind a
  nick to a type (`require_oper = yes`).
- UnrealIRCd `unrealircd.conf`: `oper` blocks with `operclass` (netadmin,
  globop), `swhois` lines, a `vhost` (netadmin.skinhead.com.my,
  staff.skinhead.com.my), `snomask`; Unreal's default operclasses locop,
  globop, admin, services-admin, netadmin.

BeeIRCd today: an oper entry has raw flags (a admin, c links, g global, h
hosts, k kill, s services), hosts, its own /oper password and a title.
BeeIRCd is IRCd and services in one, so one class covers both, like an
Unreal operclass and an Anope opertype together.

## A. Oper classes

- Named classes in beeircd.conf (`[[operclass]]`), each with: flags,
  title (WHOIS 313 text), optional vhost, optional swhois lines.
- Built in, so a fresh server works without writing any (names from
  UnrealIRCd, contents from Anope's opertypes):

  | Class | Flags | Like |
  | --- | --- | --- |
  | `helper` | h | Anope Helper (HostServ) |
  | `globop` | h k | Unreal globop, Anope Services Operator |
  | `admin` | g h k s | Unreal admin, Anope Services Administrator |
  | `netadmin` | a c g h k s | Unreal netadmin, Anope Services Root |

  The config admin is always `netadmin`.
- AdminServ: `CLASS LIST`, `CLASS <name> ADD|SET|DEL ...` (saved to
  beeircd.conf like SET, comments kept). A class in use can't be deleted.
- Oper entries get `CLASS <name>` instead of flags. Per-entry extras stay
  possible: more flags, own title, own vhost, own swhois. Existing entries
  keep working: their flags become "no class + these flags".

## B. Oper vhost and swhois (UnrealIRCd)

- At /oper the class's (or entry's) vhost becomes the shown host
  (`staff.beeircd.org`); at de-oper the normal host comes back (HostServ
  vhost or cloak). The config admin gets `admin_vhost` from
  `[services]`.
- swhois lines show in WHOIS (numeric 320), e.g. "is a Network
  Administrator", "looks very helpful".

## C. admin_hosts

- `[services] admin_hosts = ["192.168.50.*", "161.142.0.0/16"]`: where the
  config admin may /oper from. Empty = anywhere (as now), with a warning
  in the log at start; the step-7 installer will ask.
- Host matching for every oper entry learns CIDR (it only had globs).

## D. OPER ENABLE / DISABLE

- `AS OPER <nick> DISABLE [reason]` keeps the entry but refuses /oper
  (and de-opers them if online); `ENABLE` lifts it. OPER LIST shows it.

## E. AdminServ HISTORY / DIFF / UNDO

- Every change AdminServ makes to beeircd.conf (SET, MOTD, PORT, OPER,
  CLASS) is recorded: number, who, when, what, old and new value. Kept in
  services.db, last 200.
- `HISTORY [n]` lists the latest changes; `DIFF <num>` shows old and new
  of one; `UNDO <num>` puts the old value back (for SET, MOTD, CLASS and
  OPER changes; PORT changes are undone with PORT ADD/DEL).
- Every UNDO is itself a change in the history.

## F. Anope's OperServ OPER

- `OS OPER LIST`, `OS OPER INFO [class]` for opers; `OS OPER ADD <nick>
  <class>` and `OS OPER DEL <nick>` for flag a, the same list as AdminServ
  OPER (Anope names, so Anope habits work).

## G. Decisions

1. **Classes in beeircd.conf with the four built-in ones** (helper,
   globop, admin, netadmin), editable with AS CLASS. Recommended: yes.
2. **swhois lines** (UnrealIRCd, you use them): yes.
3. **admin_hosts empty = anywhere + log warning** (the installer asks
   later). Recommended: yes.
4. **OS OPER as Anope names** for the same oper list. Recommended: yes.
5. **HISTORY keeps the last 200 changes**; UNDO for SET, MOTD, CLASS and
   OPER. Recommended: yes.
6. **Not in 1.5.8**: Unreal snomasks (categories of server notices; today
   +s gets all of them) and Anope's OS LOGIN/LOGOUT (our /oper already
   asks for the oper password). Snomasks would fit 1.5.9 or later.
   Recommended: leave out for now.

## H. Tests and gates

- Server tests: classes (built-in and custom), /oper with class flags,
  vhost at /oper and back at de-oper (also with a HostServ vhost on),
  swhois in WHOIS, admin_hosts (glob and CIDR), DISABLE/ENABLE (online
  oper loses status), HISTORY/DIFF/UNDO for each kind, OS OPER; old
  entries still work after the upgrade.
- check.sh, -count=6, Linux race, load test, small-VPS alarm, live test.
