---
name: local-lydite-actions-composites-are-dead-in-this-repo
kind: gotcha
description: No workflow in this repository calls the local lydite-comment, lydite-threads, lydite-reports or lydite-binary composites any more, yet agentic/references/actions.md still describes them as live dogfood.
anchors:
  - path: agentic/references/actions.md
    blob: e91a582ed784
  - path: .github/workflows/ci-orchestration.yml
    blob: 5fcc8f188aa7
  - path: .github/workflows/gt-lydite-clearance.yml
    blob: fc210a62bbdc
confidence: verified
---

`agentic/references/actions.md` (lines ~22-27) still says "What is here is the dogfood ... `.github/workflows/lydite-pr.yml` runs the same concerns through the local composites in `.github/actions/`". That workflow no longer exists: `.github/workflows/` holds `ci-*.yml`, `gt-*.yml`, release, dependabot and workers files only. ADR 0051 rewrote the lydite jobs into calls to gt's reusable workflows (`ci-orchestration.yml` and `gt-lydite-clearance.yml`), which in turn call `lydite/actions`; `.github/AGENTS.md` and `agentic/references/ci.md` carry the corrected story.

`grep -rn "lydite-comment\|lydite-threads" .github/workflows` finds nothing. The composites `.github/actions/{lydite-comment,lydite-threads,lydite-reports,lydite-binary}` still exist on disk and are hand-maintained (gt creates each file once and never touches it again), but no trigger in this repository reaches them. Anyone editing `lydite-comment` or `lydite-threads` and following `actions.md`'s "the two are deliberately the same shape" will find nothing here exercising the edit; the change needs `lydite/actions` review or a new local trigger to run at all. See also [[relay-audience-mismatch-degrades-silently]], which is about these composites' own code.
