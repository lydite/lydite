---
name: queue-ref-trailing-sha-is-the-base-not-the-pr-head
kind: gotcha
description: The trailing sha in a gh-readonly-queue ref is the base tip the entry was replayed onto, not the pull request head, contradicting one sentence of ADR 0053, so the head is resolved live.
anchors:
  - path: source/cli/internal/forge/event.go
    blob: 4cac32acfe47
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: cccaa62747de
confidence: suspect
---

A `gh-readonly-queue/<base>/pr-<number>-<sha>` ref's trailing `<sha>` is the base branch's tip the entry was replayed onto when the group was formed, not the pull request's head SHA. The explorer verified this against GitHub's merge-queue documentation (not re-checkable from the repository, hence `suspect`). It contradicts ADR 0053's prose ("the queue ref names the PR's head SHA directly"), which is wrong on this one point. `forge.QueueEntry` (`internal/forge/event.go:186`) states the correction in its doc comment, and both the CLI and the relay (`queuePullRequest` in `pr-relay/src/index.ts`) resolve the PR's real head live rather than trust anything parsed from the ref — required anyway by `.claude/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md`. Anyone trusting that one ADR sentence would design a mechanism reading the wrong revision as a head.
