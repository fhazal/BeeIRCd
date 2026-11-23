#!/bin/sh
# Real-machine test of the seamless upgrade (the door keeper hand-over), on
# a host with LXD. Needs a kit made by scripts/handovertest-kit.sh. In ONE
# container (no network, nothing on the host): install 1.4.0-test as the
# service, put 50 bots on it (TLS and plain, shared channels, NickServ and
# SASL accounts, an oper), then switch three times while they stay:
# beeircd update (to 1.4.1-test), beeircd update --rollback, /as restart.
# After each switch: everyone still there, no ERROR/QUIT seen, every chat
# message arrives, channels, accounts, away, monitor and caps unchanged.
# Then tools/livecheck. Prints a PASS/FAIL table, exits 1 on any FAIL,
# keeps the logs in $HOVLOGS (default ./hov-logs), deletes the container.
#
#   scripts/handovertest.sh <kit dir> [image]
set -u
kit=$(cd "$1" && pwd)
image=${2:-deb13}
n="bee-hov-$image"
logs=${HOVLOGS:-$PWD/hov-logs}
mkdir -p "$logs"
tab="$logs/table.txt"
: > "$tab"
fail=0
X() { lxc exec "$n" -- "$@"; }
row() { # row <check> <PASS|FAIL> <numbers>
	printf '%-46s %-4s %s\n' "$1" "$2" "$3" >> "$tab"
	echo "$2 $1: $3"
	[ "$2" = PASS ] || fail=1
}
pf() { if [ "$1" = 0 ]; then echo PASS; else echo FAIL; fi; }
status() { X /root/hovcheck status 2>&1 || true; }
field() { echo "$1" | sed -n "s/.*$2=\([^ ]*\).*/\1/p"; }
finish() {
	set +e
	if lxc info "$n" >/dev/null 2>&1; then
		X journalctl -u beeircd --no-pager > "$logs/journal.txt" 2>&1
		lxc file pull -q "$n/root/hovbots.log" "$logs/" 2>/dev/null
		lxc file pull -q "$n/root/hovrun.out" "$logs/" 2>/dev/null
		lxc delete -f "$n" >/dev/null 2>&1
	fi
	lxc info "$n" >/dev/null 2>&1 && echo "WARNING: container $n is still there" || echo "container $n deleted"
	echo
	echo "===== hand-over test ($image)"
	printf '%-46s %-4s %s\n' CHECK RESULT NUMBERS
	cat "$tab"
	echo "logs in $logs"
}
trap finish EXIT

echo "===== $image: container $n"
lxc delete -f "$n" >/dev/null 2>&1 || true
timeout 180 lxc launch -q "$image" "$n" || exit 1 # a launch once hung for 10 minutes
for i in $(seq 60); do X systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
lxc file push -q -r "$kit/r2" "$n/root/"
lxc file push -q --mode 0755 "$kit/v1" "$n/root/beeircd"
for t in installcheck hovcheck livecheck; do lxc file push -q --mode 0755 "$kit/$t" "$n/root/$t"; done

echo "--- 1. install 1.4.0-test as the service"
printf 'irc.hov.test\nHovNet\nBee\nhov-pass-1\nhov-pass-1\n\n6667 6697\n2\n1\n1\ny\ny\ny\n' | X /root/beeircd install > "$logs/install.txt" 2>&1 || true
grep -q "" "$logs/install.txt" && tail -5 "$logs/install.txt"
upwait() { for i in $(seq 60); do X /root/hovcheck status >/dev/null 2>&1 && break; sleep 1; done; sleep 2; }
upwait
X /root/installcheck -tls 127.0.0.1:6697 -admin Bee -pass hov-pass-1 > "$logs/installcheck.txt" 2>&1
row "install + admin (NickServ, /oper)" "$(pf $?)" "$(tail -1 "$logs/installcheck.txt")"
# Releases from inside the container; test versions need --rc, so rc_url.
# Raise the per-IP limits for livecheck (its bots all come from 127.0.0.1).
X sh -c 'sed -i "/^\[update\]/a rc_url = \"http://127.0.0.1:8099/r2\"" /etc/beeircd/beeircd.conf'
X sh -c 'grep -q "^\[limits\]" /etc/beeircd/beeircd.conf || printf "\n[limits]\n" >> /etc/beeircd/beeircd.conf'
X sh -c 'sed -i "/^session_limit\|^connect_rate\|^max_unregistered_per_ip/d; /^\[limits\]/a session_limit = 100\nconnect_rate = \"off\"\nmax_unregistered_per_ip = 100" /etc/beeircd/beeircd.conf'
X beeircd check -config /etc/beeircd/beeircd.conf > "$logs/check.txt" 2>&1 || { echo "config check failed:"; cat "$logs/check.txt"; }
X systemctl restart beeircd
upwait
X sh -c 'nohup /root/installcheck -serve /root -addr 127.0.0.1:8099 >/dev/null 2>&1 &'
st=$(status)
gate=$(field "$st" gate)
[ -n "$gate" ] && [ "$gate" != off ] && [ "$gate" != none ] && r=0 || r=1
row "service runs behind the door keeper" "$(pf $r)" "$st"
case "$st" in *version=1.4.0-test*) r=0 ;; *) r=1 ;; esac
row "running 1.4.0-test" "$(pf $r)" "$(field "$st" version)"

echo "--- 2. 50 bots"
X sh -c 'cd /root && nohup /root/hovcheck run -n 50 -pass hov-pass-1 -nspass Bee-ns-password -log /root/hovbots.log > /root/hovrun.out 2>&1 &'
sleep 1
X /root/hovcheck ask ready > "$logs/ready.txt" 2>&1
r=$?
cat "$logs/ready.txt"
row "50 bots connected and set up" "$(pf $r)" "$(grep '^SUMMARY' "$logs/ready.txt" || echo 'no summary')"
[ $r = 0 ] || exit 1

# after <name> <old pid> <want version> <switch output file>: the checks
# every switch must pass.
after() {
	name=$1 oldpid=$2 want=$3 out=$4
	st=$(status)
	echo "    status: $st"
	pid=$(field "$st" pid) ver=$(field "$st" version) kept=$(field "$st" kept) of=$(field "$st" of)
	prob=$(echo "$st" | sed -n 's/.*problem="\(.*\)"/\1/p')
	[ "$ver" = "$want" ] && [ "$pid" != "$oldpid" ] && r=0 || r=1
	row "$name: new server process, $want" "$(pf $r)" "pid $oldpid -> $pid, version $ver"
	[ "$kept" = "$of" ] && [ "${kept:-0}" -ge 50 ] && [ -z "$prob" ] && r=0 || r=1
	row "$name: status handover kept == of (>= 50)" "$(pf $r)" "kept=$kept of=$of problem=\"$prob\""
	if [ -n "$out" ]; then
		line=$(grep -o '[0-9]* of [0-9]* \(people\|person\) stayed connected' "$out" || true)
		k=$(echo "$line" | cut -d' ' -f1) m=$(echo "$line" | cut -d' ' -f3)
		[ -n "$line" ] && [ "$k" = "$m" ] && [ "$k" -ge 50 ] && r=0 || r=1
		row "$name: says N of M stayed connected, N == M" "$(pf $r)" "${line:-no such line}"
	fi
	X /root/hovcheck ask "check?label=$name" > "$logs/check-$name.txt" 2>&1
	r=$?
	cat "$logs/check-$name.txt"
	row "$name: bots connected, chat, state unchanged" "$(pf $r)" "$(grep '^SUMMARY' "$logs/check-$name.txt" || echo 'no summary'); $(grep 'still connected' "$logs/check-$name.txt" | cut -c6- | cut -d: -f2 | cut -c2-22)"
	grep -h 'messages arrived\|longest PING' "$logs/check-$name.txt" | sed 's/^/    /'
}

echo "--- 3. beeircd update --rc --yes (to 1.4.1-test)"
pid=$(field "$(status)" pid)
X beeircd update --rc --yes > "$logs/update.txt" 2>&1
r=$?
cat "$logs/update.txt"
row "update: exit code" "$(pf $r)" "exit $r"
after update "$pid" 1.4.1-test "$logs/update.txt"

echo "--- 4. beeircd update --rollback --yes (back to 1.4.0-test)"
pid=$(field "$(status)" pid)
X beeircd update --rollback --yes > "$logs/rollback.txt" 2>&1
r=$?
cat "$logs/rollback.txt"
row "rollback: exit code" "$(pf $r)" "exit $r"
after rollback "$pid" 1.4.0-test "$logs/rollback.txt"

echo "--- 5. /as restart by the oper bot"
pid=$(field "$(status)" pid)
X /root/hovcheck ask restart > "$logs/restart.txt" 2>&1
r=$?
cat "$logs/restart.txt"
row "restart: AdminServ code + Going now" "$(pf $r)" "$(grep -c '^OK' "$logs/restart.txt") OK"
for i in $(seq 90); do
	p=$(field "$(status)" pid)
	[ -n "$p" ] && [ "$p" != "$pid" ] && break
	sleep 1
done
sleep 2
after restart "$pid" 1.4.0-test ""

echo "--- bots leave"
X /root/hovcheck ask quit > "$logs/quit.txt" 2>&1
r=$?
cat "$logs/quit.txt"
row "whole run: no ERROR/QUIT/KILL seen by bots" "$(pf $r)" "$(grep -o 'saw [0-9]*' "$logs/quit.txt")"

echo "--- 6. livecheck"
X /root/hovcheck cert -out /root/zt
X sh -c 'cd /root && /root/livecheck -addr 127.0.0.1:6667 -tls 127.0.0.1:6697 -cert /root/zt-cert.pem -key /root/zt-key.pem -logchan=' > "$logs/livecheck.txt" 2>&1
r=$?
grep '^FAIL' "$logs/livecheck.txt" | sed 's/^/    /' || true
row "livecheck (core,opers,modes,bots)" "$(pf $r)" "$(tail -1 "$logs/livecheck.txt")"

X sh -c 'journalctl -u beeircd --no-pager | grep -i "hand\|gate\|door"' > "$logs/journal-handover.txt" 2>&1 || true
echo "--- journal lines about the hand-over"
sed 's/^/    /' "$logs/journal-handover.txt" | tail -40
[ $fail = 0 ] && echo "HANDOVER TEST PASSED" || { echo "HANDOVER TEST FAILED"; exit 1; }
