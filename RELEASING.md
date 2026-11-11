# Releasing BeeIRCd

Only the maintainer does this. Server admins never need a key: their
`beeircd update` checks each release against the public key built into the
program.

## The signing key

- `~/.beeircd-release/release.key` on the maintainer's PC. Never on GitHub,
  never in this repository.
- Give it a passphrase once: `go run ./tools/release protect`. After that the
  file is useless without the passphrase, so a backup copy in the cloud or
  on a USB stick is safe. Forgetting the passphrase = losing the key.
- `release.key.pub` is the public half. It is not secret and is already in
  `internal/release/release.go` (`TrustedKeys`).
- Losing the key: servers can't get signed updates any more. Make a new key,
  add its public half to `TrustedKeys`, and ship that build by hand
  (`beeircd install` again, or copy the program); from then on it works.
- Changing keys on purpose: add the new public key to `TrustedKeys` in one
  release, sign the next release with the new key, then drop the old one.

## Making a release

1. Everything committed, CI green, `TESTING.md` gates for a release done.
2. Tag with the release notes as the tag message (one change per line,
   starting with `· `), and push the tag:

   ```
   git tag -a v1.0.1 -m "· Fixes a crash when a channel key has a comma
   · ChanServ AKICK ENFORCE now also kicks cloaked hosts"
   git push origin v1.0.1
   ```

   A version with a dash (`v1.1.0-rc1`) becomes a test version: only
   `beeircd update --rc` offers it.
3. GitHub Actions (`.github/workflows/release.yml`) runs the tests, builds
   `beeircd-linux-amd64` / `-arm64`, writes `release.json` and makes a
   **draft** release. Nothing is offered to servers yet.
4. On the maintainer's PC, sign the draft and check it:

   ```
   gh release download v1.0.1 -D dist-v1.0.1
   go run ./tools/release sign -in dist-v1.0.1/release.json
   go run ./tools/release verify -dir dist-v1.0.1
   gh release upload v1.0.1 dist-v1.0.1/release.json.sig
   ```

   `sign` refuses if the key isn't the one BeeIRCd trusts; `verify` checks
   the signature and every program's checksum, as `beeircd update` will.
5. Publish: `gh release edit v1.0.1 --draft=false`. Servers see it within a
   day (`beeircd status` shows an Update row, opers get one notice) and
   install it with `beeircd update`.

## Until the repository is public

`beeircd update` reads `[update] url` (default: this repository's latest
GitHub release, which needs a public repository). Before v1.0 point it at
any web folder holding the same files (`release.json`, `release.json.sig`,
the programs).
