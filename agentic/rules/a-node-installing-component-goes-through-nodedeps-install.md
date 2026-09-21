# A component that installs node dependencies goes through `nodedeps.Install`, never a package manager directly

A package manager asked to run a script inside an uninstalled workspace installs that workspace
itself, unasked and uncoordinated, on its own schedule — the same `node_modules` tree
`nodedeps.Install` produces for every other component resolving that root. Two writers of one
tree is a lockfile-rename race whose loser fails inside the package manager rather than in
anything lydite reports (`ERR_PNPM_LOCKFILE_RENAME_FILE` on a cold clone). Whether a component
runs through a derived runner (`vitest`, `jest`) or a raw `command:` is irrelevant to this: the
question is only whether it resolves a workspace root under `internal/nodedeps`, and the answer
must route through the single coalesced `nodedeps.Install` call for that root, never spawn
`npm`/`pnpm`/`yarn` on its own.

## Applies to

`source/cli/cmd/lydite/test.go`'s `prepare`, `prepareCommand` and `installsNodeDeps`, and any
future code path that decides whether a component's dependencies are lydite's to install.

## Example

```go
// ✗ a command component's suite runs pnpm directly against an uninstalled root
exec.Command("pnpm", "run", "build")

// ✓ the component's install is coalesced with every other component
// resolving the same root, before its command ever runs
nodedeps.Install(ctx, dir, root, cfg.TypeScript.Install, env.Check, log.out)
```

Reasoning: [`agentic/references/components.md`](../references/components.md) and
[`agentic/references/services-and-scheduling.md`](../references/services-and-scheduling.md).
