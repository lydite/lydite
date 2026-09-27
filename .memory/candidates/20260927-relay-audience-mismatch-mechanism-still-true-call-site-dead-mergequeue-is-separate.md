---
about: the silent-fallback mechanism the note describes is unchanged in action.yml, but its one invoking workflow (lydite-pr.yml) does not exist in this repo, and mergequeue's own relay call is a different, already-hardened code path that fails loud rather than falling back
saw:
  - .github/actions/lydite-comment/action.yml
  - source/cloud-services/pr-relay/wrangler.toml
  - source/cli/cmd/lydite/mergequeue.go
targets: relay-audience-mismatch-degrades-silently
verdict: still-true
---

Re-checked while answering a question about migrating `mergequeue`'s relay call onto Flow.

The mechanism itself is unchanged: `AUDIENCE = "https://pr.lydite.org"` is still hardcoded in
`source/cloud-services/pr-relay/wrangler.toml:19`, and `.github/actions/lydite-comment/action.yml`
still exists on disk with the same silent-fallback shape the note describes. `still-true` for the
claim as a statement about that action's own code.

But one of the note's own anchors is genuinely gone, not just moved: `.github/workflows/lydite-pr.yml`
does not exist in this repository (`ls .github/workflows/ | grep -i lydite` → only
`gt-lydite-clearance.yml`) — confirming the separate candidate
`20260926-local-lydite-actions-composites-are-dead-in-this-repo.md`: this repo's own CI no longer
invokes `.github/actions/lydite-comment` at all (ADR 0051 moved orchestration to
`lydite/actions`' reusable workflows). So the specific failure mode described — a drifted
audience going unnoticed on a real PR comment in *this* repo's CI — has no live call site here
today; the code exists but is dead weight from this repo's own runs. It's still relevant to
anyone editing the composite for `lydite/actions`' benefit.

**Mergequeue's own relay call (`cmd/lydite/mergequeue.go`'s `submitQueueComparison`) is a
different code path, not subject to this gotcha.** It mints its own OIDC token in-process
against `--relay`'s origin (`actionsIDToken`, not through `action.yml`'s script) and its own doc
comment states the opposite policy: "Every answer but 200 fails the run, and nothing falls
back... this job holds no token that could publish anything — so an unreachable relay and a
refused request are both 'no verdict was published', which has to be said out loud." So an
audience mismatch on the queue route fails the job loudly (non-200), rather than falling back
to a bot-token comment silently. Anyone migrating the mergequeue/status stream onto Flow does
not inherit this particular gotcha from the note; it applies only to the standing-comment
posting path, not the queue path.
