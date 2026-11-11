# Step 1.5.4 plan: ChanServ permission sets (draft for review, 11 Nov 2026)

Rule: follow Anope 2.1 (source d2eadd8); ConferenceRoom extras on top.

## A. Levels become permission sets (the core)
- Each channel gets a LEVELS table: privilege -> minimum level. Defaults are
  Anope's: VOICE 4, OP 5, AUTOOP 5, AUTOVOICE 3, HALFOP 5, AUTOHALFOP 4,
  KICK 4, BAN 4, UNBAN 4, TOPIC 5, INVITE 5, GETKEY 5, ACCESS_LIST 3,
  ACCESS_CHANGE 10, AKICK 10, MEMO 10, SET 9999, PROTECT 9999, OWNER founder,
  NOKICK 1, SIGNKICK 9999, FOUNDER 10000, INFO 9999, SAY 5, FANTASY 3 ...
- Every ChanServ check asks "does this user have privilege X here?" instead
  of "is their level >= AOP?". Hard-coded level checks go away.
- `LEVELS #chan SET <privilege> <level>|FOUNDER|DISABLED`, `LEVELS #chan
  DISABLE <privilege>`, `LIST`, `RESET`.
- `ACCESS #chan ADD <nick> <level> [note]`, `DEL {nick|num|list}`,
  `LIST [mask|list]`, `VIEW`, `CLEAR`. Level = number or a name (VOP, AOP...).
- xOP commands stay and map onto numbers.
- DECISION 1: level numbers. Anope uses VOP 3, HOP 4, AOP 5, SOP 10,
  QOP 10000, founder above all. We use 20/25/30/40/45/50. Recommend: switch
  to Anope's numbers (one migration, like the 1-5 -> 10-50 one), UOP = 1.

## B. New ChanServ commands (Anope)
- `STATUS #chan [nick]` + `WHY #chan nick`: what access someone has and why.
- `ENFORCE #chan SECUREOPS|RESTRICTED|REGONLY|SSLONLY|BANS|LIMIT`.
- `GETKEY #chan`.
- `LOG #chan [command method [status]]`: send ChanServ actions on the
  channel to the channel as notices (MESSAGE/NOTICE method; MEMO method
  goes into the new channel memo box).
- `SUSPEND #chan [+expiry] [reason]` / `UNSUSPEND #chan` (opers, flag s):
  nobody can use the channel, data is kept. `SET NOEXPIRE` for opers.
- `SYNC #chan`: give everyone the modes their access says.
- `MODE #chan LOCK ADD|DEL|SET|LIST` (SET MLOCK = hidden alias),
  `MODE #chan SET <modes>`, `MODE #chan CLEAR [ops|voices|bans|...|all]`
  (CLEAR = hidden alias).
- `TOPIC #chan [SET|APPEND|PREPEND|LOCK|UNLOCK] [text]` (SET TOPICLOCK alias).
- `ENTRYMSG #chan ADD|DEL|LIST|CLEAR`: several lines (old SET ENTRYMSG
  replaces line 1).
- Not in this step: CLONE, SEEN, FLAGS (FLAGS comes later on top of the
  same permission sets), BADWORDS/ASSIGN/SAY (BotServ, later).

## C. SET options (Anope)
- New: `RESTRICTED` (only people on the access list may stay), `PEACE`
  (can't kick/ban someone at your level or above), `SECUREOPS` (only access
  list may hold @), `SECUREFOUNDER`, `SIGNKICK` (kick reason shows who),
  `KEEPMODES` (modes come back after the channel empties), `PERSIST`
  (channel stays open when empty, ChanServ holds it), `AUTOOP`, `BANTYPE`.
- Our SECURE becomes SECUREOPS (SECURE = hidden alias).
- CR extras the old plan listed, handled through the new system so
  there's one way to do each thing:
  - `MEMOLEVEL <level>` = alias of `LEVELS SET MEMO <level>`.
  - `AUTOVOP ON` = alias of `LEVELS SET AUTOVOICE 0` (everyone voiced).
  - `LEAVEOPS ON` = alias of `SECUREOPS OFF`.
  - DECISION 2: `OVERFLOW` (send people to another channel when this one
    is full) needs mode +L, which comes in 1.5.9. Recommend: move it there.

## D. Channel memo boxes (decided 11 Nov 2026: Anope's way)
- One box per registered channel. `/ms send #chan text` needs MEMO privilege.
- `/ms list #chan`, `read #chan <num|list|NEW|LAST>`, `del #chan ...`,
  `info #chan`, `set #chan limit`: need MEMO privilege.
- Notice on identify and on join: "#chan has N new memos."
- Old fanned-out copies stay in people's own boxes; no migration.

## E. Tests and gates (same as every sub-step)
- Unit tests per command + permission-set table test (every privilege,
  every default, LEVELS changes, DISABLED, FOUNDER).
- Store migration test on a copy of the real test-server DB.
- check.sh, -count=6 Windows, Linux race -count=3, load gate 99% < 5 ms.
- Live test on the test server (extend the 34-check script with 1.5.4).
- FEATURES.md + ANOPE-ALIGNMENT.md updated. Then you test, then commit
  "Step 1.5.4: ChanServ permission sets".

## Size
Big step: ChanServ is about 1,350 lines and most checks change. Build order:
A (levels/ACCESS/LEVELS + migration) -> D (memo boxes, uses MEMO) -> C (SET)
-> B (new commands). Each part is tested before the next.
