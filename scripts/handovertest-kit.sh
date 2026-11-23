#!/bin/sh
# Builds the kit for scripts/handovertest.sh (run from the repository root,
# needs Go): v1 (1.4.0-test, installed first) and r2 (1.4.1-test, a signed
# release), both from this tree, so they speak the same door keeper
# protocol; plus installcheck, hovcheck (the bots) and livecheck. Signed
# with a THROWAWAY key that only these test builds trust.
#
#   scripts/handovertest-kit.sh <out dir>
set -eu
out=$1
rm -rf "$out"
mkdir -p "$out/key" "$out/r2"
go build -o "$out/key/release-tool" ./tools/release 2>/dev/null || go build -o "$out/key/release-tool.exe" ./tools/release
tool=$(ls "$out"/key/release-tool* | head -1)
"$tool" keygen -key "$out/key/test.key" >/dev/null
pub=$(cat "$out/key/test.key.pub")
ld() { echo "-X github.com/fhazal/beeircd/internal/release.TestKey=$pub -X github.com/fhazal/beeircd/internal/version.Version=$1 -X github.com/fhazal/beeircd/internal/version.Commit=test"; }
build() { CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(ld "$1")" -o "$2" ./cmd/beeircd; }
go build -ldflags "-X github.com/fhazal/beeircd/internal/release.TestKey=$pub" -o "$tool" ./tools/release
build 1.4.0-test "$out/v1"
build 1.4.1-test "$out/r2/beeircd-linux-amd64"
"$tool" manifest -version 1.4.1-test -dir "$out/r2" >/dev/null
"$tool" sign -key "$out/key/test.key" -in "$out/r2/release.json" >/dev/null
for t in installcheck hovcheck livecheck; do
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$out/$t" ./tools/$t
done
rm -rf "$out/key"
echo "kit ready in $out"
