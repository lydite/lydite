---
about: the merge-base licence read must bound its workspace-root walk by the base worktree's own root, not the branch's
saw:
  - source/cli/cmd/lydite/scan.go
  - source/cli/internal/typescript/licence.go
---

`typescript.LicenceSet(ctx, dir, scanRoot, policy)` resolves the lockfile through
`nodedeps.WorkspaceRoot(dir, scanRoot)`. On the current tree `scanRoot` is `tree.root`; on the
merge-base side `typescriptLicenceBase` passes `tree.dir`, the scan root *inside* the throwaway
base worktree. Passing the branch's own root there would let the ancestor walk climb out of the
worktree being measured and read a lockfile from the wrong tree, so both sides of the delta would
no longer be measuring the same thing.

The read is scoped per manager: npm walks the lockfile's importer entry (`memberClosure`) with no
install, which also works on the base worktree that has no `node_modules`; yarn and pnpm cannot be
scoped without an install, so a nested member of either is an error and renders `unmeasured`.
