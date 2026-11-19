#!/bin/sh
# Anope 2.0 smoke test: a SANDBOX copy of Anope (program and
# modules only, fresh config from Anope's own example files, its own empty
# database) linked to a BeeIRCd hub with built-in services off, all on
# 127.0.0.1. The live Anope's config and database are never read or used.
#
#   sh anope-smoke.sh <anope install dir> <anope source dir> [keep]
#
# Runs Anope as the ircsand user; removes the sandbox at the end unless
# "keep" is given. Needs ./beeircd (linux build) and ./anope-probe.py here.
set -eu
cd "$(dirname "$0")"
DIR=$(pwd)
INST=$1 SRC=$2 KEEP=${3:-}
S=/home/ircsand/anope-sand B=$DIR/anope-bee
LINKPORT=18995 CLIPORT=18996 PASS=sandpass-2g
stop() {
	pkill -u ircsand -f "$S/bin/services" 2>/dev/null || true
	[ -f "$B/pid" ] && kill "$(cat "$B/pid")" 2>/dev/null || true
	sleep 1
}
stop
rm -rf "$S" "$B"
mkdir -p "$S/conf" "$S/data" "$S/logs" "$B"
cp -a "$INST/bin" "$INST/lib" "$INST/locale" "$S/"
cp "$SRC"/data/*.conf "$S/conf/"
cp "$S/conf/example.conf" "$S/conf/services.conf"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 7 -subj "/CN=services.sandbee.local" \
	-keyout "$S/data/anope.key" -out "$S/data/anope.crt" 2>/dev/null
FP=$(openssl x509 -in "$S/data/anope.crt" -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64)

C=$S/conf/services.conf
sed -i 's/value = "services.example.com"/value = "services.sandbee.local"/' $C
sed -i '/^uplink/,/^}/{s/ssl = no/ssl = yes/; s/port = 7000/port = '$LINKPORT'/; s/password = "mypassword"/password = "'$PASS'"/}' $C
sed -i '/^serverinfo/,/^}/{s/^\tname = "services.host"/\tname = "services.host"\n\tid = "00A"/}' $C
sed -i 's/name = "inspircd3"/name = "unreal4"/' $C
sed -i '/^networkinfo/,/^}/{s/networkname = "[^"]*"/networkname = "BeeSand"/}' $C
sed -i 's/usemail = yes/usemail = no/' $C
sed -i 's/forceemail = yes/forceemail = no/' $S/conf/nickserv.example.conf
# m_ssl_openssl with our cert, so BeeIRCd can pin Anope's fingerprint.
printf '
module
{
	name = "m_ssl_openssl"
	cert = "%s/data/anope.crt"
	key = "%s/data/anope.key"
}
' "$S" "$S" >>$C
# m_sasl is on in Anope's modules.example.conf already.
chown -R ircsand:ircsand "$S"

# BeeIRCd hub, built-in services off: Anope is the services server.
cat >"$B/beeircd.conf" <<EOF
[server]
name = "hub.sandbee.local"
network = "BeeSand"
description = "BeeIRCd Anope smoke test"
sid = "0SB"

[[listen]]
address = "127.0.0.1:$CLIPORT"

[cloak]
secret = "$(openssl rand -hex 20)"

[services]
enabled = false
database = "$B/services.db"
admin = "ztadmin"

[control]
socket = "$B/beeircd.sock"

[link]
role = "hub"
address = "127.0.0.1"
port = $LINKPORT

[[link.server]]
name = "services.sandbee.local"
fingerprint = "$FP"
password = "$PASS"
EOF
chmod 600 "$B/beeircd.conf"
BEEIRCD_LINK_DEBUG=1 setsid nohup ./beeircd run -config "$B/beeircd.conf" >"$B/bee.log" 2>&1 </dev/null &
echo $! >"$B/pid"
sleep 2
su ircsand -s /bin/sh -c "cd $S && ./bin/services --confdir=$S/conf --dbdir=$S/data --logdir=$S/logs --modulesdir=$S/lib --localedir=$S/locale" >"$S/logs/start.out" 2>&1 || true
for i in $(seq 1 30); do grep -q "Received end of netburst from services.sandbee.local (" "$B/bee.log" && break; sleep 1; done
if ! grep -q "Received end of netburst from services.sandbee.local (" "$B/bee.log"; then
	echo "FAIL Anope did not link; BeeIRCd log:"
	grep -v " -> \| <- " "$B/bee.log" | tail -15
	echo "Anope:"
	tail -15 "$S"/logs/services.log.* 2>/dev/null || cat "$S/logs/start.out"
	stop # "keep" keeps the files to look at, never running processes
	[ -n "$KEEP" ] || rm -rf "$S" "$B"
	exit 1
fi
echo "Anope linked to BeeIRCd"
python3 -I ./anope-probe.py 127.0.0.1 $CLIPORT || true
grep -a "refused\|not handled" "$B/bee.log" | sort | uniq -c | head -20
stop
[ -n "$KEEP" ] || rm -rf "$S" "$B"
