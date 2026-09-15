---
about: .github/workflows/workers-secrets-sync.yml
kind: gotcha
---

# An org secret scoped to `visibility: "private"` reads as empty on a public repo

`workers-secrets-sync.yml` reads `secrets.LYDITE_APP_PRIVATE_KEY` as an org-level secret
(`.github/workflows/workers-secrets-sync.yml:75`). GitHub's org-secret `visibility` field takes
`all`, `private`, or `selected` — `private` means "automatically visible to every *private*
repository in the org," not "restricted to a selected set." `lydite/lydite` is public
(`gh api repos/lydite/lydite --jq '.private'` → `false`), so a secret stored with
`visibility: "private"` is invisible to any workflow run here: the env var reads as an empty
string, which is indistinguishable at the shell level from the secret never having been set at
all.

**Saw:** `gh secret set LYDITE_APP_PRIVATE_KEY --org lydite ...` without an explicit
`--visibility` flag defaulted to `private`. `workers-secrets-sync.yml`'s own preflight check
(`.github/workflows/workers-secrets-sync.yml:91-102`) then failed with
`LYDITE_APP_PRIVATE_KEY is not a PEM private key` — not because the key was malformed, but
because the value it received was empty. Comparing against `CLOUDFLARE_API_TOKEN`
(`visibility: "all"`), which the same workflow reads successfully, confirmed the scope was the
cause: `gh api orgs/lydite/actions/secrets/<name> --jq '{visibility,selected_repositories_url}'`
on each secret showed the mismatch directly.

Any org secret this repository's workflows must read has to be `visibility: "all"` (or
`"selected"` with `lydite/lydite` explicitly listed) — `"private"` silently excludes it, and the
failure surfaces as a content-format error rather than an access error.
