---
about: lydite scan runs no package-manager install for any TypeScript component, unlike lydite test — the TypeScript licence gate reads npm's lockfile and only opportunistically reads a yarn/pnpm node_modules someone else installed
saw:
  - source/cli/internal/stages/scan/checks.go
  - source/cli/internal/stages/scan/licences.go
  - source/cli/internal/typescript/licence.go
  - source/cli/internal/nodedeps/nodedeps.go
  - source/cli/internal/runner/runner.go
  - source/cli/cmd/lydite/test.go
  - docs/adr/0042-a-typescript-components-licences-are-read-from-its-lockfile.md
---

`nodedeps.Install` (the frozen `npm ci` / `yarn --immutable` / `pnpm --frozen-lockfile`) is
called only on the `lydite test`/mutation side: `installNodeDeps` in `internal/runner/runner.go`
and `prepareCommand` in `cmd/lydite/test.go`. Nothing under `internal/stages/scan/` or
`internal/flows/scan/` imports `internal/nodedeps` at all. The scan's TypeScript check
(`checksFor` in `internal/stages/scan/checks.go` → `typescript.Check`) runs Biome, which reads
source files directly and needs no `node_modules` — so a fresh `lydite scan` checkout never has an
installed tree.

This mattered for the TypeScript licence gate (ADR 0042): a design reading `node_modules` as the
licence source would need the licence code to trigger an install, once for the current tree and
again inside the merge-base's throwaway worktree (`licenceBaseTree` in
`internal/stages/scan/licences.go`), doubling the install per component per scan. The shipped
design avoids that: `typescriptLicence`'s doc says "No install is run on either side, for any
package manager." npm's licence source is `package-lock.json`, read directly; yarn/pnpm read a
`node_modules` only if some earlier step (typically `lydite test` in the same CI job) already
installed it, and the verdict is unmeasured otherwise (`typescript.LicenceSet` in
`internal/typescript/licence.go` resolves the manager through `nodedeps.WorkspaceRoot`/`Manager`,
never `Install`).
