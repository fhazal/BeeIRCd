# BeeIRCd – Go Engineering Philosophy

BeeIRCd uses Go as its primary implementation language. The choice of Go
is to be used properly: its concurrency model, networking, standard
library, type system and cross-platform builds. BeeIRCd is engineered as
a serious production-grade IRC daemon, not a simple Go chat server.

## Requirements

1. A scalable, event-driven architecture with carefully controlled
   concurrency.
2. IRC protocol correctness, message ordering and network consistency.
3. No unnecessary goroutines, channel traffic or memory allocations.
4. Profiling, benchmarking and load testing to check performance
   assumptions.
5. Race detection and thorough automated tests.
6. Clean boundaries between IRC core, protocol extensions, networking,
   persistence and services, so each stays maintainable.
7. Graceful shutdown, config reloads where practical, reliable long
   running.
8. Security, resilience and predictable resource use first.
9. Performance checked under realistic load: thousands of connections at
   once, and server-to-server traffic.

Don't optimise prematurely: set measurable targets and benchmark before
adding complexity. Recommend modern Go practice and architecture
improvements wherever they clearly help BeeIRCd.

BeeIRCd must run well on a cheap VPS and scale up on big servers, without
limiting what it can do. InspIRCd, UnrealIRCd and Anope are studied for
behaviour and techniques; all BeeIRCd code is our own.

## Performance targets (PROPOSED 11 Nov 2026, waiting for the owner's OK)

Measured with the same bots for every server (`beeload`: 50 busy
channels, each bot in 3, 200 bots in a watch channel, every bot talks
every 30 s). Reference on the same machine and limits: InspIRCd 4.12.1.

| Gate | Where | Target | Now (11 Nov 2026) | InspIRCd 4.12.1 |
| --- | --- | --- | --- | --- |
| Small VPS, 1,000 users | 1 CPU, 512 MB | all online; lag p99 < 20 ms; memory < 40 MB; CPU < 5% | 1.7 ms / 6 ms; 39 MB; 4% | 16 MB; 3% |
| Small VPS, 5,000 users | 1 CPU, 512 MB | all online in 10 s; lag p50 < 20 ms, p99 < 100 ms; memory < 160 MB (about 2x InspIRCd); CPU < 50% | 12 ms / 19 ms; 204 MB; 65% | 2–13 ms; 78 MB; ~6% |

The owner's rule (11 Nov 2026): BeeIRCd may use somewhat more than
InspIRCd when that buys performance or features, as long as it installs
on small and big servers and runs smoothly on each; never unreasonable
resources. Matching InspIRCd exactly is not required.
Roughly 2x InspIRCd's memory is the ceiling to aim for.
| Loop work | any | 99% of loop jobs < 5 ms | 99% < 2 ms | – |
| Big server | 28 cores | 5,000 users in the load test (later: 50,000) | passes | – |
| Server-to-server | Stage 2 | to be set with linking | – | – |

Every step runs the small-VPS gate (`/tmp/vpsbench.sh` on the homelab)
next to `check.sh`, the race test and the load test. A step that makes a
gate worse needs a reason.

## Performance history

- 11 Nov 2026, first small-VPS run: 3,739 of 5,000 online, lag 8.6 s, CPU
  maxed. Fixed: LUSERS sorted every user at every connect (36% of CPU);
  writer goroutines woken per line instead of once per loop batch; each
  message rebuilt for every member instead of once per kind of client.
