#!/bin/sh
# Start of the BeeIRCd container. As root it only makes /data belong to
# the beeircd user (a fresh bind mount is root's) and starts cron for the
# daily backup; the program itself always runs as beeircd.
set -e
conf=/data/beeircd.conf

if [ "$(id -u)" = 0 ]; then
	mkdir -p /data /run/beeircd
	find /data /run/beeircd ! -user beeircd -exec chown beeircd:beeircd {} + 2>/dev/null || true
fi

# wait_for_config: `docker compose up -d` before the setup is fine; the
# server starts as soon as `docker compose run --rm beeircd install` is done.
wait_for_config() {
	if [ ! -f "$conf" ]; then
		echo "BeeIRCd isn't set up yet. Run:  docker compose run --rm beeircd install"
		while [ ! -f "$conf" ]; do sleep 5; done
		echo "Config found, starting."
	fi
}

# share_beeroom: with BEEROOM_WEBIRC (BeeRoom's fixed address in
# compose.yaml) the gateway is added to beeircd.conf once, and only its
# password and the network name go to BeeRoom, in the beeroom-link volume.
share_beeroom() {
	[ -n "${BEEROOM_WEBIRC:-}" ] && [ -d /link ] || return 0
	if [ -z "$(webirc_password)" ]; then
		beeircd webirc add -q beeroom "$BEEROOM_WEBIRC" >/dev/null
		echo "WEBIRC gateway beeroom added for $BEEROOM_WEBIRC (the BeeRoom container)"
	else
		webirc_move
	fi
	umask 022
	webirc_password >/link/webirc.new && mv /link/webirc.new /link/webirc
	awk '/^\[/ { s = ($0 == "[server]") } s && /^network *=/ { sub(/^network *= *"/, ""); sub(/".*$/, ""); print; exit }' "$conf" >/link/network.new
	mv /link/network.new /link/network
}

# webirc_move points the beeroom [[webirc]] block at BEEROOM_WEBIRC when
# the address in compose.yaml changed after the first start.
webirc_move() {
	want="hosts = [\"$BEEROOM_WEBIRC\"]"
	awk -v want="$want" '
	function flush() { for (i = 1; i <= k; i++) { l = buf[i]; if (n == "beeroom" && l ~ /^hosts *=/) l = want; print l } k = 0 }
	/^\[/ { flush(); w = ($0 == "[[webirc]]"); n = "" }
	w { buf[++k] = $0; if ($0 ~ /^name *=/) { n = $0; sub(/^name *= *"/, "", n); sub(/".*$/, "", n) }; next }
	{ print }
	END { flush() }' "$conf" >"$conf.new" || { rm -f "$conf.new"; return 0; }
	if cmp -s "$conf" "$conf.new"; then
		rm -f "$conf.new"
		return 0
	fi
	cat "$conf.new" >"$conf"
	rm -f "$conf.new"
	echo "WEBIRC gateway beeroom moved to $BEEROOM_WEBIRC"
}

# webirc_password prints the password of the [[webirc]] block named beeroom.
webirc_password() {
	awk '
	/^\[/ { if (w && n == "beeroom") { print p; done = 1; exit } w = ($0 == "[[webirc]]"); n = ""; p = ""; next }
	w && /^name *=/ { n = $0; sub(/^name *= *"/, "", n); sub(/".*$/, "", n) }
	w && /^password *=/ { p = $0; sub(/^password *= *"/, "", p); sub(/".*$/, "", p) }
	END { if (!done && w && n == "beeroom") print p }' "$conf"
}

case "$1" in
run)
	wait_for_config
	share_beeroom
	if [ "$(id -u)" = 0 ]; then
		crond -b -l 8
	fi
	exec beeircd run -config "$conf"
	;;
web)
	wait_for_config
	exec beeircd web -config "$conf"
	;;
*)
	exec beeircd "$@"
	;;
esac
