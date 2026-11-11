# Testing BeeIRCd

Three layers, one tool each. Results always use these words:

| Word | Means |
| --- | --- |
| **PASS** | ran and behaved as the protocol or our spec says |
| **FAIL** | ran and behaved wrongly |
| **NOT IMPLEMENTED** | the feature doesn't exist yet (never counted as a failure) |
| **NOT TESTED** | exists, but nothing checked it this time |
| **BLOCKED** | couldn't run (missing environment, dependency, access) |

A server that starts is not a PASS. Tests check behaviour.

## 1. Unit and protocol tests (`go test`)

```
scripts/check.sh                 # gofmt, vet, staticcheck, all tests (race where gcc exists)
go test -count=6 ./internal/...  # flakiness check before a commit
BEEIRCD_LOAD=1 go test -run TestLoad5000 -v ./internal/server/   # 5,000-client load gate
```

- **Protocol tests** (`internal/server/*_test.go`) start a real server on
  `127.0.0.1:0` with a temp directory and database, and talk to it with real
  TCP clients (`login`, `registered`, `founded`, `boss`, `expect`,
  `expectNumeric`, `quiet`, `sync`).
- **Every line a test client reads is checked** by `lineProblem`
  (`protocheck_test.go`): CRLF ending, no CR/LF/NUL inside, 512-byte body,
  8191-byte tags, valid tag keys, a command or 3-digit numeric with a target,
  no empty middle parameters. It is written separately from `internal/irc`,
  so the server's parser isn't checking itself.
- **Every test server is checked when it stops**:
  - a copy of the network fed only with events must equal the real one (links depend on it);
  - the services cache must equal the database;
  - no goroutines may be left running (leak check).
- **Fuzz targets** run their seed corpus in every `go test`. They are
  `FuzzParse`, `FuzzSplitFraming`, `FuzzNormalizeBan` and `FuzzTags` in `irc`;
  `FuzzParseFlood`, `FuzzStripColour` and `FuzzReadLine` in `server`;
  `FuzzParseMlock` in `services`. Run them longer before a release:

  ```
  go test -run '^$' -fuzz=FuzzParse -fuzztime=5m ./internal/irc/
  ```

  A crash found by fuzzing lands in `testdata/fuzz/` and stays there as a
  regression seed: commit it with the fix.
- **Race detector**: `go test -race` needs cgo. It runs in CI (Linux) and on the
  homelab (`docker run --rm -v <tree>:/src -w /src golang:1.27 go test -race -count=3 ./...`).
- **Let's Encrypt against Pebble** (7c): `TestPebble` in `internal/acmecert` is
  skipped unless a Pebble test CA runs. Build Pebble from
  github.com/letsencrypt/pebble, start it with `PEBBLE_VA_NOSLEEP=1 pebble
  -config test/config/pebble-config.json`, then
  `BEEIRCD_PEBBLE=https://127.0.0.1:14000/dir BEEIRCD_PEBBLE_HTTP=:5002 go test ./internal/acmecert -run Pebble`.
  For the whole server: add `pebble-challtestsrv -dnsserver 127.0.0.1:8053`
  (answers every name with 127.0.0.1), start Pebble with `-dnsserver
  127.0.0.1:8053`, and run beeircd with `[tls] acme = true`,
  `acme_directory = "https://127.0.0.1:14000/dir"`, `acme_http = ":5002"` and
  `SSL_CERT_FILE=test/certs/pebble.minica.pem` (Linux; done 12 Nov 2026 on the homelab).

- **Installer on clean machines** (7d): `scripts/cleanmachine.sh <beeircd
  linux binary> <installcheck binary> [image ...]` on a host with LXD (the
  homelab; LXD there has a storage pool and **no network**, so the host is
  not touched). Per image (default Debian 13 + Ubuntu 24.04; on the homelab
  use the local aliases `deb13 ubu24`): fresh container, `beeircd install`
  with scripted answers, status + check, `tools/installcheck` (both ports,
  NickServ REGISTER after 30 s, wrong /oper 464, admin /oper 381), backup
  timer, stop/start, reboot (waits up to 3 min: without a network systemd
  waits 2 min for one), uninstall, `--purge`, container deleted.
  Build `installcheck` with `GOOS=linux go build ./tools/installcheck`.

## 2. Independent conformance (irctest)

[irctest](https://github.com/progval/irctest) is the suite Ergo, Solanum,
InspIRCd and UnrealIRCd use. It knows nothing about BeeIRCd's code.

```
scripts/irctest.sh <beeircd linux binary> [pytest args]     # on the homelab
```

- **Expected failures, not bugs:** the 8 private-message tests in
  `chathistory.py` (MessagesToSelf, the DM tests, Targets ×2). BeeIRCd keeps
  history for channels only in v1.0 (decided 12 Nov 2026; see
  AUDIT-TESTING.md "DM history"). Any other failure is a bug.
- Runs in a disposable container with `--network none`. Each test starts its
  own BeeIRCd in a temp directory, so the test server is never touched.
- The controller is `tools/irctest/beeircd.py` and the irctest commit is pinned in the script.
- Markers: `not implementation-specific and not deprecated` (as for other servers).
- Results: `/opt/beeircd-irctest/results/` (summary + junit.xml).
- The baseline and the reason for every known failure are in
  `AUDIT-TESTING.md` §9 and §12 (current: 270 pass / 4 fail / 163 skip /
  3 xpass, 12 Nov 2026; the 4 fails are known differences or harness limits).
- **Rule:** a failure is fixed in BeeIRCd only if it is a real protocol
  violation. Where irctest assumes another server's behaviour, BeeIRCd keeps
  its own and the test is listed as an expected difference.

## 3. Live checks against the test server (`tools/livecheck`)

```
go run ./tools/livecheck -addr 192.168.50.76:26667 -tls 192.168.50.76:26697 \
    -cert zt-cert.pem -key zt-key.pem [-suites core,opers,modes] [-admin Belon]
```

- Non-destructive: it uses only `zt*` nicks and channels, never opers up, and
  drops what it registered.
- `-admin` adds one wrong-password `/oper` try, which counts toward that
  account's lockout. Leave it off unless wanted.
- Replaces the Python `live157.py`, `live158.py` and `live159a.py` with the same checks.
- Tell the owner before running it while they are on the server.

## 4. Performance (homelab only)

- `bench2.sh bee|insp`: 1 core + 512 MB, 5,000 bots, compared with InspIRCd
  4.12.1. It lives in `/opt/beeircd-test/bench/`. Numbers go in HANDOFF per
  step and in ENGINEERING.md per stage.
- Not in CI: it needs the homelab and minutes of CPU.

## 5. CI (GitHub Actions)

`.github/workflows/ci.yml` runs on every push and pull request:
- gofmt, vet, staticcheck;
- `go test -race` (fuzz seeds, protocol and leak checks included);
- govulncheck.

It has read-only permissions, no secrets and no access to the homelab.
irctest, benchmarks, soak tests and long fuzzing stay on the homelab.

## 6. Known open risks

- **S3, connection floods: fixed in 7a.** Per-IP connect throttle (5 per
  60 s, ZLINE after repeat refusals) and caps on connections still logging
  in (3 per IP, 500 in total), checked before any DNS or DNSBL work. LAN
  IPs skip them, so the LAN-only test server sees no change.
