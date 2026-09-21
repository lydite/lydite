---
about: source/cli/cmd/lydite/test.go
saw: source/cli/cmd/lydite/test.go:prepareCommand, source/cli/internal/runner/runner.go:installNodeDeps
---

`prepareCommand` (added for `lydite/lydite#199`, joining a `command:` component into
`nodedeps.Install`) passes `root` straight through from its caller with no fallback. The
mutation worker's closure calls `prepare` with `root == ""`, because its `dir` sits inside a
copy of the scan root rather than the scan root itself (see the comment above `prepare` at
`source/cli/cmd/lydite/test.go`). `internal/runner`'s own `installNodeDeps` compensates for that
same `root == ""` case with an unexported `declarationRoot(dir)` fallback that a runner-based
component's install already benefits from — `prepareCommand` has no equivalent, so a
`command:` component prepared through the mutation path can only resolve a workspace lockfile
sitting in its own directory, not one above it. Not yet fixed; fixing it means exporting or
duplicating `declarationRoot` for `prepareCommand` to call.
