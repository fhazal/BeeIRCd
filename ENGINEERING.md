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

## Performance targets (agreed 11 Nov 2026)

Measured with the same bots for every server (`beeload`: 50 busy
channels, each bot in 3, 200 bots in a watch channel, every bot talks
every 30-60 s). Each server is pinned to one core with 512 MB (a 1-vCPU
VPS), on the same machine; reference InspIRCd 4.12.1. CPU is split into
the connect rush (first 15 s) and steady chat.

| Gate | Target | BeeIRCd now | InspIRCd 4.12.1 |
| --- | --- | --- | --- |
| 1,000 users | all online; lag p99 < 20 ms; memory < 40 MB; CPU < 5% | met except lag p99: 29 MB; 4%; lag 9 ms / 11-45 ms | 16 MB; 4%; lag 2-8 ms / 10-42 ms |
| 5,000 users | all online in 10 s; lag p50 < 20 ms, p99 < 100 ms; memory < 160 MB; CPU < 50% | met except CPU: 99 MB; lag 5-10 ms / 15-19 ms; chat 54%, connect 47% | 80 MB; chat 57%, connect 23% |
| Loop work | 99% of loop jobs < 5 ms | 99% < 2 ms | – |
| Big server | 5,000 users in the load test (later: 50,000) | passes | – |
| Server-to-server | to be set with linking (Stage 2) | – | – |

The owner's rule (11 Nov 2026): BeeIRCd may use somewhat more than
InspIRCd when that buys performance or features, as long as it installs
on small and big servers and runs smoothly on each; never unreasonable
resources. Matching InspIRCd exactly is not required. Roughly 2x
InspIRCd's memory is the ceiling to aim for.

Not met yet, with the reason: the 1,000-user lag p99 spikes to ~45 ms
on both servers (InspIRCd too), so it looks like the test machine or the
bots, not the server; to be checked. CPU at 5,000 users is 54% against
50%, while InspIRCd needs 57% for the same work. During the connect rush itself
(5,000 joining within 5 s) BeeIRCd's lag p99 reaches ~450 ms for a few
seconds before settling; worth improving with bigger write batches.

When performance work happens (owner's decision, 11 Nov 2026):

| When | What |
| --- | --- |
| Every step | Quick alarm only: the load test in `check.sh` and one small-VPS run (`/tmp/bench2.sh bee`, ~2 min). Numbers noted in HANDOFF. A step that is clearly worse (over ~20% slower or bigger than the step before) gets looked at before commit; no tuning otherwise. |
| End of each stage | One benchmark against InspIRCd, numbers written here. Tuning only if something is badly off. |
| Final stage before release | Full optimisation: profiling, tuning, every target above. |

Design-level problems (work that grows with the square of the users, a
syscall per line) are still fixed when the alarm finds them: they get
harder to remove the more is built on top.

## Performance history

- 11 Nov 2026, first small-VPS run: 3,739 of 5,000 online, lag 8.6 s, CPU
  maxed. Fixed: LUSERS sorted every user at every connect (36% of CPU);
  writer goroutines woken per line instead of once per loop batch; each
  message rebuilt for every member instead of once per kind of client.
  Result: 5,000 online, lag ~12 ms, 204 MB, CPU 65%.
- 11 Nov 2026, second round (measured per phase, pinned to one core):
  running counts instead of scanning every client at every connect
  (LUSERS, local users, sessions per IP, opers for server notices); NAMES
  no longer sorted (InspIRCd doesn't either); readers get a turn before
  writers are woken, so batches are bigger; 1 KB read buffer per
  connection (longer lines put together on the side) and pooled write
  buffers instead of 8.7 KB + 4 KB held by every connection. Result: 99 MB
  (from 204), chat CPU 54% (InspIRCd 57%), connect rush 47% (from 99%).
- Measuring tools: `BEEIRCD_CPUPROFILE=file:seconds` writes a CPU profile,
  `BEEIRCD_MEMSTATS=seconds` logs heap, stacks and goroutines. Both off by
  default. Benchmark script on the homelab: `/tmp/bench2.sh bee|insp`
  (`N=1000` for the small run).
- Later ideas (only if a gate needs them): no writer goroutine for idle
  connections (stacks are now the biggest share, ~30 MB at 5,000);
  bigger write batches during the connect rush.
