---
about: a language that ships switched off (shell) needs explicit unmeasured rows when disabled, because the Disabled disposition renders nothing for an opt-out language; offByDefaultRows is the one place that distinction lives
saw:
  - source/cli/internal/stages/scan/plan.go
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/scanlang/scanlang.go
---

`PlanComponents` (`internal/stages/scan/plan.go`) marks a component `Disabled` when its language is
scanned but `scanlang.Enabled(lang, cfg)` is false; the `Disabled` doc comment leaves "whether that
is worth a row" to the caller. `recordComponents` (`cmd/lydite/scan.go`) renders a `Disabled` entry
through `offByDefaultRows(name, lang)`, which returns nil for every language except `runner.Shell`.
Silence is correct for Go/Rust/TypeScript, which are on unless a repository opts out, so it reads
as the opt-out the repository made. For shell, gated by `shell.enabled` and off by default, that
same silence would read exactly like a clean scan of a repository that never mentioned shell — the
failure `.claude/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md` forbids — so
`offByDefaultRows` returns explicit `unmeasured` `scan`/`licence`/`findings` rows naming the config
key that would turn the language on. Any future off-by-default language needs its own branch in
`offByDefaultRows`, not just a `scanlang.Enabled` key or a dispatch case.
