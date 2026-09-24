---
about: source/cli/cmd/lydite/scan.go
saw: feature/shell-is-scanned branch, adding shell.enabled (off by default)
---

`cmd/lydite/scan.go`'s ordinary path for a disabled language (`!langEnabled(lang, cfg)`) emits
no rows at all for a component declaring that language — correct for Go/Rust/TypeScript,
which are on unless a repository opts out, so silence reads as the opt-out the repository made.
For a language that ships off-by-default (shell, gated by `shell.enabled`), that same silence
would read exactly like a clean scan of a repository that never mentioned shell at all — the
failure `.claude/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md` forbids.
The fix lives in `offByDefaultRows(name, lang runner.Lang) []ui.Row`: it returns nil for every
language except the one(s) that are off by default, and for those returns explicit `unmeasured`
scan/licence/findings rows naming the config key that would turn the language on. Any future
off-by-default language addition needs its own branch in that function, not just a
`langEnabled`/dispatch case.
