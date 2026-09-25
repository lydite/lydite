---
name: guardcredential-blocks-full-decide-parity-outside-a-two-job-split
kind: rationale
about: source/cli/cmd/lydite/review_apisurface.go
description: computeAPISurfaces's guardCredential refuses an opted-in component's comparison in-process whenever the calling job holds a writing credential; giving a credentialed command review's own full-parity Decide needed a --surfaces flag reading a document a separate credential-free job already made, not a code change alone.
anchors:
  - path: source/cli/cmd/lydite/review_apisurface.go
    blob: 583ef4dc9b3f8af996ba2fdc5ef6c44529d260af
  - path: source/cli/cmd/lydite/clearance.go
    blob: e7a94110329c8f991d1061563a84747b80217de6
  - path: source/cli/cmd/lydite/review_compare.go
    blob: 6a8c82344064f102a7731e943c1bc5d3645e4734
confidence: verified
---

`computeAPISurfaces`'s `guardCredential` parameter refuses to run any opted-in component's
API-surface comparison in-process whenever the calling job holds a writing credential — it
reports every such component as `Uncomputable` instead of actually comparing (see
`agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`). `review` avoids
this by splitting into two jobs: `review compare --write-surfaces` runs with no credential and
writes a comparison document; a credentialed `review --surfaces <doc>` reads it back rather than
re-running it.

`/lydite clear` (`cmd/lydite/clearance.go`) now has the same route, on
`feat/a-clearance-records-its-fingerprint` (ADR 0057): `clearance` gained a `--surfaces <file>`
flag mirroring `review`'s own. `clearedDecision`'s in-process fallback
(`computeAPISurfaces(ctx, cmd, opt.dir, baseSHA, true)`) is guarded *unconditionally* — not
conditioned on `--status-out` the way `review`'s own fallback is — because `runClearance`
requires `GITHUB_TOKEN` on every invocation of `clearance`, whether or not that invocation also
posts the status directly, so there is no invocation of the command in which the comparison is
safe to run beside it (see `agentic/rules/guard-an-in-process-comparison-by-whether-a-credential-
exists-not-by-a-flag.md`). `--surfaces` reconciles a document a separate credential-free job
already made (via `readSurfaces`/`reconcileSurfaces` — the same functions `review` uses) and is
the only route left to a full-parity fingerprint for a component whose comparison runs the
change's own code. The workflow-side split that actually produces that document — a computing
job with no writing credential, a posting job that reads it — lands in the separate
`lydite/actions` repository's `lydite-clearance.yml`, not in this repository's own diff.

`mergequeue.go`'s own `queueDecision` still sidesteps the whole question: it holds no writing
credential and passes the zero `referral.Evidence{}`, never calling `computeAPISurfaces`/
`measureDependencies` at all — so a clearance given for an API-surface or dependency
disqualification still does not carry forward to a queue entry, even after this branch, and goes
back to a person instead (documented as the accepted remaining gap in ADR 0057 and in
`agentic/references/referral-and-clearance.md`'s "What still does not carry forward" paragraph).
