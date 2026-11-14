#!/bin/bash
# 24 h soak test for a release: a throwaway BeeIRCd on a test machine with a
# steady crowd plus constant connect/quit churn, sampled every 5 minutes.
# Memory should stay flat after the first hour.
#
#   scripts/soak.sh /opt/beeircd-soak [hours]
#
# The directory needs ./beeircd and ./beeload (Linux builds). Results:
# samples.csv (time, rss MB, goroutines-ish threads, users, fds) and
# summary.txt. The test server is not touched.
set -u
dir=${1:?usage: soak.sh DIR [hours]}
hours=${2:-24}
port=46667
cd "$dir" || exit 1
ulimit -n 30000
mkdir -p data
secret=$(head -c 30 /dev/urandom | base64 | tr -d '/+=' | head -c 40)
cat >data/beeircd.conf <<EOF
[server]
name = "soak.bee.local"
network = "BeeSoak"
description = "24 h soak"
sid = "0SK"

[[listen]]
address = "127.0.0.1:$port"

[cloak]
secret = "$secret"

[services]
database = "services.db"
admin = "Nobody"

[limits]
max_users = 12000
session_limit = 12000
max_channels = 50
flood_burst = 1000
flood_rate = 1000

[history]
default = "50:1d"
secret = "$secret"

[backup]
keep = 3

[update]
check = false
EOF
rm -f data/services.db* data/history.db
./beeircd check -config data/beeircd.conf || exit 1
systemd-run --quiet --unit=beeircd-soak -p MemoryMax=1G -p AllowedCPUs=3 \
	"$dir/beeircd" run -config "$dir/data/beeircd.conf"
for i in $(seq 1 30); do ss -ltn | grep -q ":$port " && break; sleep 0.5; done
pid=$(systemctl show -p MainPID --value beeircd-soak)
end=$(( $(date +%s) + hours*3600 ))

# Steady crowd: 2,000 bots chatting, nick changes, part/join (beeload default mix).
./beeload -addr 127.0.0.1:$port -n 2000 -ramp 200 -chans 50 -watchers 100 \
	-talk 20s -duration $((hours*3600))s -prefix s >steady.log 2>&1 &
# Churn: 300 bots connect, chat for a minute, quit; again and again.
( while [ "$(date +%s)" -lt "$end" ]; do
	./beeload -addr 127.0.0.1:$port -n 300 -ramp 100 -chans 20 -watchers 10 \
		-talk 5s -duration 60s -prefix c >>churn.log 2>&1
	sleep 5
done ) &

echo "time,rss_mb,threads,fds,users" >samples.csv
n=0
while [ "$(date +%s)" -lt "$end" ] && kill -0 "$pid" 2>/dev/null; do
	rss=$(( $(awk '/VmRSS/{print $2}' /proc/$pid/status) / 1024 ))
	thr=$(awk '/Threads/{print $2}' /proc/$pid/status)
	fds=$(ls /proc/$pid/fd | wc -l)
	users=$(./beeircd status -config data/beeircd.conf 2>/dev/null | awk '/People/{print $2; exit}')
	echo "$(date +%FT%T),$rss,$thr,$fds,${users:-?}" >>samples.csv
	n=$((n+1))
	[ $((n % 12)) -eq 0 ] && ./beeircd backup -config data/beeircd.conf >>backup.log 2>&1
	[ $((n % 36)) -eq 0 ] && ./beeircd rehash -config data/beeircd.conf >>rehash.log 2>&1
	sleep 300
done

alive=yes; kill -0 "$pid" 2>/dev/null || alive=no
systemctl stop beeircd-soak
wait
{
	echo "alive at end: $alive"
	echo "samples: $(($(wc -l <samples.csv)-1))"
	echo "first hour end: $(sed -n 13p samples.csv)"
	echo "last: $(tail -1 samples.csv)"
	echo "max rss: $(tail -n +2 samples.csv | cut -d, -f2 | sort -n | tail -1) MB"
	echo "steady lag: $(grep '  online' steady.log | tail -1)"
	echo "backups: $(grep -c . backup.log) lines, rehash: $(grep -c . rehash.log) lines"
	journalctl -u beeircd-soak --no-pager | grep -i -E 'panic|fatal|error' | head -20
} >summary.txt
cat summary.txt
