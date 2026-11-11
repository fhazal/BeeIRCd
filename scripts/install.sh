#!/bin/sh
# BeeIRCd installer starter: downloads the program for this machine and runs
# `beeircd install`, which asks its questions and does the rest.
#
#   curl -fsSL https://beeircd.org/install.sh | sudo sh
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
echo "Downloading BeeIRCd for linux-$arch ..."
curl -fsSL -o "$tmp/beeircd" "$base/beeircd-linux-$arch"
chmod 755 "$tmp/beeircd"
# Questions need the keyboard even when this script comes from a pipe.
if [ -t 0 ]; then
	"$tmp/beeircd" install
elif [ -r /dev/tty ]; then
	"$tmp/beeircd" install < /dev/tty
else
	echo "No terminal for the questions: download $base/beeircd-linux-$arch and run 'beeircd install' yourself." >&2
	exit 1
fi
