---
about: review/status still read the webhook payload directly for the pull request title and the head SHA, the exact pattern ADR 0061 replaced with a live SCMRepository read when clearance was built on Flow
saw:
  - source/cli/cmd/lydite/review_apisurface.go
  - source/cli/cmd/lydite/status.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - docs/adr/0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md
---

`cmd/lydite/review_apisurface.go`'s `pullRequestTitle` (review_apisurface.go:135-148) reads
`event.PullRequest.Title` straight out of the webhook payload loaded from `GITHUB_EVENT_PATH`,
and returns "" silently whenever there is no event path or the load fails, with only a warning
printed. `cmd/lydite/status.go`'s `resolvePullRequest` (status.go:74-89) does the same for the
head SHA and PR number: `event.PullRequest.Head.SHA`/`event.Number`, read once at delivery time.

This is the identical shape ADR 0061 rejects for `clearance`: a payload is a copy taken at
delivery time and can be stale (title edited after push, since `review` now re-runs on `edited`)
or simply point at data the process could instead ask the platform for live. The Flow-based
`clearance` migration already solved this for the title:
`clearancestages.Fingerprint` (fingerprint.go:136-143) resolves it live via
`in.Repository.PullRequestTitle(ctx, in.Number)` through `forge.SCMRepository`, warning (not
failing) on error — the same "additive source only" reasoning `review`'s comment describes, but
resolved live instead of from a payload.

Any Flow migration of `review`/`status` should replace `pullRequestTitle` and
`resolvePullRequest`'s payload-only reads with the equivalent `SCMRepository` calls
(`PullRequestTitle`, and a head resolved via `HeadSHA` the way `scmstages.ResolveHead` already
does for clearance) rather than porting the payload-reading functions over unchanged. Note
`SCMRepository.PullRequestTitle` already exists and is exercised in production by clearance, so
no new interface method is needed for the title; a head-SHA equivalent already exists too
(`HeadSHA`, used by `scmstages.ResolveHead`).
