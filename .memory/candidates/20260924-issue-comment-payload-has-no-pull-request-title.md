---
name: issue-comment-payload-has-no-pull-request-title
kind: gotcha
about: source/cli/internal/forge/event.go
description: forge.CommentEvent (an issue_comment webhook payload) carries no pull_request.title field at all — only event.Issue.Title, the comment thread's own title, which is the pull request's title only by convention. forge.LoadPullRequestEvent's PullRequestEvent.Title parses to empty silently when fed a comment payload instead of a pull_request payload.
anchors:
  - path: source/cli/internal/forge/event.go
    blob: 710323ae40bd4d02cda3ce73baa061a0311ed75c
  - path: source/cli/cmd/lydite/clearance.go
    blob: e7a94110329c8f991d1061563a84747b80217de6
confidence: verified
---

`cmd/lydite/review_apisurface.go`'s `pullRequestTitle(warn, eventPath)` calls
`forge.LoadPullRequestEvent(eventPath)` and reads `event.PullRequest.Title`. That is
correct for `review`, which is always given a `pull_request` webhook payload. It silently
returns an empty title for ANY other payload shape, including an `issue_comment` payload
— `forge.CommentEvent`'s JSON has no top-level `pull_request` key at all (only
`issue.pull_request`, which is just a URL reference), so `json.Unmarshal` leaves
`PullRequestEvent.Title` at its zero value with no error.

`cmd/lydite/clearance.go` (`/lydite clear`'s command) is answering an `issue_comment`
event, so calling `breakDeclaration` with `opt.eventPath` and letting it fall through to
`pullRequestTitle` silently misses a break declared only in the pull request's title — a
panel-code-review `deep` pass caught this (AMBER, correctness, high confidence,
corroborated by both correctness reviewers) on `feat/a-clearance-records-its-fingerprint`.
The fix: resolve the title live through the platform instead —
`client.PullRequestTitle(ctx, repo, number)` (`GET /repos/:o/:r/pulls/:n`, added in
`internal/forge/calls.go` alongside the existing `HeadSHA`) — and pass the resolved
string into `breakDeclaration`/`renderAPISurfaceRows`, which now take a `title string`
parameter instead of an `eventPath string`. Any future code path that reads a
pull-request-shaped field off an event payload has to check which webhook event actually
produced that payload first; the two share no common shape for fields outside what both
explicitly carry.
