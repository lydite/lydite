---
about: internal/depdelta.Detect and cmd/lydite/review_depdelta.go's measureDependencies
saw: implementing .lydite/exemptions.yml's dependency-version-bump entry (chore/declare-the-bump-exemption)
---

`internal/depdelta.Detect` (source/cli/internal/depdelta/manifest.go) recognizes a fixed set of
base filenames as manifests: `go.mod`, `go.sum`, `Cargo.lock`, `package-lock.json`, plus the
named-but-unreadable `yarn.lock`, `pnpm-lock.yaml`, `requirements.txt`, `poetry.lock`,
`Pipfile.lock`. It does **not** recognize `Cargo.toml` or `package.json` — those return
`ManifestNone`.

In `cmd/lydite/review_depdelta.go`'s `measureDependencies`, a path that detects as `ManifestNone`
is `continue`d past outright — it never becomes a `manifestDelta` entry, so it is never reported
as `Unmeasured` the way an unreadable ecosystem (pip, yarn, pnpm) is. This distinction matters for
anyone writing an `.lydite/exemptions.yml` entry with a `versions:` condition: listing `Cargo.toml`
or `package.json` in an exemption's `paths` does not get that file measured — it just marks the
path as "covered," so a change touching only the manifest (not its lockfile) would satisfy
`versionsPatchAndMinor` vacuously (empty delta list passes) and clear the exemption with zero
dependency-delta evidence. The safe pattern is to list only the lockfile (`Cargo.lock`,
`package-lock.json`) in `paths`, never the manifest beside it — a real Dependabot bump that also
touches the manifest then has an uncovered path and is referred, same as before the exemption
existed, rather than silently trusting a file `depdelta` cannot read.
