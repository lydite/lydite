---
name: scanner-exit-code-cannot-signal-crash
kind: gotcha
about: source/cli/internal/executil/executil.go
description: every scanner wrapper here exits non-zero when it finds something, so executil.Result.Err/Ok() (set from cmd.Wait() at the base level) means "crashed or found something" and cannot tell the two apart — a new Crashed field must be derived from each tool's own report instead.
anchors:
  - path: source/cli/internal/executil/executil.go
    blob: c1673845e2f127680dfa27661081668387ea4cab
  - path: source/cli/internal/typescript/biome.go
    blob: 8c2d0fe3de292708e8d52ec104220e23771894c7
confidence: verified
---

`executil.run` (`executil.go`) sets `res.Err = cmd.Wait()` unconditionally, and every
scanner CLI lydite wraps (gosec, govulncheck, Biome, cargo-audit, cargo-deny, clippy,
Semgrep, gitleaks, shellcheck) exits non-zero specifically to signal "I found
something" as much as "I crashed". `internal/typescript/biome.go` makes this explicit:
it deliberately sets `r.Err = fmt.Errorf("%d finding(s)", len(findings))` (near
`biome.go:195` before this session's edit) purely so the row renders as failing when
Biome found real lint violations — there is no crash there at all.

Any code that needs to distinguish "this tool did not produce a trustworthy answer"
(crashed, could not install, report unparseable, or the tool's own report says it did
not finish — a package that would not compile) from "this tool ran fine and found
things" cannot use `Result.Err`/`Result.Ok()`/the exit code for that question. It has
to read each tool's own structured report. `executil.Result.Crashed` (added this
session) is the field that answers this; every wrapper sets it from its own report,
never from Err/Ok. See the rule at
`agentic/rules/derive-crashed-from-the-tools-own-report-never-from-err-or-the-exit-code.md`
and `agentic/references/scanning.md`.
