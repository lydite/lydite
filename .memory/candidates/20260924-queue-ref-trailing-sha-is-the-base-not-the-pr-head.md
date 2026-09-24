---
about: source/cli/internal/forge/event.go
saw: source/cli/internal/forge/event.go (QueueEntry), source/cloud-services/pr-relay/src/index.ts (queuePullRequest)
---

A `gh-readonly-queue/<base>/pr-<number>-<sha>` ref's trailing `<sha>` is the base branch's tip
the entry was replayed onto when the group was formed, not the pull request's head SHA —
verified against GitHub's own merge-queue documentation while implementing ADR 0053. This
contradicts ADR 0053's own prose ("the queue ref names the PR's head SHA directly"), which is
wrong on this one point; `forge.QueueEntry`'s doc comment states the correction, and both the CLI
(`event.go`) and the relay (`index.ts`) resolve the pull request's real head live rather than
trusting anything parsed out of the ref — required anyway by
`.claude/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md`, independent
of the ADR's wording error.

Anyone reading `docs/adr/0053-a-clearance-carries-forward-when-the-decision-it-was-given-for-is-unchanged.md`
and trusting that one sentence would design a mechanism that reads the wrong revision as a head.
