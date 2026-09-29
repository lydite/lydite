---
name: mutation-worker-prepare-gets-no-scan-root
kind: gotcha
description: A mutation worker calls Prepare inside a copy of the scan root with an empty root, so anything needing an upward-walk bound must derive it from the copy; prepareCommand has no such fallback, unlike installNodeDeps.
anchors:
  - path: source/cli/internal/stages/mutation/run.go
    blob: f2d6f052cb32
  - path: source/cli/internal/mutation/worktree.go
    blob: b98ddba3d9db
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
  - path: source/cli/internal/test/run/prepare.go
    blob: f03a29baedc8
confidence: suspect
---

A mutation worker invokes a component's `Prepare` with `dir` pointed inside a *copy* of the scan root, not the scan root itself: `internal/mutation/worktree.go` calls `t.Prepare` with `filepath.Join(workerDir, component)`, via the `prep` closure `prepareTarget` (`internal/stages/mutation/run.go:278`) builds. That closure calls `in.Lifecycle.Prepare(ctx, c.Name, suite, dir, "", ...)` — an empty root, on purpose — which the CLI's `mutationLifecycle.Prepare` forwards to the same `Prepare` `lydite test` uses (`internal/test/run/prepare.go:29`), whose doc names this caller as the one passing `""`. The component's own baseline preparation in `mutateComponent` passes the real scan root `in.Dir`. The worker copies git-tracked files, so the copy carries its own `.lydite/`.

Consequence for code in `Prepare` needing an upper bound for an upward walk (e.g. `nodedeps.WorkspaceRoot`'s scan-root bound): for the worker, derive it by reading the tree `dir` actually sits in (nearest ancestor holding `.lydite`), not the outer run's scan root. `installNodeDeps` (`internal/runner/runner.go:895`) falls back to `declarationRoot(dir)` (`:921`) when `root == ""`; **`prepareCommand` (`prepare.go:61`) has no equivalent**, so with an empty root `nodedeps.WorkspaceRoot`'s `within(dir, "")` is false and the walk is bounded by `dir` itself — a `command:` component prepared through the worker could only resolve a lockfile in its own directory. Fixing it means exporting or duplicating `declarationRoot`.

Only reachable for a `command:` component that `installsNodeDeps` accepts and only if mutation ever runs one; today `prepareTarget` stops a raw-command component at `KindRawCommand` before any worker exists. This note was carried over from earlier candidates; the line-level pointers were not all re-checked, hence `suspect`.
