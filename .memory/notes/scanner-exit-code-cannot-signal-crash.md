---
name: scanner-exit-code-cannot-signal-crash
kind: gotcha
description: Every wrapped scanner exits non-zero when it finds something, so executil.Result.Err cannot distinguish a crash from findings; Crashed must be derived from each tool's own report.
anchors:
  - path: source/cli/internal/executil/executil.go
    blob: 1f8345f1508d
  - path: source/cli/internal/typescript/biome.go
    blob: 8c2d0fe3de29
confidence: verified
---

`executil.run` (`internal/executil/executil.go`) sets `res.Err = cmd.Wait()` unconditionally, and every scanner CLI lydite wraps (gosec, govulncheck, Biome, cargo-audit, cargo-deny, clippy, Semgrep, gitleaks, shellcheck) exits non-zero to signal "I found something" as much as "I crashed". `internal/typescript/biome.go` makes this explicit: it deliberately sets `r.Err = fmt.Errorf("%d finding(s)", len(findings))` purely so the row renders failing when Biome found real violations — there is no crash.

Code that needs "this tool did not produce a trustworthy answer" (crashed, could not install, report unparseable, or the tool's own report says it did not finish) cannot use `Result.Err`/`Ok()`/the exit code. It must read each tool's structured report. `executil.Result.Crashed` (`executil.go:85`) answers this; every wrapper sets it from its own report, never from Err/Ok. See `agentic/rules/derive-crashed-from-the-tools-own-report-never-from-err-or-the-exit-code.md` and `agentic/references/scanning.md`.
