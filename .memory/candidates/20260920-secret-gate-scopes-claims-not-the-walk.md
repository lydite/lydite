---
about: gitleaks dir cannot scope its walk, so the secret gate filters claims against gitdiff.Tracked
saw:
  - source/cli/internal/secrets/findings.go
  - docs/adr/0035-secret-scanning-is-root-scoped-over-the-working-tree.md
---

`gitleaks dir` (v8.30.1) takes one path and has no flag that excludes paths or reads
`.gitignore`, so a warm `target/` is walked and its compiled-in test vectors reported.
`tracked` in `source/cli/internal/secrets/findings.go` scopes the *claims* instead, against
`gitdiff.Tracked`, and the filter is not a suppression (`internal/referral` treats
`gitleaks:allow` as one, and a suppression refers). Three shapes make the filter fail open
if forgotten: an empty answer from git (`errNoScope`), a submodule gitlink or embedded repo
(git's answer stops at its boundary, `nestedRepositories`), and path shape (gitleaks reports
relative under `.`, absolute otherwise, and darwin resolves `/var` to `/private/var`).
ADR 0035's amendment records the trade: a credential in a gitignored file is not reported.
