# BeeIRCd

An IRC server with its services built in: NickServ, ChanServ, MemoServ, OperServ and AdminServ in one program, with BeeIRCd-to-BeeIRCd linking, a web admin panel (Hive Panel) and a web chat (BeeRoom) to follow.

**Status:** early work, not usable yet. The plan is in [SPEC.md](SPEC.md).

## Building

Needs Go 1.27 or newer.

```sh
go build ./cmd/beeircd
./beeircd version
```

## Checks

```sh
scripts/check.sh
```

Runs gofmt, `go vet`, [staticcheck](https://staticcheck.dev) and the tests.

## Layout

| Folder | What lives there |
|---|---|
| `cmd/beeircd` | the `beeircd` program and its commands |
| `internal/` | the server, services and storage, one package each |
| `scripts/` | developer scripts |

## Licence

GNU Affero General Public License v3.0 or later, see [LICENSE](LICENSE). If you run a changed BeeIRCd for other people, you share your changes.

---

A BajuBesi Labs project.
