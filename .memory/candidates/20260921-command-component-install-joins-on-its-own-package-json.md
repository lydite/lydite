---
about: source/cli/cmd/lydite/test.go
saw: source/cli/cmd/lydite/test.go:prepare, prepareCommand, installsNodeDeps, installNote
---

A `command:` component (no `runner:`, so `runner.Lookup` fails and `r.Lang` does not exist)
joins lydite's coordinated `nodedeps.Install` only when its own directory holds a `package.json`
— not merely when it resolves a shared JS workspace root via `nodedeps.WorkspaceRoot`. Resolving
a root is not sufficient on its own: a Go or Rust `command:` component can sit as a sibling
under the same ancestor workspace root (e.g. beside `packages/ui`) without being one of that
workspace's own packages, and handing it a node install it never asked for is a worse failure
than the lockfile race the join exists to prevent (`lydite/lydite#199`).

`installsNodeDeps(dir, c)` is the single predicate both `prepareCommand` (the install path) and
`installNote` (the "no install ran, no root resolved" report row) key off, so a `command:`
component that joins the install gets the same failure-row and unmeasured-row visibility a
runner-based TypeScript component already had — before this, `installNote` gated on
`r.Lang != runner.TypeScript`, which unconditionally hid the row for every `command:` component
regardless of whether it should have joined.
