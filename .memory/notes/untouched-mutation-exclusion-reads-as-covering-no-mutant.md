---
name: untouched-mutation-exclusion-reads-as-covering-no-mutant
kind: gotcha
description: In a diff-scoped mutation run every exclude_from_mutation declaration in a touched file is checked, so an untouched, still-valid one is reported as covering no mutant.
anchors:
  - path: source/cli/internal/mutation/sites.go
    blob: aa193f9bd4ea
  - path: source/cli/internal/mutation/mutation.go
    blob: e25be6802d90
  - path: source/cli/internal/stages/mutation/generate.go
    blob: f96c20e16e90
confidence: suspect
---

`generate` in `internal/stages/mutation/generate.go` calls `mutation.Generate` once per file with at least one line both in the diff scope and reported executed, and writes every returned `UnmatchedDeclaration` to the run's diagnostics as `lydite: <component>: <path>:<line>: [lydite:exclude_from_mutation] covers no mutant` (text from `UnmatchedDeclaration.String()`, `internal/mutation/mutation.go:344`).

The generators read *every* declaration in the file, but `sites.add` (`internal/mutation/sites.go:49`) keeps a mutant only if every line it spans is in the scoped `lines`, and `sites.resolve` (`:90`) checks declarations against those kept spans only. A declaration on a line the change did not touch (or did not execute) has no mutant to match however correct it is, and is reported every time its file is otherwise in scope. Concretely, the `stop()` declaration near `cmd/lydite/mutation.go:98` draws this line on stderr for any PR that changes and executes another line of `mutation.go`. It is diagnostic only — no row, finding or verdict effect — but read at face value it tells a reviewer to remove a declaration that is still needed. `agentic/references/mutation.md` and ADR 0027 say a declaration covering no mutant is reported, and neither says the check runs over declarations outside the change. The `sites.go`/`mutation.go` mechanism was re-read; the concrete `mutation.go:98` example was not.
