---
name: off-by-default-language-needs-its-own-unmeasured-rows
kind: gotcha
description: A language shipping switched off (shell) needs explicit unmeasured rows when disabled, because the Disabled disposition renders nothing; offByDefaultRows is the one place that lives.
anchors:
  - path: source/cli/internal/stages/scan/plan.go
    blob: 6e5358165f49
  - path: source/cli/cmd/lydite/scan.go
    blob: f714a442e1bf
  - path: source/cli/internal/scanlang/scanlang.go
    blob: e4224f1d7728
confidence: verified
---

`PlanComponents` (`internal/stages/scan/plan.go`) marks a component `Disabled` when its language is scanned but `scanlang.Enabled(lang, cfg)` is false; the `Disabled` doc leaves "whether that is worth a row" to the caller. `recordComponents` (`cmd/lydite/scan.go`) renders a `Disabled` entry through `offByDefaultRows(name, lang)` (`scan.go:356`), which returns nil for every language except `runner.Shell`. Silence is right for Go/Rust/TypeScript, which are on unless a repository opts out — it reads as the opt-out the repository made. For shell, gated by `shell.enabled` and off by default, the same silence would read exactly like a clean scan of a repository that never mentioned shell — the failure `.claude/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md` forbids — so `offByDefaultRows` returns explicit `unmeasured` `scan`/`licence`/`findings` rows naming the config key that would turn the language on. Any future off-by-default language needs its own branch there, not just a `scanlang.Enabled` key or a dispatch case ([[scanlang-gates-scan-dispatch]]).
