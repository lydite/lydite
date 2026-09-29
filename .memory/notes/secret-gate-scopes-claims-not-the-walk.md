---
name: secret-gate-scopes-claims-not-the-walk
kind: gotcha
description: gitleaks dir cannot scope its walk, so the secret gate filters claims against git's tracked files, and three shapes make that filter fail open if forgotten.
anchors:
  - path: source/cli/internal/secrets/findings.go
    blob: 39e82221f384
confidence: verified
---

`gitleaks dir` (v8.30.1) takes one path and has no flag that excludes paths or reads `.gitignore`, so a warm `target/` is walked and its compiled-in test vectors reported. `tracked` in `internal/secrets/findings.go` scopes the *claims* instead, against `gitdiff.Tracked`. The filter is not a suppression (`internal/referral` treats `gitleaks:allow` as one, and a suppression refers).

Three shapes make the filter fail open if forgotten: an empty answer from git (`errNoScope`, `findings.go:107`), a submodule gitlink or embedded repo where git's answer stops at its boundary (`nestedRepositories`, `:126`), and path shape — gitleaks reports relative under `.`, absolute otherwise, and darwin resolves `/var` to `/private/var`. ADR 0035's amendment records the trade: a credential in a gitignored file is not reported.
