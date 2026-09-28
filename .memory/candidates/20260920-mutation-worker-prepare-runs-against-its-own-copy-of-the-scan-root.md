---
about: source/cli/internal/stages/mutation/run.go, source/cli/internal/mutation/worktree.go, source/cli/internal/runner/runner.go, source/cli/cmd/lydite/mutation.go
saw: source/cli/internal/stages/mutation/run.go:prepareTarget; source/cli/internal/mutation/worktree.go:109; source/cli/internal/runner/runner.go:installNodeDeps, declarationRoot; source/cli/cmd/lydite/mutation.go:mutationLifecycle.Prepare; source/cli/internal/test/run/prepare.go:Prepare
---

A mutation worker invokes a component's `Prepare` with `dir` pointed inside a *copy* of the
scan root, not the scan root itself: `internal/mutation/worktree.go:109` calls `t.Prepare` with
`filepath.Join(workerDir, component)`, via the `prep` closure `prepareTarget` builds in
`internal/stages/mutation/run.go` (line ~313). That closure calls
`in.Lifecycle.Prepare(ctx, c.Name, suite, dir, "", in.Config, tc)` — an empty root, on purpose —
which the CLI's `mutationLifecycle.Prepare` (`cmd/lydite/mutation.go` line ~374) forwards to the
same `prepare` helper `lydite test` uses, i.e. `internal/test/run/prepare.go`'s `Prepare`, whose
doc comment names this caller as the one that passes `""`. The component's own baseline
preparation in `mutateComponent` (run.go line ~373) passes the real scan root `in.Dir` instead.
The worker copies git-tracked files, so that copy carries its own `.lydite/` directory.

Consequence for any code inside `Prepare` that needs an upper bound for an upward directory walk
(e.g. `nodedeps.WorkspaceRoot`'s scan-root bound): for the worker, the bound must be derived by
reading the tree `dir` actually sits in (walk up to the nearest ancestor holding `.lydite`), not
the outer run's scan root, which the worker's `dir` is not under — that would stop the walk
short inside the copy or let it climb into the repository the copy came from.
`internal/runner/runner.go`'s `installNodeDeps` and `installPythonDeps` take a `root` and fall
back to `declarationRoot(dir)` only when it is `""`; every other caller threads the real root
(see the `thread-the-real-root-tree-derived-bound-is-the-mutation-worker-fallback` rule).
