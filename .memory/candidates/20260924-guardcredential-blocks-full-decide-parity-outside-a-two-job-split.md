---
about: reviewdecision.CompareSurfaces's guardCredential refuses an untrusted-build component's comparison in any credentialed process; the clearance Fingerprint stage always guards, so a full-parity clearance fingerprint needs a --surfaces document from a separate credential-free job — and the queue path sidesteps comparison entirely
saw:
  - source/cli/internal/reviewdecision/surface.go
  - source/cli/internal/reviewdecision/decide.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/internal/stages/scm/scm.go
  - source/cli/cmd/lydite/review.go
  - source/cli/cmd/lydite/review_compare.go
  - source/cli/cmd/lydite/clearance.go
  - source/cli/cmd/lydite/mergequeue.go
  - agentic/rules/guard-an-in-process-comparison-by-whether-a-credential-exists-not-by-a-flag.md
---

Re-checked on `refactor/flow-architecture-clearance-pilot`: `computeAPISurfaces` became
`reviewdecision.Surfaces`/`CompareSurfaces` (`internal/reviewdecision/surface.go`), and
`clearedDecision` moved into the clearance flow's Fingerprint stage. The claim holds; pointers
moved, and the credential requirement is now enforced by a stage rather than inline.

**The guard.** `CompareSurfaces(ctx, dir, base, guardCredential, tc, progress)` reports every
opted-in component whose comparison runs the tree's own code (`untrustedBuild`: Rust's
`build.rs`/proc-macros, TypeScript's npm lifecycle scripts; Go's is safe) as `Uncomputable` when
`guardCredential` is true, instead of comparing. See
`agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`.

**Who guards when.**
- `review` passes `doPublish` (`cmd/lydite/review.go`): a render-only `review` holds no
  credential and may compare in-process.
- `review compare --write-surfaces` passes `false` — it never publishes
  (`cmd/lydite/review_compare.go`).
- The clearance Fingerprint stage passes `true` **unconditionally**
  (`clearedDecision` in `internal/stages/clearance/fingerprint.go`), not conditioned on
  `--status-out`. Every clearance run holds a credential regardless of route: the flow's
  `InitSCM` stage refuses to run without one (`scmstages.ErrNoCredential`,
  `internal/stages/scm/scm.go`), because reading the comment, head, permission and referral and
  posting the reply all need a token. So there is no clearance invocation where the comparison
  is safe beside it (rule: `guard-an-in-process-comparison-by-whether-a-credential-exists-not-by-a-flag.md`).

**The only route to full parity** for an untrusted-build component is `clearance --surfaces
<file>` (flag in `cmd/lydite/clearance.go`, threaded as the flow's `SurfacesDocument` input),
read through `reviewdecision.Surfaces` → `reconcileSurfaces` exactly as `review --surfaces` does.
The workflow-side split that produces that document (a credential-free computing job, a
credentialed posting job) lives in `lydite/actions`' `lydite-clearance.yml`, not in this repo.

**The queue path sidesteps the question.** `clearance queue`'s `queueDecision`
(`cmd/lydite/mergequeue.go`) is now a one-line wrapper over `reviewdecision.DecideFromDiff`
(`internal/reviewdecision/decide.go`), which passes the zero `referral.Evidence{}` and never
calls `Surfaces`/`measureDependencies`. A clearance given for an API-surface or dependency
disqualification therefore fingerprints differently at queue time and goes back to a person —
the accepted gap in ADR 0057 and `agentic/references/referral-and-clearance.md`'s "What still
does not carry forward".
