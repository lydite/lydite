---
name: depdelta-unrecognized-manifest-skipped-not-unmeasured
kind: gotcha
description: depdelta.Detect does not recognize Cargo.toml or package.json, and reviewdecision's measureDependencies skips an unrecognized path outright rather than reporting it Unmeasured — so an exemption entry listing a manifest (instead of the lockfile) clears a versions condition vacuously.
anchors:
  - path: source/cli/internal/depdelta/manifest.go
    blob: 161a6483e641
  - path: source/cli/internal/reviewdecision/dependencies.go
    blob: 36b11ae58a86
  - path: .claude/rules/an-exemptions-entrys-paths-list-only-depdelta-readable-manifests.md
    blob: 15fff787cbaf
confidence: verified
---

`depdelta.Detect` (`manifest.go:74`, `manifests[path.Base(p)]` over a fixed map) recognizes `go.mod`,
`go.sum`, `Cargo.lock`, `package-lock.json`, plus the named-but-unreadable `yarn.lock`,
`pnpm-lock.yaml`, `requirements.txt`, `poetry.lock`, `Pipfile.lock`. `Cargo.toml` and `package.json`
are not in it and detect as `ManifestNone`.

In `reviewdecision.measureDependencies`, a `ManifestNone` path is `continue`d past — it never
becomes a `ManifestDelta`, so unlike an unreadable ecosystem (which gets `Unmeasured: "no dependency
reader for <ecosystem>"`) it is never reported at all. `versionsPatchAndMinor` fails only on an
`Unmeasured` or non-patch/minor entry, so an empty delta list passes it.

Consequence for `.lydite/exemptions.yml`: listing `Cargo.toml` or `package.json` in a `paths` entry
with a `versions:` condition marks it covered without measuring it, so a change touching only the
manifest clears the exemption with zero dependency evidence. List only the lockfile `Detect` reads;
a real bump that also touches the manifest then has an uncovered path and is referred.
</content>
