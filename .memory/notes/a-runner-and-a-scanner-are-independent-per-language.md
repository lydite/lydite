---
name: a-runner-and-a-scanner-are-independent-per-language
kind: invariant
description: runner.Runs answers whether a language has a runner and scanlang.Scanned whether it has a scanner; Python has a runner and no scanner, Shell a scanner and no runner, and neither list derives from the other.
anchors:
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
  - path: source/cli/internal/scanlang/scanlang.go
    blob: e4224f1d7728
  - path: source/cli/internal/stages/scan/plan.go
    blob: 6e5358165f49
  - path: source/cli/internal/orphan/orphan.go
    blob: 35f44afe37d8
  - path: source/cli/internal/crap/treesitter.go
    blob: a8d9f43c7f2e
confidence: verified
---

`runner.SourceExts()`/`LangForExt` (`internal/runner/runner.go:164`) answer "is this file source" and cover Python and Shell (`.py`, `.sh`, `.bash`). Python has a runner (`python-pytest`) and no scanner; Shell has a scanner (`internal/shell`, ShellCheck) and no runner — #187 settled that Shell never gets one, because `runner.Runs` (`runner.go:132`, derived from the registry) flipping true for it would make `crap.skipped` (`internal/crap/treesitter.go`, `ok && runner.Runs(lang)`) report every `.sh` skipped forever with no grammar to check against. Test-side path-driven code must ask `Runs` before treating a file as something a test-side gate could act on.

`scanlang.Scanned(lang)` (`internal/scanlang/scanlang.go:27`, ADR 0056) is the separate, deliberately runner-independent question "does lydite have a scanner for this language": an explicit enumeration (Go, Rust, TypeScript, Shell). It is the one list `PlanComponents` (`internal/stages/scan/plan.go`) and `orphan.Unscanned` (`internal/orphan/orphan.go`) both read, so the scan and the unscanned warning cannot disagree. `PlanComponents` asks `Scanned` before `Enabled`: `Enabled` answers false for any language with no config key, so a language with a runner and no scanner would otherwise land in `Disabled`, as though the repository had switched it off, instead of `Unscanned` with its three unmeasured rows. `lang: shell` (no runner at all) is gated off by default behind `shell.enabled` in `.lydite/config.yml` since ShellCheck fails on every diagnostic including style.
