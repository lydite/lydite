# A review reads its pull request from the payload, and posts to the revision it measured

`reviewflow.New` takes the pull request a review is about — its number, its head revision and its
title — from the webhook payload, loaded by the CLI from `--event` or `GITHUB_EVENT_PATH` and
handed to the flow as an input. The credential and the repository do not come from there: they
come from `truststages.InitTrust` and `scmstages.InitSCM`, and the status is written through
`forge.SCMRepository.PostStatus`. Both of those stages are declared late — after the decision,
ahead of only the stages that load the pull request and compose and post its status — and run
only for a run that posts directly (`--publish` without `--status-out`). A review
that only renders, or renders a status document for another step to post, never reaches them and
holds no credential. This is the opposite of [ADR 0061](0061-trust-and-the-repository-come-first-and-a-webhook-payload-only-points.md)'s
ordering for clearance, and this record says why the two commands differ.

## Rejected: resolving the head revision live

A review's status belongs to the revision whose tree the run measured. `HeadSHA` answers what the
pull request's head is *now*; a push landing while the run is working moves it, and posting to
the live answer would put a verdict on a commit nothing measured — a pass for code no gate ever
saw. The payload's head is the revision the run was started for, which is exactly the one the
verdict is about. [The rule on resolving a write's target live](../../agentic/rules/resolve-a-writes-target-live-never-trust-the-claim-or-the-body.md)
compares a claimed target with the live one and refuses a mismatch; it never substitutes the live
value for the claim. Clearance resolves its head live because a clearance is a human's statement
about whatever the head is when they speak, which is a different question.

## Rejected: resolving the title live

The title can only ever add a referral — a breaking-change declaration in it turns an undeclared
API break (a failure, [ADR 0040](0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md))
into a declared one (a referral). A stale title therefore costs nothing a reviewer needs: at worst
it misses a declaration made after the run started, and a missed declaration fails, which is the
conservative direction. Resolving it live would need a credential on every review, including the
render-only runs that deliberately hold none, and a run that rendered its status and a run that
posted it could then disagree about the same pull request's verdict.

## Rejected: trust and the repository first

Clearance declares them first because resolving its comment needs an `SCMRepository` to fetch
through and a trusted repository to compare the payload's claim against. Nothing a review does
before its post needs either, so declaring them first would only make every run that posts fail
earlier on a missing credential — before its comparison, its warnings and its report — and would
buy no safety, since the credential is used for one write at the end.

## Consequences

- A missing or malformed `GITHUB_REPOSITORY` is reported in `internal/trust`'s own words, and is
  reported ahead of a missing token, because `trust.FromEnvironment` checks the repository first.
  That differs from the pre-Flow command's wording, and only in a misconfigured job. `trust`
  returns untyped errors, so restoring the old wording would mean matching its message text, which
  couples the CLI to another package's prose. A missing token (`scmstages.ErrNoCredential`) and a
  missing or non-pull-request event keep their exact texts, mapped back in the CLI from typed
  errors.
- Nothing below the CLI reads `GITHUB_EVENT_PATH`. The CLI resolves the path once; the stage that
  loads the pull request and the domain function that reads its title both take it as an input.
