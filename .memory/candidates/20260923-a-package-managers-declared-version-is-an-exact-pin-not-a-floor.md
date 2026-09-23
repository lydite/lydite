---
about: source/cli/internal/toolchain/toolchain.go
saw: source/cli/internal/toolchain/toolchain.go:satisfied
---

Everywhere else `toolchain.satisfied` treats a declared version as a floor: an ambient Go 1.26.6
satisfies a `go 1.26` directive, a newer patch and all (`!olderThan(ambient, req.Version)`). A
package manager's `packageManager` pin is not a floor — Corepack itself enforces it by refusing
to run anything but the exact release named, because a newer pnpm can resolve and write a
lockfile the pinned one would not. So `satisfied`, when `Requirement.Manager != ""`, requires
`semver.Compare(ambient, req.Version) == 0` instead: an ambient release one patch newer than the
pin is provisioned past exactly as one patch older is. `ambient.go`'s `pinned()` (used only for
the manager probes) also keeps a pre-release rather than canonicalizing it away, for the same
reason — `canonical()` would read an ambient `9.0.0-rc.1` as the `9.0.0` a repo pinned.
