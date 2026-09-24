---
about: source/cli/cmd/lydite/review_apisurface.go
saw: source/cli/cmd/lydite/review_apisurface.go (computeAPISurfaces), source/cli/cmd/lydite/clearance.go, source/cli/cmd/lydite/mergequeue.go
---

`computeAPISurfaces`'s `guardCredential` parameter refuses to run any opted-in component's
API-surface comparison in-process whenever the calling job holds a writing credential — it
reports every such component as `Uncomputable` instead of actually comparing (see
`agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`). `review` avoids this
only because it splits into two jobs (`referral`, no credential, runs the comparison;
`referral-publish`, holds the credential, only reads the document it wrote) — ADR 0051's own
stated *exception* for lydite's own repo, not the general mechanism for a consumer.

This blocks any future attempt to give a credentialed job (like `/lydite clear`'s comment
handler, which already holds `GITHUB_TOKEN` with `statuses: write`) full parity with `review`'s
own `referral.Decide` computation — doing so would need the same two-job split, i.e. editing
`.github/workflows/` and possibly `lydite/actions`' `lydite.yml`, not just a code change inside
`cmd/lydite/clearance.go`. This is exactly the blocker behind
[lydite/lydite#254](https://github.com/lydite/lydite/issues/254) (wiring a fingerprint into
`/lydite clear` so ADR 0053's merge-queue mechanism actually carries a clearance forward): the
obvious fix ("call `referral.Decide` there too, the way `mergequeue.go`'s `queueDecision`
already does") silently drops every API-surface disqualification the moment that job is given a
writing credential, unless the split is done too. `mergequeue.go`'s own `queueDecision` sidesteps
this entirely by holding no writing credential and passing the zero `referral.Evidence{}` —
never running `computeAPISurfaces`/`measureDependencies` at all, deliberately, documented in its
own doc comment.
