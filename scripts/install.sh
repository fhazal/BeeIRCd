#!/bin/sh
# BeeIRCd installer starter: downloads the program for this machine and runs
# `beeircd install`, which asks its questions and does the rest.
#
#   curl -fsSL https://beeircd.org/install.sh | sudo sh
#   wget -qO- https://beeircd.org/install.sh | sudo sh     (no curl here)
#
# BEEIRCD_URL overrides where the program comes from (a folder holding
# beeircd-linux-amd64 / beeircd-linux-arm64).
set -eu
case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "BeeIRCd has builds for amd64 and arm64, not $(uname -m)." >&2; exit 1 ;;
esac
base="${BEEIRCD_URL:-https://github.com/fhazal/BeeIRCd/releases/latest/download}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# get URL FILE: curl, else wget; a file:// folder (test installs) is copied.
get() {
	case "$1" in
	file://*) cp "${1#file://}" "$2" ;;
	*)
		if command -v curl >/dev/null 2>&1; then
			curl -fsSL -o "$2" "$1"
		elif command -v wget >/dev/null 2>&1; then
			wget -q -O "$2" "$1"
		else
			echo "BeeIRCd needs curl or wget to download itself. Install one (apt install curl, dnf install curl) and run this again." >&2
			exit 1
		fi ;;
	esac
}
echo "Downloading BeeIRCd for linux-$arch ..."
if ! get "$base/beeircd-linux-$arch" "$tmp/beeircd"; then
	echo "The download failed: $base/beeircd-linux-$arch" >&2
	exit 1
fi
chmod 755 "$tmp/beeircd"
# Questions need the keyboard even when this script comes from a pipe.
if [ -t 0 ]; then
	"$tmp/beeircd" install
elif (: </dev/tty) 2>/dev/null; then # can really be opened (not in a container without one)
	"$tmp/beeircd" install < /dev/tty
else
	echo "No terminal for the questions: download $base/beeircd-linux-$arch and run 'beeircd install' yourself." >&2
	exit 1
fi
