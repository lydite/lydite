---
name: a-typescript-unit-can-carry-two-toolchain-requirements
kind: gotcha
description: toolchain.Requirements can return two requirements for one TypeScript unit (Node, then its pinned package manager), and Ensure merges them through alongside into a new Env rather than mutating the shared runtime.
anchors:
  - path: source/cli/internal/toolchain/require.go
    blob: 3cced96dc2fc
  - path: source/cli/internal/toolchain/toolchain.go
    blob: 30dbfa828d12
confidence: verified
---

`toolchain.Requirements` no longer strictly returns one `Requirement` per `Unit`. A TypeScript unit whose workspace pins pnpm or yarn in `packageManager` gets a second `Requirement` right after its Node one, both carrying the same `Unit`. Since `Lang` is `TypeScript` for both, they are told apart by `Requirement.Manager` (`""` for the runtime, `"pnpm"`/`"yarn"` otherwise); `probes`, `provision()` and the `resolveOne` sharing key all branch on `Manager` before the `Lang` switch (`toolchain.go:289`, `:577`, `:639`; `require.go:77`).

`Ensure`'s per-unit loop used to overwrite `envs[req.Unit.Name]`, safe with one requirement each. With two, the manager is merged into the runtime via `alongside(runtime, manager *Env)` (`toolchain.go:316`), which builds a *new* `Env`: the runtime's `*Env` is shared by every other unit resolved to the same Node, so appending one unit's manager to it would leak that manager into unrelated components. A future third per-unit requirement needs the same merge, not a third `envs[...] = env` overwrite.
