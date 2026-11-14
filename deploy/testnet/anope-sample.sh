#!/bin/sh
# Anope 2.0 sample database: the same SANDBOX as anope-smoke.sh (program and
# modules only, fresh config from Anope's own example files, its own empty
# database) linked to a BeeIRCd hub with built-in services off, filled by
# anope-fill.py, then its anope.db is copied out for the import tests.
# The live Anope's config and database are never read or used.
#
#   sh anope-sample.sh <anope install dir> <anope source dir> <out file> [enc] [full|nicks]
#
# enc: sha256 (default), bcrypt, md5, sha1 or none - the module that
# encrypts new passwords. Needs ./beeircd (linux build) and ./anope-fill.py.
set -eu
cd "$(dirname "$0")"
DIR=$(pwd)
INST=$1 SRC=$2 OUT=$3 ENC=${4:-sha256} FILL=${5:-full} KEEP=
S=/home/ircsand/anope-sample B=$DIR/anope-sample-bee
LINKPORT=18997 CLIPORT=18998 PASS=sandpass-sample
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
# Passwords: the chosen module encrypts (it must come first), sha256 checks too.
sed -i 's/^\tname = "enc_sha256"/\tname = "enc_'"$ENC"'"/' $C
[ "$ENC" = sha256 ] || printf '\nmodule { name = "enc_sha256" }\n' >>$C
# An oper to fill OperServ, HostServ and BotServ; no waiting between registrations.
printf '\noper\n{\n\tname = "zfill"\n\ttype = "Services Root"\n\trequire_oper = no\n}\n' >>$C
sed -i 's/regdelay = [0-9a-z]*/regdelay = 0/; s/senddelay = 3s/senddelay = 0s/' $S/conf/nickserv.example.conf $S/conf/memoserv.example.conf
# OperServ answers services opers that aren't IRC opers here.
sed -i 's/opersonly = yes/opersonly = no/' $S/conf/operserv.example.conf
# Modules the example files leave off.
for m in cs_flags; do grep -q "^[[:space:]]*name = \"$m\"" $S/conf/*.conf || printf '\nmodule { name = "%s" }\n' $m >>$C; done
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

[limits]
connect_rate = "off"
session_limit = 50
max_unregistered_per_ip = 20

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
for i in $(seq 1 30); do grep -q "services.sandbee.local: sync done" "$B/bee.log" && break; sleep 1; done
if ! grep -q "services.sandbee.local: sync done" "$B/bee.log"; then
	echo "FAIL Anope did not link; BeeIRCd log:"
	grep -v " -> \| <- " "$B/bee.log" | tail -15
	echo "Anope:"
	tail -15 "$S"/logs/services.log.* 2>/dev/null || cat "$S/logs/start.out"
	stop
	rm -rf "$S" "$B"
	exit 1
fi
echo "Anope linked to BeeIRCd"
python3 -I ./anope-fill.py 127.0.0.1 $CLIPORT $FILL
for i in $(seq 1 10); do [ -s "$S/data/anope.db" ] && break; sleep 1; done
cp "$S/data/anope.db" "$OUT" && echo "sample written: $OUT ($(wc -l <"$OUT") lines)"
grep -a "refused\|not handled" "$B/bee.log" | sort | uniq -c | head -20
stop
rm -rf "$S" "$B"
