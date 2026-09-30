---
about: a raw command: component with its own package.json is provisioned Node and the pinned package manager through toolchain.Unit.NodeCommand, not through Unit.Lang
saw:
  - source/cli/internal/test/run/env.go
  - source/cli/internal/toolchain/require.go
  - source/cli/internal/test/run/prepare.go
  - source/cli/internal/stages/mutation/run.go
  - docs/adr/0079-a-raw-command-with-its-own-package-json-runs-under-the-provisioned-node.md
---
`run.ComponentUnits(root, components)` emits `Unit{Name, Dir, NodeCommand: true}` (Lang empty) for a component with a `command:`, no runner and a regular-file `package.json` in its own directory — the same criterion `installsNodeDeps` (prepare.go) joins `nodedeps.Install` on. `Unit.lang()` (require.go) maps a NodeCommand unit with no Lang to TypeScript, so `Requirements` gives it the Node requirement, the pinned manager and the `toolchain.node` override exactly as a TypeScript unit; `Unit.Lang` stays empty so a `lang: shell` command beside a package.json is never TypeScript to anything that decides by language (ADR 0056, qualified by ADR 0079).

Only `lydite test` builds these units (`testUnits` in cmd/lydite/test.go, `stages/test`'s toolchains and base-tree baseline, the latter rooted at the base worktree). `lydite review` keeps `componentUnits`, which drops command components, and `scanUnits` (stages/scan/toolchains.go) is untouched. A download failure still ends in `pnpm: not on PATH` from `nodedeps.managerOnPath`, under a provisioning warning.

Mutation never reaches this: `prepareTarget` stops a command component at `KindRawCommand` (stages/mutation/run.go), so `prepareCommand`'s missing `declarationRoot` fallback is unreachable.
