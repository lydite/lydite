---
about: source/cli/internal/runner/runner.go, source/cli/internal/mutation/worktree.go, source/cli/cmd/lydite/mutation.go
saw: source/cli/internal/runner/runner.go:installNodeDeps, declarationRoot; source/cli/internal/mutation/worktree.go:109; source/cli/cmd/lydite/mutation.go:771
---

A mutation worker invokes a component's `Prepare` with `dir` pointed inside a *copy* of the
scan root, not the scan root itself: `internal/mutation/worktree.go:109` calls `Prepare` with
`filepath.Join(workerDir, component)`, via the `prep` closure `cmd/lydite/mutation.go:771`
builds. The worker copies git-tracked files, so that copy carries its own `.lydite/` directory.

Consequence for any code inside `Prepare` that needs an upper bound for an upward directory
walk (e.g. `nodedeps.WorkspaceRoot`'s scan-root bound): the bound must be derived by reading the
tree `dir` actually sits in at the moment of the walk (walk up to the nearest ancestor holding
`.lydite`), not threaded down as a parameter computed once from the outer run's scan root.
Threading the outer scan root through would bound the walk against the original repository,
which the worker's `dir` is not under — the walk would refuse to resolve anything inside the
worker copy. `runner.declarationRoot(dir)` is the fix already landed for this
(`fix/fresh-checkout-races`, task 1 of `lydite/lydite#186`).
