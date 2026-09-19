---
about: cmd/lydite/scan.go runs no package-manager install for any TypeScript component, unlike lydite test
saw:
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/nodedeps/nodedeps.go
  - source/cli/internal/runner/runner.go
---

`nodedeps.Install` (the frozen `npm ci` / `yarn --immutable` / `pnpm --frozen-lockfile`) is
called only from `internal/runner.go`, on the `lydite test`/coverage path. `cmd/lydite/scan.go`'s
TypeScript branch runs Biome, which reads source files directly and needs no `node_modules` at
all — so a fresh `lydite scan` checkout never has an installed tree.

This mattered for the TypeScript licence gate (ADR 0042): a design reading `node_modules` as the
licence source would need the licence-recording code itself to trigger an install, once for the
current tree and again inside the merge-base's throwaway worktree (`licenceBaseTree`), doubling
`npm ci` per component per scan. The design that shipped avoids this entirely — npm's licence
source is `package-lock.json`, read directly with no install ever; yarn/pnpm read a `node_modules`
only if some earlier step (typically `lydite test` in the same CI job) already installed it,
opportunistically, and report `unmeasured` otherwise. `scan` itself still triggers no install, for
any manager.
