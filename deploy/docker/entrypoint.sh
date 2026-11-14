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

case "$1" in
run)
	wait_for_config
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
