#!/bin/sh
# BeeIRCd 3-server test network (Stage 2f), for the homelab.
#
#   hub.beenet.test    services on   clients 127.0.0.1:27011
#   leaf1.beenet.test  services off  clients 127.0.0.1:27012
#   leaf2.beenet.test  services off  clients 127.0.0.1:27013
#
# Links run on their own internal Docker network (10.77.77.0/24); clients
# come in on a second one. Each server is a container with its own network
# namespace, so leaf2's link is cut with iptables inside its namespace
# (nsenter), the way a real network fault looks, while its clients stay.
#
#   sh testnet.sh up      build the image, make certs + configs, start
#   sh testnet.sh check   run livecheck -suites net against the three
#   sh testnet.sh cut     cut leaf2's link (by hand)   / heal: give it back
#   sh testnet.sh down    stop and remove everything (keeps nothing)
#
# Needs in this folder: beeircd and livecheck (linux/amd64 builds) and
# ../homelab-test/Dockerfile. Ports are bound to 127.0.0.1 only.
set -eu
cd "$(dirname "$0")"
DIR=$(pwd)
NET=beenet-link CLI=beenet-cli IMG=beeircd-testnet:dev
SUB=10.77.77 CLISUB=10.77.78.0/24 # free on the homelab (Docker's 172.x pool is full)
NODES="hub leaf1 leaf2"

ip_of() { case $1 in hub) echo $SUB.2 ;; leaf1) echo $SUB.3 ;; leaf2) echo $SUB.4 ;; esac; }
port_of() { case $1 in hub) echo 27011 ;; leaf1) echo 27012 ;; leaf2) echo 27013 ;; esac; }
sid_of() { case $1 in hub) echo 0HB ;; leaf1) echo 1LF ;; leaf2) echo 2LF ;; esac; }

# fp prints the link fingerprint of a cert: base64 SHA-256 of its public
# key (what beeircd link-info and ./unrealircd spkifp show).
fp() { openssl x509 -in "$1" -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64; }

conf() { # node role services
	n=$1
	cat <<EOF
[server]
name = "$n.beenet.test"
network = "BeeNet"
description = "BeeIRCd test network ($n)"
sid = "$(sid_of "$n")"

[[listen]]
address = "0.0.0.0:6667"

[cloak]
secret = "$CLOAK"

[services]
enabled = $3
database = "/data/services.db"
admin = "ztnadmin"
reg_delay = "0s"
force_email = false

[control]
socket = "/data/beeircd.sock"

[link]
role = "$2"
port = 7000
cert = "/data/link-cert.pem"
key = "/data/link-key.pem"
EOF
}

block() { # name fingerprint [host]
	printf '\n[[link.server]]\nname = "%s.beenet.test"\nfingerprint = "%s"\n' "$1" "$2"
	if [ -n "${3:-}" ]; then
		printf 'host = "%s"\nport = 7000\nautoconnect = true\n' "$3"
	fi
}

pid_of() { docker inspect -f '{{.State.Pid}}' "bee-$1"; }

cut() {
	p=$(pid_of leaf2)
	# Kill the open link first (the hub gets a reset), then refuse new ones.
	nsenter -t "$p" -n ss -K dst "$SUB.2" >/dev/null 2>&1 || true
	nsenter -t "$p" -n iptables -I OUTPUT -d "$SUB.2" -p tcp -j REJECT --reject-with tcp-reset
	nsenter -t "$p" -n iptables -I INPUT -s "$SUB.2" -p tcp -j DROP
	echo "leaf2 link cut"
}

heal() {
	p=$(pid_of leaf2)
	nsenter -t "$p" -n iptables -D OUTPUT -d "$SUB.2" -p tcp -j REJECT --reject-with tcp-reset 2>/dev/null || true
	nsenter -t "$p" -n iptables -D INPUT -s "$SUB.2" -p tcp -j DROP 2>/dev/null || true
	echo "leaf2 link back"
}

up() {
	[ -x ./beeircd ] || { echo "put a linux beeircd build here first" >&2; exit 1; }
	cp ./beeircd ../homelab-test/beeircd
	docker build -q -t $IMG ../homelab-test >/dev/null
	rm -f ../homelab-test/beeircd
	docker network inspect $NET >/dev/null 2>&1 || docker network create --internal --subnet $SUB.0/24 $NET >/dev/null
	docker network inspect $CLI >/dev/null 2>&1 || docker network create --subnet $CLISUB $CLI >/dev/null
	CLOAK=$(openssl rand -hex 20)
	for n in $NODES; do
		d=$DIR/data/$n
		rm -rf "$d"
		mkdir -p "$d"
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
			-subj "/CN=$n.beenet.test" -keyout "$d/link-key.pem" -out "$d/link-cert.pem" 2>/dev/null
	done
	FH=$(fp data/hub/link-cert.pem) F1=$(fp data/leaf1/link-cert.pem) F2=$(fp data/leaf2/link-cert.pem)
	{ conf hub hub true; block leaf1 "$F1"; block leaf2 "$F2"; } >data/hub/beeircd.conf
	{ conf leaf1 leaf false; block hub "$FH" $SUB.2; } >data/leaf1/beeircd.conf
	{ conf leaf2 leaf false; block hub "$FH" $SUB.2; } >data/leaf2/beeircd.conf
	for n in $NODES; do
		chmod 600 data/$n/link-key.pem
		chown -R 65534:65534 data/$n
		docker rm -f bee-$n >/dev/null 2>&1 || true
		docker create --name bee-$n --network $CLI -p 127.0.0.1:$(port_of $n):6667 \
			-v "$DIR/data/$n:/data" --restart no $IMG >/dev/null
		docker network connect --ip "$(ip_of $n)" $NET bee-$n
		docker start bee-$n >/dev/null
	done
	for i in $(seq 1 30); do
		if docker logs bee-hub 2>&1 | grep -q "leaf1.beenet.test: sync done" &&
			docker logs bee-hub 2>&1 | grep -q "leaf2.beenet.test: sync done"; then
			echo "test network up: hub + leaf1 + leaf2 linked"
			return 0
		fi
		sleep 1
	done
	echo "the leaves did not link within 30 s; docker logs bee-hub says:" >&2
	docker logs bee-hub 2>&1 | tail -20 >&2
	exit 1
}

check() {
	./livecheck -suites net \
		-net "hub.beenet.test=127.0.0.1:27011,leaf1.beenet.test=127.0.0.1:27012,leaf2.beenet.test=127.0.0.1:27013" \
		-cut "sh $DIR/testnet.sh cut" -heal "sh $DIR/testnet.sh heal"
}

down() {
	for n in $NODES; do docker rm -f bee-$n >/dev/null 2>&1 || true; done
	docker network rm $NET $CLI >/dev/null 2>&1 || true
	rm -rf "$DIR/data"
	echo "test network removed"
}

case ${1:-} in
up) up ;;
check) check ;;
cut) cut ;;
heal) heal ;;
down) down ;;
*) echo "usage: sh testnet.sh up|check|cut|heal|down" >&2; exit 2 ;;
esac
