---
name: github-concurrency-group-evicts-a-queued-run-not-just-cancels-in-progress
kind: gotcha
about: .github/workflows/lydite-baseline.yml
description: GitHub keeps only one pending run per concurrency group even with cancel-in-progress false, so a third trigger silently evicts a second trigger's still-queued run before it ever starts — a workflow with a shared group loses runs this way regardless of cancel-in-progress.
anchors:
  - path: .github/workflows/lydite-baseline.yml
    blob: fcb9c74dcc88033940c47bf6d024536620419ca8
confidence: verified
---

`cancel-in-progress: false` on a `concurrency:` block only stops GitHub from cancelling the run
*currently executing* — it says nothing about the queue behind it. GitHub keeps exactly one
pending run per group regardless of that flag, so with one run in progress and one queued, a
third arrival evicts the queued one before it starts, silently: no cancellation event, no log
entry naming what was dropped.

`lydite-baseline.yml` hit this concretely — three merges landing close together (`0566be0` in
progress, `b321928` queued, `ee89b8c` arriving 17 seconds later) evicted `b321928`'s run outright,
and because its `record` job is `if: ${{ !cancelled() }}`, nothing partial was written either;
the loss was invisible until the ndjson history was read closely.

The fix is not to serialize harder — it's to scope the group so each key that needs a guaranteed
run (here, `${{ github.sha }}`) gets its own queue slot, and to make sure the thing the workflow
writes to can absorb the resulting overlap (see
[[gitstate-write-retry-cap-is-a-tolerance-not-a-correctness-bound]]). See also
`agentic/rules/scope-a-concurrency-group-to-what-must-not-evict-it.md` for the general rule this
became.
