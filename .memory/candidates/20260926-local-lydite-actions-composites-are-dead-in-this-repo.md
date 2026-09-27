---
about: actions.md's "dogfood" paragraph is now false — this repo's own CI no longer calls .github/actions/lydite-comment or lydite-threads at all
saw:
  - agentic/references/actions.md
  - agentic/references/ci.md
  - .github/workflows/ci-orchestration.yml
  - .github/workflows/gt-lydite-clearance.yml
  - .github/AGENTS.md
  - docs/adr/0051-lydite-is-a-plain-consumer-of-its-own-relay.md
targets: null
verdict: null
---

Not a re-check of an existing note (nothing in the store anchors this); a fresh finding hit
while answering an architecture question.

`agentic/references/actions.md` (lines 22-27) still says: "What is here is the dogfood, and the
two are deliberately the same shape. `.github/workflows/lydite-pr.yml` runs the same concerns
through the local composites in `.github/actions/`. ... When the shape here changes, that
repository is where the change has to land as well."

That file does not exist:

```
$ find . -iname "*lydite-pr*" -o -iname "*lydite-baseline*"
(no output)
```

`.github/AGENTS.md` and `agentic/references/ci.md` already say the current truth: ADR 0051
("lydite is a plain consumer of `lydite/actions`, and a clearance may resolve a pending
referral") rewrote `lydite-pr.yml`/`lydite-clearance.yml` into calls to
`pedromvgomes/gt/.github/workflows/reusable-lydite.yml@v2` (`ci-orchestration.yml:181`) and
`reusable-lydite-clearance.yml@v2` (`gt-lydite-clearance.yml`), which in turn call
`lydite/actions`' own reusable workflows and composites — none of which live in this
repository. Confirmed no workflow file here references the local composites any more:

```
$ grep -rln "actions/lydite-comment\|actions/lydite-threads\|actions/lydite-reports\|actions/lydite-binary" .github/workflows/
(no output)
```

`.github/actions/{lydite-comment,lydite-threads,lydite-reports,lydite-binary}` still exist on
disk and are hand-maintained (per `.github/AGENTS.md`: "gt creates each file the first time a
stage is declared and never touches it again... this repository owns their content the same way
it owns... everything under `actions/`"), but they are currently unreachable from any trigger in
this repository — they are the shape a *consumer* gets via the external `lydite/actions` repo,
mirrored here as a design reference / for `lydite/actions` PRs to be checked against, not as
live dogfood. `actions.md`'s framing ("the two are deliberately the same shape... when the shape
here changes, that repository is where the change has to land") reads as still describing a live
mirroring relationship that ADR 0051 ended. `ci.md` already carries the corrected framing in its
own CI section ("The rest of this section describes what `lydite-pr.yml` and `lydite-baseline.yml`
did — neither exists in this repository any more").

Net: anyone editing `.github/actions/lydite-comment` or `lydite-threads` today, following
`actions.md`'s instruction that "the two are deliberately the same shape" and this repo's own
`lydite-pr.yml` proves it out, will find no workflow here exercises the edit at all — the
mirrored copies would need `lydite/actions` PR review or a new local trigger to ever run again.
