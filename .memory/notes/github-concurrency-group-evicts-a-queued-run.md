---
name: github-concurrency-group-evicts-a-queued-run
kind: gotcha
description: GitHub keeps one pending run per concurrency group even with cancel-in-progress false, so a third trigger silently evicts a queued second run before it starts.
anchors:
  - path: agentic/rules/scope-a-concurrency-group-to-what-must-not-evict-it.md
    blob: abd183289e31
confidence: suspect
---

`cancel-in-progress: false` on a `concurrency:` block only stops GitHub cancelling the run *currently executing*; it says nothing about the queue behind it. GitHub keeps exactly one pending run per group regardless, so with one run in progress and one queued, a third arrival evicts the queued one before it starts — silently, with no cancellation event and no log naming what was dropped. This is GitHub platform behaviour the explorer observed and this session could not re-check from the repository, hence `suspect`.

It hit the (since deleted) `lydite-baseline.yml` concretely: three merges landing close together (`0566be0` in progress, `b321928` queued, `ee89b8c` arriving 17 seconds later) evicted `b321928`'s run outright, and because its `record` job was `if: ${{ !cancelled() }}` nothing partial was written; the loss was invisible until the ndjson history was read closely. The fix is not to serialize harder but to scope the group so each key needing a guaranteed run (there `${{ github.sha }}`) gets its own queue slot, and to make sure what the workflow writes to can absorb the overlap — see [[gitstate-write-retry-cap-is-a-tolerance-not-a-correctness-bound]] and the rule this became, `agentic/rules/scope-a-concurrency-group-to-what-must-not-evict-it.md`.
