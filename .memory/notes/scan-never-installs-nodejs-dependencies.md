---
name: scan-never-installs-nodejs-dependencies
kind: rationale
description: lydite scan runs no package-manager install for any TypeScript component, so the TypeScript licence gate reads npm's lockfile and only opportunistically reads a yarn or pnpm node_modules that someone else installed.
anchors:
  - path: source/cli/internal/stages/scan/licences.go
    blob: b63a436db7d9
  - path: source/cli/internal/typescript/licence.go
    blob: c1e23b1d206f
  - path: source/cli/internal/nodedeps/nodedeps.go
    blob: b3c37269f66d
  - path: source/cli/internal/runner/runner.go
    blob: 3a3d11a81409
confidence: verified
---

`nodedeps.Install` (frozen `npm ci` / `yarn --immutable` / `pnpm --frozen-lockfile`) is called only on the `lydite test`/mutation side: `installNodeDeps` in `internal/runner/runner.go:895` and `prepareCommand` in `internal/test/run/prepare.go`. Nothing under `internal/stages/scan/` or `internal/flows/scan/` imports `internal/nodedeps` (`grep -rn internal/nodedeps` there is empty). The scan's TypeScript check runs Biome, which reads source directly, so a fresh `lydite scan` checkout never has an installed tree.

That is why the TypeScript licence gate (ADR 0042) does not read `node_modules` by default: doing so would need an install on the current tree and again inside the merge-base's throwaway worktree (`licenceBaseTree` in `internal/stages/scan/licences.go`), doubling installs per component per scan. `typescriptLicence` installs nothing for any manager: npm's source is `package-lock.json`, read directly; yarn/pnpm read a `node_modules` only if an earlier step (typically `lydite test` in the same CI job) installed one, and the verdict is `unmeasured` otherwise. `typescript.LicenceSet` (`internal/typescript/licence.go`) resolves the manager through `nodedeps.WorkspaceRoot`/`Manager`, never `Install`.
