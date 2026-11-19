#!/bin/sh
# Heavy link test, for a test machine: the 3-server test net under load,
# leaf2 cut off and given back again and again, and after every return
# netagree checks that all three servers see the same network.
#
#   sh heavy.sh            1 hour, 1700 bots on each server (5100 in all)
#   DUR=600 N=300 sh heavy.sh   a short try
#
# Needs in this folder: beeircd, beeload, netagree and livecheck (linux
# builds) as testnet.sh does. Writes heavy.log and heavy-summary.txt, and
# takes the test net down at the end (KEEP=1 keeps it).
set -u
cd "$(dirname "$0")"
DUR=${DUR:-3600} N=${N:-1700} CUT=${CUT:-40}
ADDRS=127.0.0.1:27011,127.0.0.1:27012,127.0.0.1:27013
LOG=heavy.log
: >"$LOG"
say() { echo "$(date '+%H:%M:%S') $*" | tee -a "$LOG"; }

sh testnet.sh up >>"$LOG" 2>&1 || { say "FAIL: the test net did not come up"; exit 1; }
say "test net up; starting $N bots on each server"
pids=""
for i in 1 2 3; do
	./beeload -addr 127.0.0.1:2701$i -n "$N" -ramp 100 -prefix "b$i" -watchers 50 -duration "$((DUR + 900))s" >"load$i.log" 2>&1 &
	pids="$pids $!"
done
sleep $((N / 100 + 30))
if ./netagree -addrs "$ADDRS" >>"$LOG" 2>&1; then say "before any cut: AGREE"; else say "before any cut: DISAGREE (see above)"; fi

syncs() { docker logs bee-hub 2>&1 | grep -c "Received end of netburst from leaf2.beenet.test ("; }
end=$(($(date +%s) + DUR))
cycle=0 ok=0 bad=0 norelink=0
while [ "$(date +%s)" -lt "$end" ]; do
	cycle=$((cycle + 1))
	before=$(syncs)
	sh testnet.sh cut >>"$LOG" 2>&1
	sleep "$CUT"
	sh testnet.sh heal >>"$LOG" 2>&1
	t=0
	while [ "$(syncs)" -le "$before" ] && [ $t -lt 360 ]; do sleep 2; t=$((t + 2)); done
	if [ "$(syncs)" -le "$before" ]; then
		norelink=$((norelink + 1))
		say "cycle $cycle: leaf2 did NOT link again within 6 minutes"
		continue
	fi
	sleep 15
	if ./netagree -addrs "$ADDRS" >>"$LOG" 2>&1; then
		ok=$((ok + 1))
		say "cycle $cycle: back after ${t}s, AGREE"
	else
		bad=$((bad + 1))
		say "cycle $cycle: back after ${t}s, DISAGREE (see above)"
	fi
done

for p in $pids; do kill "$p" 2>/dev/null; done
sleep 10
panics=$(for n in hub leaf1 leaf2; do docker logs "bee-$n" 2>&1 | grep -c -i "panic\|fatal error"; done | paste -sd+ | bc)
mem=$(docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' bee-hub bee-leaf1 bee-leaf2 | tr '\n' ';')
{
	echo "heavy link test $(date '+%Y-%m-%d %H:%M')"
	echo "bots: $N per server, cut: ${CUT}s each, run: ${DUR}s"
	echo "cycles: $cycle  agree: $ok  disagree: $bad  no relink: $norelink"
	echo "panics in the logs: $panics"
	echo "memory: $mem"
	if [ "$bad" -eq 0 ] && [ "$norelink" -eq 0 ] && [ "$panics" -eq 0 ] && [ "$cycle" -gt 0 ]; then echo "RESULT: PASS"; else echo "RESULT: FAIL"; fi
} | tee heavy-summary.txt | tee -a "$LOG"
[ "${KEEP:-0}" = 1 ] || sh testnet.sh down >>"$LOG" 2>&1
