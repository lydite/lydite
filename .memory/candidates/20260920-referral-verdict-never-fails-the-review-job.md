---
about: lydite/actions/review's step only fails the GHA job on exit >2, so a "refer" (exit 2) or isolation-gate failure (exit 1) always reports job success; the real gate is the lydite/referral commit status, and no consumer can require that status yet
saw:
  - /Users/pedrogomes/work/repositories-personal/lydite-actions/main/review/action.yml
  - agentic/references/referral-and-clearance.md
  - .github/workflows/lydite-pr.yml
---

Traced how `lydite review`'s exit code becomes CI outcome, for a question about wiring gt's
`ci-gate` to `lydite/actions/review@v1`.

`review/action.yml:76-80` (lydite-actions repo): `verdict=0; lydite "${args[@]}" || verdict=$?;
if [ "${verdict}" -gt 2 ]; then exit "${verdict}"; fi`. So exit 0 (pass), 1 (the
exemption-isolation gate — the one thing `referral-and-clearance.md` calls a real failure) and
2 (refer) **all leave the composite step at exit 0**. Only exit >2 (lydite itself erroring)
fails the job. A `needs:` hard dependency on this job in gt's `ci-gate` therefore cannot gate
on the verdict at all — the job "succeeds" on both pass and refer.

`agentic/references/referral-and-clearance.md` (Clearance section) confirms the intended gate
is elsewhere: `review --publish` writes the **`lydite/referral` commit status** (`pending` for
a referral, never `failure`, per `stateFor`), and a `/lydite clear` comment flips that status
directly — clearance never re-runs `review`, matching "the status is read before it is
written."

Critically, the same doc says: "The status blocks no merge yet. Making it a required check is
one field on a ruleset `gt` renders and hardcodes to `ci-gate` alone, so it cannot be set from
this repository — see #34." `.github/workflows/lydite-pr.yml:38-45` (this repo, dogfooding the
same integration a consumer like gt would do) confirms this is not hypothetical: lydite's own
`review` job runs **outside** the gt pipeline entirely, because "gt's CI accepts exactly the
stages preflight, build, test and end2end, so there is no stage a publish could be" and
"`statuses: write` is not something a called stage can be granted" — so `lydite/referral`
today blocks nothing in `ci-gate`, even in lydite's own repo, pending #34.
