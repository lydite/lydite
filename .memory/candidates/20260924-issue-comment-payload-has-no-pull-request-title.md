---
about: an issue_comment payload carries no pull_request.title, so forge.LoadPullRequestEvent silently parses an empty title from one; clearance now avoids payload fields entirely, but review's decide stage still reads the title from the payload and would go silent on a non-pull_request event
saw:
  - source/cli/internal/forge/event.go
  - source/cli/internal/forge/calls.go
  - source/cli/internal/stages/clearance/fingerprint.go
  - source/cli/internal/reviewdecision/title.go
  - source/cli/internal/stages/review/decide.go
  - source/cli/cmd/lydite/review.go
---

Re-checked on `refactor/flow-architecture-clearance-pilot`. The payload fact still holds; how
clearance meets it changed.

**The payload fact.** An `issue_comment` payload has no top-level `pull_request` key (only
`issue.pull_request`, a URL reference), so `forge.LoadPullRequestEvent` (`internal/forge/event.go`)
unmarshals one into a `PullRequestEvent` whose `PullRequest.Title` is empty, with no error.
`issue.title` is the thread's title, which equals the PR's only by convention.

**Clearance no longer reads payload fields at all.** `cmd/lydite/clearance.go` reads the payload
through `forge.ReadCommentRef`, which extracts only `comment.id` and `repository.full_name`;
body, author and PR number come live from `forge.Client.IssueComment`. The title is resolved live
inside the Fingerprint stage (`clearedDecision` in `internal/stages/clearance/fingerprint.go`) as
the lazy `Title` func handed to `reviewdecision.Decide`, calling
`SCMRepository.PullRequestTitle` (`GET /repos/:o/:r/pulls/:n`, `internal/forge/calls.go`). A
failed lookup is a warning ("could not resolve the pull request's title ... a break declared
only there is not seen"), not fatal, since the title can only add a referral.

**Where the trap remains.** `review` still reads the title from the payload:
`reviewdecision.PullRequestTitle(eventPath)` (`internal/reviewdecision/title.go`) calls
`LoadPullRequestEvent` and returns `event.PullRequest.Title`, and the `decide` stage
(`internal/stages/review/decide.go`) wires it as `reviewdecision.Decide`'s `Title` func over the
event path `cmd/lydite/review.go` resolves once from `--event` or `GITHUB_EVENT_PATH`. Correct for
a `pull_request` trigger; on any other event shape it returns "" silently and a title-only break
declaration is missed. Reading the title live was considered for `review` and rejected (ADR 0071:
the title can only add a referral, and a live read needs a token on every run). Any new code path
that reads a pull-request field off a payload must check which event produced the payload, or
resolve the field live as clearance does.
