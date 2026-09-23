---
about: source/cli/internal/toolchain/require.go, source/cli/internal/toolchain/toolchain.go
saw: source/cli/internal/toolchain/require.go:Requirements,managerRequirement; toolchain.go:Ensure,alongside
---

`toolchain.Requirements` no longer strictly returns one `Requirement` per `Unit`. A TypeScript
unit whose workspace pins pnpm or yarn in `packageManager` gets a second `Requirement`
immediately after its Node one, both carrying the same `Unit` — the Node requirement dispatches
on `Requirement.Lang` (`runner.TypeScript`) alone, and the package-manager one is told apart by a
new `Requirement.Manager` field (`""` for the runtime, `"pnpm"`/`"yarn"` otherwise), since `Lang`
is `TypeScript` for both and can't disambiguate. `probes`/`provision()`/the `resolveOne` sharing
key all branch on `Manager` before falling into the `Lang` switch.

`toolchain.Ensure`'s per-unit loop used to overwrite `envs[req.Unit.Name]` — safe when each unit
had exactly one requirement. With two, the second (the package manager) is merged into the first
(the runtime) via `alongside(runtime, manager *Env)`, which builds a *new* `Env` rather than
mutating the runtime's own — the runtime's `*Env` is shared by every other unit resolved to the
same Node, so appending one unit's package manager to it would leak that manager into unrelated
components. Any future third per-unit requirement (a fourth toolchain layer) would need the same
merge treatment, not a third `envs[...] = env` overwrite.
