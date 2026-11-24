#!/bin/bash
# Long fuzzing for a release: every Fuzz target for TIME (default 1h), all
# at once, each in its own golang container with 3 workers. Run on the
# test machine from a copy of the tree:
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
# Every *_test.go, not only fuzz_test.go: targets also live in files like
# websocket_fuzz_test.go and insp_test.go. Names carry the package because
# two packages can have a target with the same name (FuzzParse).
for f in $(grep -l '^func Fuzz' internal/*/*_test.go); do
	pkg=./$(dirname "$f")
	for t in $(grep -o '^func Fuzz[A-Za-z0-9_]*' "$f" | cut -d' ' -f2); do
		n=$(basename "$pkg")-$t
		docker run --rm --name "bee-fuzz-$n" -v "$src:/src" -v bee-gomod:/go/pkg/mod -w /src golang:1.27 \
			go test -run '^$' -fuzz "^$t\$" -fuzztime "$time" -parallel 3 "$pkg" >"fuzz-$n.log" 2>&1 &
		pids+=($!)
	done
done
fail=0
for p in "${pids[@]}"; do wait "$p" || fail=1; done
for l in fuzz-*.log; do echo "$l: $(tail -1 "$l")"; done
[ $fail = 0 ] && echo "FUZZ PASSED" || { echo "FUZZ FAILED"; exit 1; }
