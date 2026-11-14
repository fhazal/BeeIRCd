#!/bin/sh
# Clean-machine test for `beeircd install`. Runs on a host with
# LXD: makes a fresh system container per OS, installs BeeIRCd inside it
# with scripted answers, tests it, reboots it, tests again, removes it.
# The containers have no network; the host is not changed.
#
#   scripts/cleanmachine.sh <beeircd linux binary> <installcheck binary> [image ...]
set -eu
bin=$1 check=$2
shift 2
[ $# -gt 0 ] || set -- images:debian/13 images:ubuntu/24.04
pw="clean-machine-$(date +%s)"
fail=0
for image in "$@"; do
	name="bee-$(echo "$image" | tr -c 'a-z0-9\n' '-' | sed 's/^images-//; s/-$//')"
	echo "===== $image ($name)"
	lxc delete -f "$name" >/dev/null 2>&1 || true
	lxc launch -q "$image" "$name"
	for i in $(seq 30); do lxc exec "$name" -- systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
	lxc file push -q --mode 0755 "$bin" "$name/root/beeircd"
	lxc file push -q --mode 0755 "$check" "$name/root/installcheck"
	# A firewall to open ports in, where the image has one.
	lxc exec "$name" -- sh -c 'command -v ufw >/dev/null && ufw --force enable >/dev/null && echo "ufw enabled"' || true
	# Answers: name, network, admin, password x2, oper-from, ports,
	# certificate (self-signed: no network here), prefixes, role hub +
	# link port 7000, Hive Panel yes, confirm, start.
	answers="irc.clean.test
CleanNet
Bee
$pw
$pw
*@127.0.0.1, *@localhost
6667 6697
2
1
2

y
y
y
"
	if ! printf '%s' "$answers" | lxc exec "$name" -- /root/beeircd install; then
		echo "FAIL install on $image"; fail=1; continue
	fi
	lxc exec "$name" -- sh -c 'command -v ufw >/dev/null && ufw status | grep -E "6667|6697|7000"' || true
	# run_checks [-again]: -again = the admin nick is registered already.
	run_checks() {
		lxc exec "$name" -- beeircd status || fail=1
		lxc exec "$name" -- beeircd check || fail=1
		lxc exec "$name" -- /root/installcheck -plain 127.0.0.1:6667 -tls 127.0.0.1:6697 -admin Bee -pass "$pw" "$@" || fail=1
	}
	run_checks
	echo "--- files and service"
	lxc exec "$name" -- sh -c 'ls -ld /etc/beeircd /var/lib/beeircd /run/beeircd; ls -l /etc/beeircd /var/lib/beeircd; grep -F -A6 "[link]" /etc/beeircd/beeircd.conf | grep -v "^#"; id beeircd; systemctl is-enabled beeircd beeircd-backup.timer; systemd-analyze security beeircd 2>/dev/null | tail -1'
	echo "--- Hive Panel (service beeircd-web on 127.0.0.1:8787)"
	lxc exec "$name" -- sh -c 'for i in $(seq 20); do systemctl is-active -q beeircd-web && break; sleep 1; done; systemctl is-enabled beeircd-web && systemctl is-active beeircd-web && grep -F -A4 "[web]" /etc/beeircd/beeircd.conf | grep "^listen" && systemd-analyze security beeircd-web 2>/dev/null | tail -1' || { echo "FAIL hive panel service"; fail=1; }
	# No curl in the images: plain HTTP over bash's /dev/tcp.
	lxc exec "$name" -- bash -c 'get() { exec 3<>/dev/tcp/127.0.0.1/8787; printf "GET %s HTTP/1.0\r\nHost: 127.0.0.1:8787\r\n\r\n" "$1" >&3; cat <&3; exec 3<&-; }
		get / | grep -q "<title>Hive Panel</title>" && get /api/status | head -1 | grep -q " 401 " && echo "panel answers, its API needs a sign-in"' || { echo "FAIL hive panel http"; fail=1; }
	echo "--- backup timer runs"
	lxc exec "$name" -- sh -c 'systemctl start beeircd-backup.service && ls /var/lib/beeircd/backups' || fail=1
	echo "--- Hive Panel backup download (beeircd-web reads the backup folder)"
	lxc exec "$name" -- /root/installcheck -panel 127.0.0.1:8787 -admin Bee -pass "$pw" || { echo "FAIL hive panel backup download"; fail=1; }
	echo "--- backup to a path of your own (the locked-down server can't write /root)"
	lxc exec "$name" -- sh -c 'beeircd backup /root/mine.tar.gz && tar -tzf /root/mine.tar.gz && ! ls -A /var/lib/beeircd/backups | grep -q named' || { echo "FAIL backup to /root"; fail=1; }
	echo "--- stop, then start (check runs while stopped)"
	lxc exec "$name" -- sh -c 'beeircd stop && beeircd start && ls -l /var/lib/beeircd' || fail=1
	lxc exec "$name" -- /root/installcheck -tls 127.0.0.1:6697 -admin Bee -pass "$pw" -again || { echo "FAIL after stop/start"; fail=1; }
	echo "--- reboot"
	# Without a network (as here) systemd waits 2 minutes for one first.
	lxc restart "$name"
	for i in $(seq 180); do lxc exec "$name" -- systemctl is-active beeircd 2>/dev/null | grep -q '^active' && break; sleep 1; done
	run_checks -again
	echo "--- uninstall keeps data, --purge removes it"
	printf 'y\n' | lxc exec "$name" -- beeircd uninstall || fail=1
	lxc exec "$name" -- test -f /etc/beeircd/beeircd.conf || { echo "FAIL config gone without --purge"; fail=1; }
	printf 'y\n' | lxc exec "$name" -- /root/beeircd uninstall --purge || fail=1
	lxc exec "$name" -- sh -c '! test -e /etc/beeircd && ! test -e /var/lib/beeircd && ! id beeircd 2>/dev/null' || { echo "FAIL purge left things"; fail=1; }
	lxc delete -f "$name"
done
[ $fail = 0 ] && echo "CLEAN-MACHINE TEST PASSED" || { echo "CLEAN-MACHINE TEST FAILED"; exit 1; }
