#!/bin/sh
# Builds the kit for scripts/updatetest.sh (run from the repository root,
# needs Go): three test releases signed with a THROWAWAY key that only these
# test builds trust. The real release key is never used.
#
#   scripts/updatetest-kit.sh <out dir>
set -eu
out=$1
rm -rf "$out"
mkdir -p "$out/key" "$out/r2" "$out/r3" "$out/rbad"
go build -o "$out/key/release-tool" ./tools/release 2>/dev/null || go build -o "$out/key/release-tool.exe" ./tools/release
tool=$(ls "$out"/key/release-tool* | head -1)
"$tool" keygen -key "$out/key/test.key" >/dev/null
pub=$(cat "$out/key/test.key.pub")
ld() { echo "-X github.com/fhazal/beeircd/internal/release.TestKey=$pub -X github.com/fhazal/beeircd/internal/version.Version=$1 -X github.com/fhazal/beeircd/internal/version.Commit=test"; }
build() { CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(ld "$1")" -o "$2" ./cmd/beeircd; }
# The tool that signs must trust the test key too (it checks what it signs).
go build -ldflags "-X github.com/fhazal/beeircd/internal/release.TestKey=$pub" -o "$tool" ./tools/release
build 1.0.0-test "$out/v1"
build 1.0.1-test "$out/r2/beeircd-linux-amd64"
# 1.0.2-test passes every check but never answers once started.
cat > "$out/r3/beeircd-linux-amd64" <<'EOF'
#!/bin/sh
case "$1" in
version) echo "BeeIRCd 1.0.2-test (broken)" ;;
upgrade-config) cp "$3" "$5" ;;
check) echo "  ✓ safe to restart" ;;
run) echo "broken test build: refusing to start" >&2; exit 1 ;;
esac
EOF
for r in r2 r3; do
	v=$([ $r = r2 ] && echo 1.0.1-test || echo 1.0.2-test)
	"$tool" manifest -version "$v" -dir "$out/$r" >/dev/null
	"$tool" sign -key "$out/key/test.key" -in "$out/$r/release.json" >/dev/null
done
# rbad: r2's signed release with the version changed afterwards.
cp "$out/r2/"* "$out/rbad/"
sed -i 's/1.0.1-test/1.0.3-test/' "$out/rbad/release.json"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$out/installcheck" ./tools/installcheck
rm -rf "$out/key"
echo "kit ready in $out"
