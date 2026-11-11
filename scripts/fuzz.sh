#!/bin/bash
# Long fuzzing for a release: every Fuzz target for TIME (default 1h), all
# at once, each in its own golang container with 3 workers. Run on the
# homelab from a copy of the tree:
#
#   scripts/fuzz.sh /opt/beeircd-test/src [1h]
#
# Results: fuzz-<target>.log next to this run; a crasher lands in the
# package's testdata/fuzz/<target>/ and fails that target.
set -u
src=${1:?usage: fuzz.sh SRC [time]}
time=${2:-1h}
cd "$src" || exit 1
pids=()
for f in internal/*/fuzz_test.go; do
	pkg=./$(dirname "$f")
	for t in $(grep -o '^func Fuzz[A-Za-z0-9_]*' "$f" | cut -d' ' -f2); do
		docker run --rm --name "bee-fuzz-$t" -v "$src:/src" -v bee-gomod:/go/pkg/mod -w /src golang:1.27 \
			go test -run '^$' -fuzz "^$t\$" -fuzztime "$time" -parallel 3 "$pkg" >"fuzz-$t.log" 2>&1 &
		pids+=($!)
	done
done
fail=0
for p in "${pids[@]}"; do wait "$p" || fail=1; done
for l in fuzz-*.log; do echo "$l: $(tail -1 "$l")"; done
[ $fail = 0 ] && echo "FUZZ PASSED" || { echo "FUZZ FAILED"; exit 1; }
