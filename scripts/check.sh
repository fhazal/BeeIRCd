#!/usr/bin/env sh
# Runs every check the repo expects before a commit: format, vet, lint, tests.
# Usage: scripts/check.sh   (from the repo root)
set -eu

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "Not gofmt'ed:"
	echo "$unformatted"
	exit 1
fi

go vet ./...
staticcheck ./...

# -race needs cgo; run it where a C compiler exists (Linux CI, the homelab).
if [ "$(go env CGO_ENABLED)" = "1" ] && command -v gcc >/dev/null 2>&1; then
	go test -race -count=1 ./...
else
	go test -count=1 ./...
fi

echo "All checks passed."
