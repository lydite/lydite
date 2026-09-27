---
about: the merge-base licence read must bound its workspace-root walk by the base worktree's own root, not the branch's
saw:
  - source/cli/internal/stages/scan/licences.go
  - source/cli/internal/typescript/licence.go
---

`typescript.LicenceSet(ctx, dir, scanRoot, policy)` (`internal/typescript/licence.go`) resolves the
lockfile through `nodedeps.WorkspaceRoot(dir, scanRoot)`. In `internal/stages/scan/licences.go`,
the current tree's read in `typescriptLicence` passes `tree.root` (the scan root); the merge-base
read in `typescriptLicenceBase` passes `tree.dir`, the scan root *inside* the throwaway base
worktree that `licenceBaseTree.open` checked out (worktree root joined with `gitdiff.Prefix`).
Passing the branch's own root there would let the ancestor walk climb out of the worktree being
measured and read a lockfile from the wrong tree, so both sides of the delta would no longer be
measuring the same thing — the comment inside `typescriptLicenceBase` states this.

The read is scoped per manager: npm walks the lockfile's importer entry (`memberClosure`) with no
install, which also works on the base worktree that has no `node_modules`; yarn and pnpm are read
out of an installed `node_modules`, so a nested member of either is an error, which
`typescriptLicence` records as `LicenceVerdict.Err` and `recordLicence` (`cmd/lydite/scan.go`)
renders `unmeasured`.
