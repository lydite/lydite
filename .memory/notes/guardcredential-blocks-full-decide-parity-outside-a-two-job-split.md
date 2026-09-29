---
name: guardcredential-blocks-full-decide-parity-outside-a-two-job-split
kind: gotcha
description: CompareSurfaces refuses an untrusted-build component's comparison in a credentialed process, and clearance always guards, so full parity needs a --surfaces document from a credential-free job.
anchors:
  - path: source/cli/internal/reviewdecision/surface.go
    blob: 4f3bc50e32be
  - path: source/cli/internal/stages/clearance/fingerprint.go
    blob: 516836b3bf3a
  - path: source/cli/internal/flows/review/review.go
    blob: 172c39425310
  - path: source/cli/cmd/lydite/clearance.go
    blob: 65506735cacb
confidence: verified
---

**The guard.** `CompareSurfaces(ctx, dir, base, guardCredential, tc, progress)` (`internal/reviewdecision/surface.go:145`) reports every opted-in component whose comparison runs the tree's own code (`untrustedBuild`: Rust's `build.rs`/proc-macros, TypeScript's npm lifecycle scripts; Go's is safe) as `Uncomputable` when `guardCredential` is true (`:185`), instead of comparing. See `agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md`.

**Who guards when.** `review`'s flow binds the guard to its `Publish` input (`internal/flows/review/review.go`), set from `--publish`; the comparison runs in the `surfaces` stage before any credential stage, so a render-only `review --publish --status-out` is still guarded and only a plain non-publishing `review` compares in-process. `review compare --write-surfaces` never publishes (`flow.Literal(false)`). The clearance Fingerprint stage passes `true` **unconditionally** (`clearedDecision` in `internal/stages/clearance/fingerprint.go`), because every clearance run holds a credential: the flow's `InitSCM` stage refuses to run without one (`scmstages.ErrNoCredential`, `internal/stages/scm/scm.go`). So no clearance invocation has a safe in-process comparison (rule: `agentic/rules/guard-an-in-process-comparison-by-whether-a-credential-exists-not-by-a-flag.md`).

**The only route to full parity** for an untrusted-build component is `clearance --surfaces <file>` (`cmd/lydite/clearance.go`), read through `reviewdecision.Surfaces` (`surface.go:115`) exactly as `review --surfaces` does; the credential-free computing job lives in `lydite/actions`, not this repo.

**The queue path sidesteps the question.** `queueDecision` (`cmd/lydite/mergequeue.go`) wraps `reviewdecision.DecideFromDiff`, which passes a zero `referral.Evidence{}` and never calls `Surfaces`. A clearance given for an API-surface or dependency disqualification therefore fingerprints differently at queue time and goes back to a person — the accepted gap in ADR 0057 and `agentic/references/referral-and-clearance.md`'s "What still does not carry forward".
