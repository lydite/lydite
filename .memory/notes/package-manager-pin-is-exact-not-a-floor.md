---
name: package-manager-pin-is-exact-not-a-floor
kind: gotcha
description: toolchain.satisfied treats a runtime version as a floor but a packageManager pin as exact, because Corepack refuses any other release; a newer ambient pnpm or yarn is provisioned past.
anchors:
  - path: source/cli/internal/toolchain/toolchain.go
    blob: 30dbfa828d12
  - path: source/cli/internal/toolchain/ambient.go
    blob: 5cd4894d0e7d
confidence: verified
---

Everywhere else `toolchain.satisfied` (`internal/toolchain/toolchain.go:605`) treats a declared version as a floor: an ambient Go 1.26.6 satisfies a `go 1.26` directive, newer patch and all. A package manager's `packageManager` pin is not a floor — Corepack refuses to run anything but the exact release named, because a newer pnpm can resolve and write a lockfile the pinned one would not. So when `Requirement.Manager != ""` (`toolchain.go:609`) `satisfied` requires `semver.Compare(ambient, req.Version) == 0`: an ambient release one patch newer than the pin is provisioned past exactly as one patch older is. `ambient.go`'s `pinned()` (used only for manager probes, `ambient.go:92`) also keeps a pre-release rather than canonicalizing it away — `canonical()` would read an ambient `9.0.0-rc.1` as the `9.0.0` a repo pinned.
