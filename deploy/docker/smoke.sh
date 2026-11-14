#!/usr/bin/env bash
# smoke.sh IMAGE PLATFORM: the BeeIRCd image on one platform (linux/amd64,
# linux/arm64; arm64 runs emulated in CI): the nine questions, start,
# `beeircd status`, an IRC client gets 001, and `beeircd update` points
# to Docker. Cleans up after itself.
set -euo pipefail
img=$1
plat=$2
n="beesmoke-$$"
port=$((20000 + $$ % 20000))
cleanup() {
	docker rm -f "$n" >/dev/null 2>&1 || true
	docker volume rm "$n" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker volume create "$n" >/dev/null
printf 'irc.smoke.test\n\nBoss\nSmokeOper-2026\nSmokeOper-2026\n\n\n2\n1\n1\nn\ny\n' |
	docker run --rm -i --platform "$plat" -v "$n:/data" "$img" install | grep -E '✓|✗'
docker run -d --name "$n" --init --platform "$plat" -v "$n:/data" -p "127.0.0.1:$port:6667" "$img" >/dev/null

for _ in $(seq 1 90); do
	docker exec "$n" beeircd status >/dev/null 2>&1 && break
	sleep 1
done
docker exec "$n" beeircd status
echo "machine: $(docker exec "$n" uname -m)"

exec 3<>"/dev/tcp/127.0.0.1/$port"
printf 'NICK smoke\r\nUSER s 0 * :s\r\n' >&3
got=""
for _ in $(seq 1 30); do
	if IFS= read -r -t 2 line <&3; then
		got+="$line"$'\n'
		[[ $line == *" 001 smoke "* ]] && break
	fi
done
printf 'QUIT\r\n' >&3
exec 3<&-
grep -q ' 001 smoke ' <<<"$got" || { echo "FAIL: no welcome from IRC"; echo "$got"; exit 1; }
echo "IRC: welcome received"

upd=$(docker exec "$n" beeircd update 2>&1 || true) # exits 1 in Docker on purpose
if grep -q 'docker compose pull' <<<"$upd"; then
	echo "update: points to docker compose pull"
else
	echo "FAIL: beeircd update doesn't point to Docker"
	exit 1
fi
echo "SMOKE PASS $plat"
