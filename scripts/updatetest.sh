#!/bin/sh
# Real-machine test of `beeircd update`, on a host with LXD. Needs a kit
# made by scripts/updatetest-kit.sh: v1 (1.0.0-test, installed first), r2
# (1.0.1-test, good), r3 (1.0.2-test, never answers: must roll back), rbad
# (a release.json changed after signing: must be refused), installcheck.
# All signed with a throwaway test key. No network, nothing on the host.
#
#   scripts/updatetest.sh <kit dir> [image ...]
set -eu
kit=$1
shift
[ $# -gt 0 ] || set -- deb13 ubu24
fail=0
want() { # want <what> <command...>: the command's output must contain what
	what=$1; shift
	out=$("$@" 2>&1) || true
	if echo "$out" | grep -qF "$what"; then
		echo "OK   $what"
	else
		echo "FAIL wanted \"$what\" from: $*"; echo "$out" | sed 's/^/     | /'; fail=1
	fi
}
for image in "$@"; do
	n="bee-upd-$image"
	echo "===== $image"
	lxc delete -f "$n" >/dev/null 2>&1 || true
	lxc launch -q "$image" "$n"
	for i in $(seq 30); do lxc exec "$n" -- systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
	lxc file push -q -r "$kit"/r2 "$kit"/r3 "$kit"/rbad "$n/root/"
	lxc file push -q --mode 0755 "$kit/v1" "$n/root/beeircd"
	lxc file push -q --mode 0755 "$kit/installcheck" "$n/root/installcheck"
	printf 'irc.update.test\nUpdNet\nBee\nupdate-pass-1\nupdate-pass-1\n\n6667 6697\n2\n1\n1\ny\ny\ny\n' | lxc exec "$n" -- /root/beeircd install >/dev/null
	lxc exec "$n" -- /root/installcheck -tls 127.0.0.1:6697 -admin Bee -pass update-pass-1 >/dev/null || { echo "FAIL setup"; fail=1; }
	want "BeeIRCd 1.0.0-test" lxc exec "$n" -- beeircd version
	lxc exec "$n" -- sh -c 'nohup /root/installcheck -serve /root -addr 127.0.0.1:8099 >/dev/null 2>&1 &'
	# setonly <folder>: point [update] url at one of the served releases.
	# Test versions need --rc, so the release folder goes in rc_url.
	setonly() { lxc exec "$n" -- sh -c "sed -i '/^rc_url = \"http:\/\/127.0.0.1:8099/d' /etc/beeircd/beeircd.conf && sed -i '/^\[update\]/a rc_url = \"http://127.0.0.1:8099/$1\"' /etc/beeircd/beeircd.conf"; }

	echo "--- a release changed after signing is refused"
	setonly rbad
	want "doesn't match BeeIRCd's key" lxc exec "$n" -- beeircd update --rc --yes
	want "BeeIRCd 1.0.0-test" lxc exec "$n" -- beeircd version

	echo "--- --check, then the update"
	setonly r2
	want "Install it with:  beeircd update --rc" lxc exec "$n" -- beeircd update --rc --check
	# Someone online during the update: what do they see?
	lxc exec "$n" -- sh -c 'nohup /root/installcheck -watch 127.0.0.1:6667 > /root/watch.txt 2>&1 &'
	sleep 2
	want "restarted · 1.0.1-test answering" lxc exec "$n" -- beeircd update --rc --yes
	want "Server updating to BeeIRCd 1.0.1-test, back in a few seconds" lxc exec "$n" -- cat /root/watch.txt
	want "BeeIRCd 1.0.1-test" lxc exec "$n" -- beeircd version
	want "1.0.1-test   ● running" lxc exec "$n" -- beeircd status
	lxc exec "$n" -- /root/installcheck -tls 127.0.0.1:6697 -admin Bee -pass update-pass-1 -again >/dev/null && echo "OK   IRC works on 1.0.1-test (NickServ IDENTIFY + /oper)" || { echo "FAIL IRC after update"; fail=1; }
	want "stopping: Server updating to BeeIRCd 1.0.1-test" lxc exec "$n" -- journalctl -u beeircd --no-pager
	lxc exec "$n" -- sh -c 'ls -l /opt/beeircd/; stat -c "%U:%G %a %n" /etc/beeircd/beeircd.conf'

	echo "--- a release that never answers rolls back by itself"
	setonly r3
	want "rolled back: 1.0.1-test is running again" lxc exec "$n" -- beeircd update --rc --yes
	want "1.0.1-test   ● running" lxc exec "$n" -- beeircd status

	echo "--- --rollback by hand (to 1.0.0-test: the failed 1.0.2 must not have used it up)"
	want "answering after" lxc exec "$n" -- beeircd update --rollback --yes
	want "BeeIRCd 1.0.0-test" lxc exec "$n" -- beeircd version
	want "1.0.0-test   ● running" lxc exec "$n" -- beeircd status
	lxc exec "$n" -- /root/installcheck -tls 127.0.0.1:6697 -admin Bee -pass update-pass-1 -again >/dev/null && echo "OK   IRC works after --rollback" || { echo "FAIL IRC after rollback"; fail=1; }
	lxc delete -f "$n"
done
[ $fail = 0 ] && echo "UPDATE TEST PASSED" || { echo "UPDATE TEST FAILED"; exit 1; }
