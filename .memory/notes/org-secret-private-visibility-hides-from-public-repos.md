---
name: org-secret-private-visibility-hides-from-public-repos
kind: gotcha
description: A GitHub org secret's visibility "private" means "visible to every private repo in the org," not "restricted to a selected set" — a workflow in a public repo (lydite/lydite) reads it as an empty string, which fails as a content-format error rather than an access error.
anchors:
  - path: .github/workflows/workers-secrets-sync.yml
    blob: c4c6bf8975fb
confidence: suspect
---

`workers-secrets-sync.yml` reads `secrets.LYDITE_APP_PRIVATE_KEY` as an org-level secret. GitHub's
org-secret `visibility` field takes `all`, `private`, or `selected` — `private` means "automatically
visible to every *private* repository," not a restricted allowlist. `lydite/lydite` is public, so a
secret stored with `visibility: "private"` is invisible to any workflow run here: the env var reads
as empty, indistinguishable at the shell level from never having been set.

`gh secret set ... --org lydite` without an explicit `--visibility` flag defaults to `private`. That
produced a failure reading "is not a PEM private key" — not a malformed key, but an empty value.
Comparing against `CLOUDFLARE_API_TOKEN` (`visibility: "all"`), read successfully by the same
workflow, confirmed the cause; `gh api orgs/lydite/actions/secrets/<name>` shows the mismatch
directly. Any org secret this repository's workflows must read has to be `visibility: "all"` (or
`"selected"` with `lydite/lydite` explicitly listed).

(Marked suspect: this is a fact about the org's current GitHub secret configuration, not something
checkable from the repository's own files — it can drift independently of any commit.)
</content>
