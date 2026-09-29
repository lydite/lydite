---
name: issue-comment-payload-has-no-pull-request-title
kind: gotcha
description: An issue_comment payload carries no pull_request.title, so LoadPullRequestEvent silently parses an empty one; clearance resolves the title live, but review still reads it from the payload.
anchors:
  - path: source/cli/internal/forge/event.go
    blob: 4cac32acfe47
  - path: source/cli/internal/reviewdecision/title.go
    blob: 6ff06f915dc6
  - path: source/cli/internal/stages/review/decide.go
    blob: 73e3a1f747ae
  - path: source/cli/internal/stages/clearance/fingerprint.go
    blob: 516836b3bf3a
confidence: verified
---

An `issue_comment` payload has no top-level `pull_request` key (only `issue.pull_request`, a URL reference), so `forge.LoadPullRequestEvent` (`internal/forge/event.go`) unmarshals one into a `PullRequestEvent` whose `PullRequest.Title` is empty, with no error. `issue.title` is the thread's title, equal to the PR's only by convention.

**Clearance avoids the trap.** It reads the payload only through `forge.ReadCommentRef` (`event.go:78`, `comment.id` and `repository.full_name`), and resolves the title live inside the Fingerprint stage: `SCMRepository.PullRequestTitle` (`fingerprint.go:137`, `internal/forge/calls.go:44`, `GET /repos/:o/:r/pulls/:n`). A failed lookup is a warning ("a break declared only there is not seen"), not fatal, since the title can only add a referral.

**Review still has it.** `reviewdecision.PullRequestTitle(eventPath)` (`internal/reviewdecision/title.go:19`) calls `LoadPullRequestEvent` and returns `event.PullRequest.Title`, wired as `Decide`'s `Title` func by the `decide` stage (`internal/stages/review/decide.go`). Correct for a `pull_request` trigger; on any other event shape it returns "" silently and a title-only break declaration is missed. Reading it live for `review` was considered and rejected (ADR 0071: the title can only add a referral, and a live read needs a token on every run). Any new code reading a pull-request field off a payload must check which event produced the payload, or resolve the field live as clearance does.
