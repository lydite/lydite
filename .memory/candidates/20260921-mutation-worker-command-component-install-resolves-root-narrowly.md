---
about: source/cli/internal/test/run/prepare.go
saw: source/cli/internal/test/run/prepare.go:Prepare, prepareCommand; source/cli/internal/stages/mutation/run.go:prepareTarget; source/cli/internal/runner/runner.go:installNodeDeps; source/cli/internal/nodedeps/nodedeps.go:WorkspaceRoot, within
---

`prepareCommand` (`internal/test/run/prepare.go` line ~61, joining a `command:` component into
`nodedeps.Install`) passes `root` straight through from `Prepare` with no fallback. The mutation
worker's closure — built in `internal/stages/mutation/run.go`'s `prepareTarget` and routed
through the CLI's `mutationLifecycle.Prepare` — calls `Prepare` with `root == ""`, because its
`dir` sits inside a copy of the scan root rather than the scan root itself (`Prepare`'s own doc
comment says so). `internal/runner`'s `installNodeDeps` compensates for that same `root == ""`
case with the unexported `declarationRoot(dir)` fallback a runner-based component's install
benefits from; `prepareCommand` has no equivalent. With an empty `scanRoot`,
`nodedeps.WorkspaceRoot`'s `within(dir, "")` is false, so the walk's bound is `dir` itself — a
`command:` component prepared through the mutation worker can only resolve a workspace lockfile
sitting in its own directory, not one above it. Fixing it means exporting or duplicating
`declarationRoot` for `prepareCommand` to call.

Only reachable in practice for a `command:` component that `installsNodeDeps` accepts (a
`package.json` in its own directory), and only if mutation ever runs one: today
`prepareTarget` stops a component declaring a raw command at `KindRawCommand` before any
backend or worker is built, so the worker closure is never created for it.
