#!/usr/bin/env bash
# Runs the independent irctest conformance suite against a throwaway
# BeeIRCd, on a test machine, inside a disposable container.
#
#   scripts/irctest.sh <beeircd linux binary> [extra pytest args]
#
# Needs /opt/beeircd-irctest/irctest (git clone of progval/irctest, pinned
# below). The container has no network except its own loopback, so the
# running test server is never touched; every BeeIRCd it starts lives in
# irctest's temp directory and is removed afterwards.
set -euo pipefail

BIN=${1:?usage: irctest.sh <beeircd binary> [pytest args]}
shift || true
ROOT=/opt/beeircd-irctest
IRCTEST=$ROOT/irctest
PIN=7d5d761b7b67f9031daf7784e743d630def22a96 # irctest main, 31 Oct 2026
HERE=$(cd "$(dirname "$0")/.." && pwd)

git -C "$IRCTEST" checkout -q "$PIN"
cp "$HERE/tools/irctest/beeircd.py" "$IRCTEST/irctest/controllers/beeircd.py"
mkdir -p "$ROOT/bin" "$ROOT/results"
install -m 0755 "$BIN" "$ROOT/bin/beeircd"

# Python deps are installed once into a cached image.
if ! docker image inspect beeircd-irctest:py >/dev/null 2>&1; then
	docker build -q -t beeircd-irctest:py - <<'EOF'
FROM python:3.12-slim
RUN pip install --no-cache-dir "pytest!=9.0.0" pytest-timeout filelock ecdsa websockets
EOF
fi

MARKERS='not implementation-specific and not deprecated'
docker run --rm --network none \
	-v "$IRCTEST:/irctest" -v "$ROOT/bin:/opt/bee" -v "$ROOT/results:/results" \
	-w /irctest -e PATH=/opt/bee:/usr/local/bin:/usr/bin:/bin \
	beeircd-irctest:py \
	python -m pytest -p no:cacheprovider --controller=irctest.controllers.beeircd \
	-m "$MARKERS" -v --timeout=60 -rfEs --junitxml=/results/junit.xml "$@" irctest/server_tests/ \
	| tee "$ROOT/results/summary.txt"
