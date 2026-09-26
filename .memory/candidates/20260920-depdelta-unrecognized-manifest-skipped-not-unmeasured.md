---
about: depdelta.Detect does not recognize Cargo.toml or package.json, and reviewdecision's measureDependencies skips an unrecognized path outright rather than reporting it Unmeasured — so an exemption listing a manifest clears a versions condition vacuously
saw:
  - source/cli/internal/depdelta/manifest.go
  - source/cli/internal/reviewdecision/dependencies.go
  - .claude/rules/an-exemptions-entrys-paths-list-only-depdelta-readable-manifests.md
---

Re-checked on `refactor/flow-architecture-clearance-pilot`: `measureDependencies` and
`versionsPatchAndMinor` moved from `cmd/lydite/review_depdelta.go` into
`internal/reviewdecision/dependencies.go` (both still unexported). The claim holds unchanged.

`depdelta.Detect` (`internal/depdelta/manifest.go`) is `manifests[path.Base(p)]` over a fixed map:
`go.mod`, `go.sum`, `Cargo.lock`, `package-lock.json`, plus the named-but-unreadable `yarn.lock`,
`pnpm-lock.yaml`, `requirements.txt`, `poetry.lock`, `Pipfile.lock`. `Cargo.toml` and
`package.json` are not in it and detect as `ManifestNone`.

In `reviewdecision.measureDependencies`, a `ManifestNone` path is `continue`d past — it never
becomes a `ManifestDelta`, so unlike an unreadable ecosystem (which gets `Unmeasured: "no
dependency reader for <ecosystem>"`) it is never reported at all. `versionsPatchAndMinor` fails
only on an `Unmeasured` or non-patch/minor entry, so an empty delta list passes it.

Consequence for `.lydite/exemptions.yml`: listing `Cargo.toml` or `package.json` in a `paths`
entry with a `versions:` condition marks it covered without measuring it, so a change touching
only the manifest clears the exemption with zero dependency evidence. List only the lockfile
`Detect` reads; a real bump that also touches the manifest then has an uncovered path and is
referred.
