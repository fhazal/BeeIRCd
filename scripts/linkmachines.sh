#!/bin/sh
# Two-machine link test for `beeircd install`. Runs on a host
# with LXD: two fresh system containers on a private bridge (no NAT, no
# internet). The hub is installed first; the leaf is installed with only
# what the hub's installer printed (fingerprint) and its cloak secret; the
# block the leaf's installer prints is pasted on the hub, then rehash.
# Checks: the link comes up on both, the admin can /oper on the leaf
# (accounts live on the hub), link-info. Removes everything at the end.
#
#   scripts/linkmachines.sh <beeircd linux binary> <installcheck binary> [image]
set -eu
bin=$1 check=$2 image=${3:-images:debian/13}
pw="link-machines-$(date +%s)"
net=beelink fail=0
hub=bee-link-hub leaf=bee-link-leaf
cleanup() {
	lxc delete -f $hub $leaf >/dev/null 2>&1 || true
	lxc network delete $net >/dev/null 2>&1 || true
}
cleanup
lxc network create $net ipv4.address=10.77.79.1/24 ipv4.nat=false ipv6.address=none >/dev/null
for n in $hub $leaf; do
	lxc init -q "$image" $n
	ip=10.77.79.10
	[ $n = $leaf ] && ip=10.77.79.11
	lxc config device add -q $n eth0 nic network=$net ipv4.address=$ip >/dev/null
	lxc start $n
done
for n in $hub $leaf; do
	for i in $(seq 60); do lxc exec $n -- systemctl is-system-running 2>/dev/null | grep -qE 'running|degraded' && break; sleep 1; done
	# Static address inside too: the host firewall may not let LXD's DHCP answer.
	ip=10.77.79.10
	[ $n = $leaf ] && ip=10.77.79.11
	lxc exec $n -- sh -c "printf '[Match]\nName=eth0\n[Network]\nAddress=$ip/24\n' >/etc/systemd/network/eth0.network && networkctl reload"
	lxc file push -q --mode 0755 "$bin" "$n/root/beeircd"
	lxc file push -q --mode 0755 "$check" "$n/root/installcheck"
done

echo "===== install the hub"
# name, network, admin, password x2, oper-from, ports, self-signed,
# prefixes, hub + link port 7000, confirm, start.
out=$(printf '%s\n' irc-hub.link.test LinkNet Bee "$pw" "$pw" "*@127.0.0.1, *@localhost" "6667 6697" 2 "" 2 "" y y |
	lxc exec $hub -- /root/beeircd install) || { echo "$out"; echo "FAIL hub install"; cleanup; exit 1; }
echo "$out" | sed -n '/Linking/,$p'
hubfp=$(echo "$out" | sed -n 's/^  Fingerprint   //p' | head -1)
cloak=$(lxc exec $hub -- awk '/^\[cloak\]/{c=1} c && /^secret/{print $3; exit}' /etc/beeircd/beeircd.conf | tr -d '"')
[ -n "$hubfp" ] && [ -n "$cloak" ] || { echo "FAIL no fingerprint or cloak secret from the hub"; cleanup; exit 1; }

echo "===== install the leaf (hub's name, address, fingerprint, cloak secret)"
out=$(printf '%s\n' irc-leaf.link.test LinkNet Bee "$pw" "$pw" "*@127.0.0.1, *@localhost" "6667 6697" 2 "" 3 "" \
	irc-hub.link.test 10.77.79.10:7000 "$hubfp" "$cloak" y y |
	lxc exec $leaf -- /root/beeircd install) || { echo "$out"; echo "FAIL leaf install"; cleanup; exit 1; }
echo "$out" | sed -n '/Linking/,$p'
if echo "$out" | grep -q "$cloak"; then echo "FAIL the cloak secret was shown"; fail=1; fi

echo "===== paste the leaf's block on the hub, rehash"
block=$(echo "$out" | awk '/On the hub \(BeeIRCd\)/{b=1; next} /On an UnrealIRCd/{b=0} b && /^    /{sub(/^    /, ""); print}')
echo "$block"
printf '\n%s\n' "$block" | lxc exec $hub -- sh -c 'cat >> /etc/beeircd/beeircd.conf && beeircd rehash'

echo "===== link up on both"
up=0
for i in $(seq 60); do
	if lxc exec $hub -- beeircd status 2>/dev/null | grep -q "irc-leaf.link.test.*up" &&
		lxc exec $leaf -- beeircd status 2>/dev/null | grep -q "irc-hub.link.test.*up"; then
		up=1
		break
	fi
	sleep 1
done
lxc exec $hub -- beeircd status | grep -i -A2 "links" || true
lxc exec $leaf -- beeircd status | grep -i -A2 "links" || true
[ $up = 1 ] || { echo "FAIL the link did not come up in 60 s"; lxc exec $leaf -- journalctl -u beeircd --no-pager | tail -20; fail=1; }

echo "===== the admin on the leaf: NickServ on the hub, /oper on the leaf"
lxc exec $leaf -- /root/installcheck -plain 127.0.0.1:6667 -tls 127.0.0.1:6697 -admin Bee -pass "$pw" || fail=1

echo "===== link-info on the leaf"
lxc exec $leaf -- beeircd link-info | grep -E "This server|Links to" || fail=1

cleanup
[ $fail = 0 ] && echo "LINK-MACHINES TEST PASSED" || { echo "LINK-MACHINES TEST FAILED"; exit 1; }
