# Server links: security review (Stage 2g, 12 Nov 2026)

## Who may link

Only a server holding the private key whose fingerprint is in a
`[[link.server]]` block (TLS 1.2+, pinned like UnrealIRCd's `spkifp`). A
password is optional and adds little. Everything below is about what a
server that *is* allowed to link may do: a leaf that is broken, buggy or
taken over.

## What a linked server may say (checked on every line, `link2g_guard.go`)

| Rule | Before 2g | Now |
| --- | --- | --- |
| A line's source is a server behind that link, or one of its users | Many commands didn't check: a leaf could send messages, notices, topics, kicks, channel and user modes, SETHOST/CHGHOST as a user on the hub | Refused and logged once per kind |
| SVSLOGIN, SVSKILL, SVSJOIN, SVSPART, SVSNICK, SVSMODE, SVS2MODE, SVSNOOP only from services (the services server or the server it hangs off) | Any server: log anyone in as any account name, kill, join, part, rename | Refused. A clash rename to the user's UID stays allowed (BeeIRCd settles collisions that way) |
| CHGHOST/CHGIDENT/CHGNAME sent as a server only from services; sent by a user, and SAJOIN/SAPART, only from an IRC operator | Any server or user | Refused |
| KILL sent by a user only from an IRC operator or services (servers may KILL: collisions, server kills) | A non-oper user on a leaf could kill anyone | Refused |
| One malformed TKL line (`TKL + :`) | Crashed the whole server (found by FuzzLinkLine) | Ignored |
| Network bans and Q-lines that match everyone (`*@*`, `*`, `::/0`) | A `*@*` G-line from a leaf removed every user on the hub | Refused, as OperServ refuses them here |
| Server tree: a SID or name already on the network (ours, our services', a jupe, any linked one), bad names, unknown uplink | Already checked | Link dropped (unknown uplink: line refused) |
| A UID for a server not behind the link, or a UID already in use | Already checked | Link dropped |
| Lines over 64 KB | Already checked | Link dropped |
| /oper on a leaf (2f) | — | Asked of the services server; the answer only taken from the link the request went out on; password masked in the link debug log |

Tests: `TestRogueServerSpoofs` (27 cases), `TestRogueServerTreeAndJunk`
(33), `TestRogueRandomLines` (5 seeds x 3,000 random lines through the
real handlers, no panic), fuzz targets `FuzzLinkLine`, `FuzzLinkUID`,
`FuzzLinkSJOIN` (~100 million inputs, 3 min each), `TestRogueQlineEveryone`, `TestMatchesEveryone`, the 2f oper relay
tests. Real traffic checked with the UnrealIRCd 6.2.7 sandbox (2d and 2e
kits, no refused lines) and the 3-server test network (30/30).

## Still trusted (open decision)

- **Accounts in a UID.** When a server introduces a user with an account
  name (the burst after a split, or a new user), services log them into
  that account. UnrealIRCd, InspIRCd and Anope trust linked servers the
  same way; it is what keeps people logged in across a split. A leaf that
  is taken over could therefore claim any account. Tightening it means
  the services server only honours accounts it gave out itself (kept
  across splits and restarts). Owner's call.
- **Oper flags of a server's own users** (`MD bee-operflags`) and network
  bans set by its opers: trusted, as in UnrealIRCd.
- **Volume**: no cap on how many users or channels one server brings.
  Bounded only by memory and the 2g load tests.
