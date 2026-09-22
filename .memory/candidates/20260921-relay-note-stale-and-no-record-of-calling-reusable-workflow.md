---
about: relay-audience-mismatch-degrades-silently is stale but its ADR 0022/0037 context is unchanged; no ADR records a workflow_call-caller attempt for lydite-pr.yml, and gt#71 appears nowhere in the repo
saw:
  - docs/adr/0022-a-vendor-operated-app-and-an-oidc-relay.md
  - agentic/references/actions.md
  - agentic/references/surface.md
  - .github/workflows/lydite-pr.yml
  - source/cli/internal/clearance/decide.go
targets: relay-audience-mismatch-degrades-silently
verdict: unchecked
---

Not re-verified line-by-line (5 anchors drifted); the audience/fallback story is subsumed by ADR 0037 (a deterministic misconfiguration now fails the step), see candidate 20260915-relay-audience-mismatch-now-fails-loudly.

Findings from a 4-question sweep:
- job_workflow_ref rationale: ADR 0022 "Amendment (2026-09-21)" lines ~289-332. OIDC has no job identity; lydite-pr.yml has one id-token job, so environment/audience/ref cannot separate the isolated job; local same-repo reusable workflow refs are PR-controlled, so only external lydite/actions refs are allowlisted (REFERRAL_WORKFLOW_REFS / CLEARANCE_WORKFLOW_REFS, exact strings, no patterns; pr-relay/src/index.ts:22,376,445).
- /status falls back only on 409: ADR 0022:322-325, surface.md:204 -- a 403 is deterministic; 5xx/000 fail the step so a relay-authored pending is never followed by a bot-authored success (mixed record). 409 is index.ts:199.
- lydite-pr.yml mirrors, not calls: actions.md:22-27 and workflow header lines 3-6 -- run from source so the binary is the PR's own build; "nothing enforces" the mirror. grep of ADRs/references finds NO record of a workflow_call attempt being tried/rejected: that half of the question is unanswered in-repo.
- Clearance: decide.go:11,23 Context=lydite/referral, ClearanceContext=lydite/clearance; Decide reads only the referral status (Request.Status, decide.go:60,187-200). gt#71 / integration_id: zero grep hits in agentic/, docs/, .github/; only #34 (ruleset hardcoded to ci-gate) is cited.
