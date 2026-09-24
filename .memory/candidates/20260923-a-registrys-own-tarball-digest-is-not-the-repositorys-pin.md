---
about: source/cli/internal/toolchain/provision.go
saw: source/cli/internal/toolchain/provision.go:verifyTarball,verifyDist,declaredHash.verify,managerCacheKey
---

Downloading a pnpm/yarn release, `downloadPackageManager` checks a tarball against two
independent digests, and both are required — one does not stand in for the other.
`verifyDist` checks it against `dist.integrity`/`dist.shasum`, which the npm registry publishes
*about its own tarball*: that digest comes from the same host serving the file, so it proves
internal consistency, not that the file is what the repository actually meant to trust. The
`packageManager` field can carry its own hash after `+` (`pnpm@8.15.4+sha512.<hex>`,
`Requirement.Hash`) — this is the repository's independently-authored pin, checked separately by
`declaredHash.verify` via `verifyTarball`, and a tarball that satisfies the registry's own digest
but not the declared one is still refused (a compromised/republished registry entry would pass
the first check and fail the second). An algorithm `parseDeclaredHash` doesn't recognise is a
hard error, not a skipped check — an unverifiable pin is treated the same as a violated one, not
as no pin.

`managerCacheKey` folds the declared hash into the cache directory name (`<manager>-<version>` →
`<manager>-<version>+<algo>.<hex>` when a hash is declared) so a repository that re-pins the same
version string to different bytes (found a supply-chain issue, republished under the same
version) can't be handed a previously-unpacked-and-verified copy that was checked against the old
hash.
