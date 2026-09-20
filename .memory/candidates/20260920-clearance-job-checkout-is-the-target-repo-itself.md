---
about: the clearance job's checkout is the same repository the pull request belongs to, at its default branch — not a separate copy of lydite's own source
saw:
  - .github/workflows/lydite-clearance.yml
  - docs/adr/0015-clearance-binds-to-a-commit.md
---

`.github/workflows/lydite-clearance.yml` runs `actions/checkout` with no `repository:`
override, which checks out whatever repository the workflow file lives in — the same
repository the triggering pull request belongs to, at its default branch. ADR 0015's
"the default branch's own copy of lydite" language is easy to misread as "a separate
checkout of lydite's own source distinct from the target repository" (as if the clearance
job somehow fetched a second, unrelated repo); it does not — the checkout IS the target
repository, just its default branch's tree rather than the pull request's.

This matters concretely: `.lydite/exemptions.yml` at the scan root is already present in
the working directory when `lydite clearance` runs, and can be read with a plain
`os.ReadFile` — no git-show-at-a-ref trick and no new forge API call is needed to read it,
unlike `cmd/lydite/review.go`'s `loadExemptionsAt` (which needs `git show <base>:<path>`
because *that* job's checkout is the pull request's own branch with history, and reading
the working tree there would let a PR benefit from its own widening). What genuinely is
missing from this checkout is the pull request's *diff* — the changed-path list and any
line content — since only the default branch's tree is present, not the PR's commits.
