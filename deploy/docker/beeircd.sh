#!/bin/sh
# `beeircd` inside the image: every command runs as the beeircd user, also
# `docker compose exec beeircd beeircd ...` (which starts as root), so no
# file in /data ever ends up owned by root.
if [ "$(id -u)" = 0 ]; then
	exec su-exec beeircd:beeircd /opt/beeircd/beeircd "$@"
fi
exec /opt/beeircd/beeircd "$@"
