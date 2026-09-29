---
name: licence-base-read-bounds-its-walk-by-the-base-worktree
kind: invariant
description: The merge-base TypeScript licence read passes the scan root inside the throwaway base worktree, not the branch's root, so the workspace-root walk cannot climb out of the tree being measured.
anchors:
  - path: source/cli/internal/stages/scan/licences.go
    blob: b63a436db7d9
  - path: source/cli/internal/typescript/licence.go
    blob: c1e23b1d206f
confidence: verified
---

`typescript.LicenceSet(ctx, dir, scanRoot, policy)` (`internal/typescript/licence.go`) resolves the lockfile through `nodedeps.WorkspaceRoot(dir, scanRoot)`. In `internal/stages/scan/licences.go` the current tree's read in `typescriptLicence` passes `tree.root` (line 221); the merge-base read in `typescriptLicenceBase` passes `tree.dir` (line 302) — the scan root *inside* the throwaway base worktree `licenceBaseTree.open` checked out (worktree root joined with `gitdiff.Prefix`). Passing the branch's own root there would let the ancestor walk climb out of the worktree being measured and read a lockfile from the wrong tree, so both sides of the delta would no longer measure the same thing.

The read is scoped per manager: npm walks the lockfile's importer entry (`memberClosure`) with no install, which also works on the base worktree that has no `node_modules`; yarn and pnpm are read out of an installed `node_modules`, so a nested member of either is an error, recorded as `LicenceVerdict.Err` and rendered `unmeasured` by `recordLicence` (`cmd/lydite/scan.go`).
