---
name: registry-tarball-digest-does-not-replace-the-declared-pin
kind: invariant
description: A pnpm or yarn tarball is checked against both the registry's own digest and the packageManager field's declared hash, and neither stands in for the other; an unrecognised hash algorithm is a hard error.
anchors:
  - path: source/cli/internal/toolchain/provision.go
    blob: 00987d4fec52
confidence: verified
---

`downloadPackageManager` (`internal/toolchain/provision.go`) checks a pnpm/yarn tarball against two independent digests, both required. `verifyDist` (`:571`) checks `dist.integrity`/`dist.shasum`, which the npm registry publishes *about its own tarball* — the same host serving the file, so it proves internal consistency, not that the file is what the repository meant to trust. The `packageManager` field can carry its own hash after `+` (`pnpm@8.15.4+sha512.<hex>`, `Requirement.Hash`), the repository's independent pin, checked by `declaredHash.verify` via `verifyTarball` (`:556`); a tarball passing the registry's digest but not the declared one is refused (a republished registry entry would pass the first check and fail the second). An algorithm `parseDeclaredHash` (`:495`) does not recognise is a hard error, not a skipped check.

`managerCacheKey` (`:543`) folds the declared hash into the cache directory name (`<manager>-<version>` → `<manager>-<version>+<algo>.<hex>`), so a repository re-pinning the same version to different bytes cannot be handed a previously verified copy.
